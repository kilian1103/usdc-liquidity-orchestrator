package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/httpserver"
	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/types"
)

// stubExecutor is a test double for stampPlanResponse matrix cases.
type stubExecutor struct {
	receipt liquidity.Receipt
	err     error
}

func (s stubExecutor) Execute(ctx context.Context, p liquidity.Plan) (liquidity.Receipt, error) {
	return s.receipt, s.err
}

const (
	arcCAIP2      = "eip155:5042002"
	arcUSDC       = "0x3600000000000000000000000000000000000000"
	baseSepCAIP2  = "eip155:84532"
	baseSepUSDC   = "0x036CbD53842c5426634e7929541eC2318f3dCF7e"
	merchantPayTo = "0xMerchantPayTo000000000000000000000001"
	agentAddr     = "0xAgentSelf000000000000000000000000000001"
)

func TestGETChains_IncludesArc(t *testing.T) {
	mux := httpserver.NewMux()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chains", nil)
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body types.ChainsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	var found bool
	for _, c := range body.Chains {
		if c.CAIP2 == arcCAIP2 {
			found = true
			assert.Equal(t, 26, c.GatewayDomain)
			assert.True(t, c.GatewayOK)
			assert.Equal(t, arcUSDC, c.USDC)
		}
	}
	assert.True(t, found, "Arc Testnet eip155:5042002 must be registered")
}

func TestGETUI_IndexHTML(t *testing.T) {
	mux := httpserver.NewMux()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	ct := rec.Header().Get("Content-Type")
	assert.Contains(t, ct, "text/html")
	body := rec.Body.String()
	assert.Contains(t, body, "USDC Liquidity Orchestrator")
	assert.Contains(t, body, "/v1/plan")
	assert.Contains(t, body, "/v1/payment-funding")
	assert.Contains(t, body, "Scale factor")
	// MVP UI polish markers
	assert.Contains(t, body, "/v1/inventory")
	assert.Contains(t, body, "Load live inventory")
	assert.Contains(t, body, "Circle Gateway")
	assert.Contains(t, body, "Activity timeline")
	assert.NotContains(t, body, "localStorage.")
	assert.NotContains(t, body, "sessionStorage.")
	assert.NotContains(t, body, "localStorage[")
	assert.NotContains(t, body, "sessionStorage[")
}

