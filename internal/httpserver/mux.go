// Package httpserver is the thin HTTP surface over pkg/liquidity via planio.
// cmd/server wires ListenAndServe; black-box tests use NewMux / NewMuxWithOptions.
package httpserver

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/planio"
	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/types"
)

//go:embed static/*
var staticEmbed embed.FS

// MuxOptions configures optional execute and inventory wiring. Zero value is plan-only.
type MuxOptions struct {
	// Executor runs plans when request execute=true. Nil → UnconfiguredExecutor.
	Executor liquidity.Executor
	// LoadInventory loads request-scoped live balances for POST /v1/inventory.
	// Nil → 503 liquidity_rail_unavailable (no network). Plan path never calls this.
	LoadInventory func(ctx context.Context, agentAddress string) (liquidity.Inventory, error)
}

// NewMux returns the service routes (plan-only default: UnconfiguredExecutor).
// Serves a small plan-only UI at GET / (embedded static/).
func NewMux() http.Handler {
	return NewMuxWithOptions(MuxOptions{})
}

// NewMuxWithOptions returns routes with optional live Executor and inventory loader.
func NewMuxWithOptions(opts MuxOptions) http.Handler {
	ex := opts.Executor
	if ex == nil {
		ex = liquidity.UnconfiguredExecutor{}
	}
	s := &server{ex: ex, loadInventory: opts.LoadInventory}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /v1/chains", handleChains)
	mux.HandleFunc("POST /v1/plan", s.handlePlan)
	mux.HandleFunc("POST /v1/consolidate", s.handleConsolidate)
	mux.HandleFunc("POST /v1/payment-funding", s.handlePaymentFunding)
	mux.HandleFunc("POST /v1/inventory", s.handleInventory)

	staticRoot, err := fs.Sub(staticEmbed, "static")
	if err != nil {
		panic("httpserver: embed static/: " + err.Error())
	}
	mux.Handle("GET /", http.FileServer(http.FS(staticRoot)))
	return mux
}

type server struct {
	ex            liquidity.Executor
	loadInventory func(ctx context.Context, agentAddress string) (liquidity.Inventory, error)
}

// LogRequests wraps next logging method + path only (never query, body, agent, balances).
func LogRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func handleChains(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, planio.ListChains())
}

func (s *server) handlePlan(w http.ResponseWriter, r *http.Request) {
	var req types.PlanRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, liqerr.CodeInvalidQuery, "invalid JSON body")
		return
	}
	resp, outcome := planio.RunPlan(r.Context(), s.ex, req)
	writeJSON(w, planio.HTTPStatus(outcome), resp)
}

func (s *server) handleConsolidate(w http.ResponseWriter, r *http.Request) {
	var req types.ConsolidateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, liqerr.CodeInvalidQuery, "invalid JSON body")
		return
	}
	resp, outcome := planio.RunConsolidate(r.Context(), s.ex, req)
	writeJSON(w, planio.HTTPStatus(outcome), resp)
}

func (s *server) handlePaymentFunding(w http.ResponseWriter, r *http.Request) {
	var req types.PaymentFundingRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, liqerr.CodeInvalidQuery, "invalid JSON body")
		return
	}
	resp, outcome := planio.RunPaymentFunding(r.Context(), s.ex, req)
	writeJSON(w, planio.HTTPStatus(outcome), resp)
}

func (s *server) handleInventory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	var req types.InventoryRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeAPIErr(w, http.StatusBadRequest, liqerr.CodeInvalidQuery, "invalid JSON body")
		return
	}
	agent := strings.TrimSpace(req.AgentAddress)
	if agent == "" {
		writeAPIErr(w, http.StatusBadRequest, liqerr.CodeInvalidQuery, "agent_address required")
		return
	}
	if s.loadInventory == nil {
		writeAPIErr(w, http.StatusServiceUnavailable, liqerr.CodeLiquidityRailUnavailable,
			"inventory load unavailable")
		return
	}

	inv, err := s.loadInventory(r.Context(), agent)
	if err != nil {
		code, msg, status := inventoryAPIError(err)
		writeAPIErr(w, status, code, msg)
		return
	}
	writeJSON(w, http.StatusOK, liquidity.InventoryToWire(inv))
}

// inventoryAPIError maps load errors to bare APIError fields + HTTP status.
// invalid_query → 400; rail/other → 503. Messages are sanitized (no RPC/body).
func inventoryAPIError(err error) (code, msg string, status int) {
	var e *liqerr.Error
	if errors.As(err, &e) && e != nil {
		code = e.Code
		msg = e.Message
	} else {
		code = liqerr.CodeOf(err)
		msg = "inventory load failed"
	}
	if code == "" {
		code = liqerr.CodeLiquidityRailUnavailable
	}
	if msg == "" {
		msg = "inventory load failed"
	}
	if code == liqerr.CodeInvalidQuery {
		return code, msg, http.StatusBadRequest
	}
	if code != liqerr.CodeLiquidityRailUnavailable {
		code = liqerr.CodeLiquidityRailUnavailable
	}
	return code, msg, http.StatusServiceUnavailable
}

// writeAPIErr writes a bare types.APIError (not PlanResponse) with Cache-Control: no-store.
func writeAPIErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, types.APIError{Code: code, Message: msg})
}

func writeErr(w http.ResponseWriter, code, msg string) {
	writeJSON(w, http.StatusBadRequest, types.PlanResponse{
		Error: &types.APIError{Code: code, Message: msg},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}