func TestPOSTPaymentFunding_ScenarioDry(t *testing.T) {
	mux := httpserver.NewMux()
	// scale 10: 300+100 human → reals 30e6 + 10e6, payment 40e6
	body := map[string]any{
		"required": map[string]any{
			"protocol":              "x402",
			"chain_caip2":           baseSepCAIP2,
			"asset":                 baseSepUSDC,
			"amount_atomic":         "40000000",
			"amount_logical_atomic": "400000000",
			"scale_factor":          10,
		},
		"inventory": map[string]any{
			"agent_address": agentAddr,
			"balances": []map[string]any{
				{"location": "native", "chain_caip2": baseSepCAIP2, "asset": baseSepUSDC, "amount_atomic": "30000000"},
				{"location": "native", "chain_caip2": "eip155:421614", "asset": "0x75faf114eafb1BDbe2F0316DF893fd58CE46AA4d", "amount_atomic": "10000000"},
			},
		},
		"sources": []map[string]any{
			{"chain_caip2": baseSepCAIP2, "amount_atomic": "30000000", "amount_logical_atomic": "300000000"},
			{"chain_caip2": "eip155:421614", "amount_atomic": "10000000", "amount_logical_atomic": "100000000"},
		},
		"execute": false,
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/payment-funding", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "circle_gateway_deposit", resp.Plan.Action)
	assert.True(t, resp.Plan.DryRun)
	assert.False(t, resp.Plan.Executed)
	assert.Contains(t, resp.Plan.Reason, "Phase A")
	require.Len(t, resp.Plan.Steps, 2) // deposits only
	assert.Equal(t, "40000000", resp.Plan.Required.AmountAtomic)
	assert.Equal(t, "400000000", resp.Plan.Required.AmountLogicalAtomic)
	assert.Equal(t, int64(10), resp.Plan.Required.ScaleFactor)
}

func TestGETHealthz_NotCapturedByStatic(t *testing.T) {
	mux := httpserver.NewMux()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok\n", rec.Body.String())
}

func TestPOSTPlan_DryStampsForced(t *testing.T) {
	mux := httpserver.NewMux()
	payload := planBody(false, merchantPayTo, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/plan", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Nil(t, resp.Error)
	assert.True(t, resp.Plan.DryRun)
	assert.False(t, resp.Plan.Executed)
	assert.True(t, resp.Plan.InventoryAsserted)
	assert.True(t, resp.Plan.InventoryUnverified)
	assert.Equal(t, "circle_gateway_withdraw", resp.Plan.Action)
}

func TestPOSTPlan_ExecuteTrue_ErrorPlusPlan(t *testing.T) {
	mux := httpserver.NewMux()
	payload := planBody(true, merchantPayTo, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/plan", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, liqerr.CodeLiquidityRailUnavailable, resp.Error.Code)
	// Dry plan still returned for inspection.
	assert.Equal(t, "circle_gateway_withdraw", resp.Plan.Action)
	assert.True(t, resp.Plan.DryRun)
	assert.False(t, resp.Plan.Executed)
	require.NotEmpty(t, resp.Plan.Steps)
}

func TestPOSTPlan_EmptyPayTo_OK(t *testing.T) {
	mux := httpserver.NewMux()
	payload := planBody(false, "", true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/plan", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Nil(t, resp.Error)
	assert.Equal(t, "circle_gateway_withdraw", resp.Plan.Action)
	assert.True(t, resp.Plan.DryRun)
}

func TestPOSTPlan_ArcGatewayWithdraw(t *testing.T) {
	mux := httpserver.NewMux()
	payload := planBody(false, merchantPayTo, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/plan", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Nil(t, resp.Error)
	assert.Equal(t, "circle_gateway_withdraw", resp.Plan.Action)
	require.NotEmpty(t, resp.Plan.Steps)
	assert.Equal(t, "circle_gateway_withdraw", resp.Plan.Steps[0].Kind)
	assert.Equal(t, agentAddr, resp.Plan.Steps[0].Recipient)
	assert.Equal(t, "agent_self", resp.Plan.Steps[0].RecipientRole)
	assert.Equal(t, arcCAIP2, resp.Plan.Steps[0].ToChainCAIP2)
	// Fee must not appear as a step.
	for _, s := range resp.Plan.Steps {
		assert.NotEqual(t, "orchestrator_fee", s.Kind)
	}
}

func TestPOSTConsolidate_MultiChainDry(t *testing.T) {
	mux := httpserver.NewMux()
	body := map[string]any{
		"inventory": map[string]any{
			"agent_address": agentAddr,
			"balances": []map[string]string{
				{
					"chain_caip2":   baseSepCAIP2,
					"asset":         baseSepUSDC,
					"amount_atomic": "3000000",
					"location":      "native",
				},
				{
					"chain_caip2":   arcCAIP2,
					"asset":         arcUSDC,
					"amount_atomic": "1000000",
					"location":      "native",
				},
			},
		},
		"execute": false,
	}
	payload, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/consolidate", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Nil(t, resp.Error)
	assert.Equal(t, "circle_gateway_consolidate", resp.Plan.Action)
	assert.True(t, resp.Plan.DryRun)
	assert.False(t, resp.Plan.Executed)
	assert.Nil(t, resp.Plan.Required)
	assert.Empty(t, resp.Plan.AmountSource)
	require.Len(t, resp.Plan.Steps, 2)
	assert.Equal(t, "circle_gateway_deposit", resp.Plan.Steps[0].Kind)
	require.Len(t, resp.Plan.Steps[0].PrepareCalls, 2)
	assert.Equal(t, "approve", resp.Plan.Steps[0].PrepareCalls[0].Method)
	assert.Equal(t, "deposit", resp.Plan.Steps[0].PrepareCalls[1].Method)
}

func TestPOSTConsolidate_ExecuteTrue_RailUnavailable(t *testing.T) {
	mux := httpserver.NewMux()
	payload := consolidateBody(true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/consolidate", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, liqerr.CodeLiquidityRailUnavailable, resp.Error.Code)
	assert.Equal(t, "circle_gateway_consolidate", resp.Plan.Action)
	assert.True(t, resp.Plan.DryRun)
	assert.Nil(t, resp.Receipt)
}

func TestStampMatrix_ExecuteFalse_ForceDry(t *testing.T) {
	// Even with a succeeding executor, execute=false must force dry stamps and no receipt.
	mux := httpserver.NewMuxWithOptions(httpserver.MuxOptions{
		Executor: stubExecutor{receipt: liquidity.Receipt{TxHashes: []string{"0xshouldnot"}}},
	})
	payload := consolidateBody(false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/consolidate", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Nil(t, resp.Error)
	assert.True(t, resp.Plan.DryRun)
	assert.False(t, resp.Plan.Executed)
	assert.Nil(t, resp.Receipt)
}

func TestStampMatrix_ExecuteSuccess_ExecutedTrue(t *testing.T) {
	mux := httpserver.NewMuxWithOptions(httpserver.MuxOptions{
		Executor: stubExecutor{receipt: liquidity.Receipt{TxHashes: []string{"0xabc", "0xdef"}}},
	})
	payload := consolidateBody(true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/consolidate", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Nil(t, resp.Error)
	assert.False(t, resp.Plan.DryRun)
	assert.True(t, resp.Plan.Executed)
	require.NotNil(t, resp.Receipt)
	assert.Equal(t, []string{"0xabc", "0xdef"}, resp.Receipt.TxHashes)
}

func TestStampMatrix_PartialFail_ExecutedFalseWithReceipt(t *testing.T) {
	mux := httpserver.NewMuxWithOptions(httpserver.MuxOptions{
		Executor: stubExecutor{
			receipt: liquidity.Receipt{TxHashes: []string{"0xpartial"}},
			err:     liqerr.New(liqerr.CodeLiquidityRailUnavailable, "deposit execute: broadcast failed"),
		},
	})
	payload := consolidateBody(true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/consolidate", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, liqerr.CodeLiquidityRailUnavailable, resp.Error.Code)
	assert.Equal(t, "deposit execute: broadcast failed", resp.Error.Message)
	assert.False(t, resp.Plan.DryRun)
	assert.False(t, resp.Plan.Executed)
	require.NotNil(t, resp.Receipt)
	assert.Equal(t, []string{"0xpartial"}, resp.Receipt.TxHashes)
}

func TestStampMatrix_FailZeroHashes_ForceDry(t *testing.T) {
	mux := httpserver.NewMuxWithOptions(httpserver.MuxOptions{
		Executor: stubExecutor{
			err: liqerr.Wrap(liqerr.CodeLiquidityRailUnavailable,
				assert.AnError, "deposit execute: broadcast failed"),
		},
	})
	payload := consolidateBody(true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/consolidate", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Error)
	// Sanitized: Message is fixed field, not wrapped cause string.
	assert.Equal(t, "deposit execute: broadcast failed", resp.Error.Message)
	assert.NotContains(t, resp.Error.Message, "assert.AnError")
	assert.True(t, resp.Plan.DryRun)
	assert.False(t, resp.Plan.Executed)
	assert.Nil(t, resp.Receipt)
}

func consolidateBody(execute bool) []byte {
	body := map[string]any{
		"inventory": map[string]any{
			"agent_address": agentAddr,
			"balances": []map[string]string{{
				"chain_caip2": arcCAIP2, "asset": arcUSDC,
				"amount_atomic": "1000", "location": "native",
			}},
		},
		"execute": execute,
	}
	b, _ := json.Marshal(body)
	return b
}

func TestGETChains_TestnetAndGatewayWallet(t *testing.T) {
	mux := httpserver.NewMux()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chains", nil)
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body types.ChainsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	var arc, base *types.ChainInfo
	for i := range body.Chains {
		c := &body.Chains[i]
		if c.CAIP2 == arcCAIP2 {
			arc = c
		}
		if c.CAIP2 == "eip155:8453" {
			base = c
		}
	}
	require.NotNil(t, arc)
	assert.True(t, arc.Testnet)
	assert.Equal(t, "0x0077777d7EBA4688BDeF3E311b846F25870A19B9", arc.GatewayWallet)
	require.NotNil(t, base)
	assert.False(t, base.Testnet)
	assert.Equal(t, "0x77777777Dcc4d5A8B6E418Fd04D8997ef11000eE", base.GatewayWallet)
}

func TestPOSTInventory_NilLoader_503BareAPIError(t *testing.T) {
	mux := httpserver.NewMux() // LoadInventory nil
	payload, _ := json.Marshal(map[string]string{"agent_address": agentAddr})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/inventory", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var api types.APIError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &api))
	assert.Equal(t, liqerr.CodeLiquidityRailUnavailable, api.Code)
	assert.NotEmpty(t, api.Message)
	// bare APIError — not PlanResponse
	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	_, hasPlan := raw["plan"]
	assert.False(t, hasPlan)
}

func TestPOSTInventory_InjectSuccess_200NoStore(t *testing.T) {
	var calledWith string
	mux := httpserver.NewMuxWithOptions(httpserver.MuxOptions{
		LoadInventory: func(ctx context.Context, agent string) (liquidity.Inventory, error) {
			calledWith = agent
			return liquidity.Inventory{
				AgentAddress: agent,
				Balances: []liquidity.Balance{{
					ChainCAIP2:   baseSepCAIP2,
					Asset:        baseSepUSDC,
					AmountAtomic: decimal.RequireFromString("1000000"),
					Location:     liquidity.LocationNative,
				}},
			}, nil
		},
	})
	payload, _ := json.Marshal(map[string]string{"agent_address": agentAddr})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/inventory", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, agentAddr, calledWith)
	var inv types.Inventory
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &inv))
	assert.Equal(t, agentAddr, inv.AgentAddress)
	require.Len(t, inv.Balances, 1)
	assert.Equal(t, "1000000", inv.Balances[0].AmountAtomic)
	assert.Equal(t, liquidity.LocationNative, inv.Balances[0].Location)
}

func TestPOSTInventory_InjectFail_503BareAPIError(t *testing.T) {
	mux := httpserver.NewMuxWithOptions(httpserver.MuxOptions{
		LoadInventory: func(ctx context.Context, agent string) (liquidity.Inventory, error) {
			return liquidity.Inventory{}, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
				"inventory: balanceOf call failed")
		},
	})
	payload, _ := json.Marshal(map[string]string{"agent_address": agentAddr})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/inventory", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var api types.APIError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &api))
	assert.Equal(t, liqerr.CodeLiquidityRailUnavailable, api.Code)
	assert.Equal(t, "inventory: balanceOf call failed", api.Message)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	_, hasPlan := raw["plan"]
	assert.False(t, hasPlan)
}

func TestPOSTInventory_InvalidJSON_400(t *testing.T) {
	mux := httpserver.NewMuxWithOptions(httpserver.MuxOptions{
		LoadInventory: func(ctx context.Context, agent string) (liquidity.Inventory, error) {
			t.Fatal("LoadInventory must not run on invalid JSON")
			return liquidity.Inventory{}, nil
		},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/inventory", bytes.NewReader([]byte(`{not json`)))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var api types.APIError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &api))
	assert.Equal(t, liqerr.CodeInvalidQuery, api.Code)
}

func TestPOSTInventory_EmptyAgent_400(t *testing.T) {
	mux := httpserver.NewMuxWithOptions(httpserver.MuxOptions{
		LoadInventory: func(ctx context.Context, agent string) (liquidity.Inventory, error) {
			t.Fatal("LoadInventory must not run on empty agent")
			return liquidity.Inventory{}, nil
		},
	})
	payload, _ := json.Marshal(map[string]string{"agent_address": "  "})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/inventory", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var api types.APIError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &api))
	assert.Equal(t, liqerr.CodeInvalidQuery, api.Code)
}

func TestPOSTPlan_DoesNotInvokeLoadInventory(t *testing.T) {
	loadCalls := 0
	mux := httpserver.NewMuxWithOptions(httpserver.MuxOptions{
		LoadInventory: func(ctx context.Context, agent string) (liquidity.Inventory, error) {
			loadCalls++
			return liquidity.Inventory{}, nil
		},
	})
	payload := planBody(false, merchantPayTo, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/plan", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 0, loadCalls)
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.Plan.InventoryAsserted)
	assert.True(t, resp.Plan.InventoryUnverified)
	assert.True(t, resp.Plan.DryRun)
}

func planBody(execute bool, payTo string, withGateway bool) []byte {
	balances := []map[string]string{}
	if withGateway {
		balances = append(balances, map[string]string{
			"asset":         "USDC",
			"amount_atomic": "100000000",
			"location":      "circle_gateway",
		})
	}
	balances = append(balances, map[string]string{
		"chain_caip2":   arcCAIP2,
		"asset":         arcUSDC,
		"amount_atomic": "20000000",
		"location":      "native",
	})
	req := map[string]string{
		"chain_caip2":   arcCAIP2,
		"asset":         arcUSDC,
		"amount_atomic": "42000000",
	}
	if payTo != "" {
		req["pay_to"] = payTo
		req["pay_to_role"] = "merchant"
		req["protocol"] = "x402"
	}
	body := map[string]any{
		"required": req,
		"inventory": map[string]any{
			"agent_address": agentAddr,
			"balances":      balances,
		},
		"orchestration": map[string]any{
			"target_chain_caip2": arcCAIP2,
			"prefer_rail":        "circle_gateway",
		},
		"execute": execute,
	}
	b, _ := json.Marshal(body)
	return b
}