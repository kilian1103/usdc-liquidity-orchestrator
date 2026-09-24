// Command server is a thin HTTP microservice wrapping the pure liquidity planner.
//
//	POST /v1/plan         — dry plan (execute defaults false; true fails closed unless testnet execute enabled)
//	POST /v1/payment-funding — scenario full-funding
//	POST /v1/consolidate  — Gateway deposit plan (+ optional testnet execute)
//	POST /v1/inventory    — request-scoped live balances (agent_address only; never invent from env/key)
//	GET  /v1/chains       — registered corridors (discovery for agents)
//	GET  /healthz
//
// Non-custodial: never holds product funds. Optional testnet Gateway execute requires
// ENABLE_TESTNET_EXECUTE=1 + AGENT_PRIVATE_KEY + RPCs and loopback LISTEN_ADDR only.
// Never log keys, agent addresses, balances, calldata, or RPC URLs.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/envfile"
	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/execenv"
	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/httpserver"
	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/inventory"
	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/rpcenv"
	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
)

func main() {
	if err := envfile.Load(".env"); err != nil {
		log.Fatal(err)
	}

	addr := envOr("LISTEN_ADDR", ":8088")
	ex, err := execenv.BuildExecutor(execenv.Options{RequireLoopbackListen: addr})
	if err != nil {
		log.Fatal(err)
	}
	if os.Getenv("ENABLE_TESTNET_EXECUTE") == "1" {
		log.Printf("testnet gateway execute enabled (deposit + withdraw; loopback only)")
	}
	s := &http.Server{
		Addr: addr,
		Handler: httpserver.LogRequests(httpserver.NewMuxWithOptions(httpserver.MuxOptions{
			Executor:      ex,
			LoadInventory: loadInventory,
		})),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("usdc-liquidity-orchestrator listening on %s", addr)
	log.Fatal(s.ListenAndServe())
}

// loadInventory is request-scoped live load: agent from body only (never env/key).
// Empty RPC map → 503 pre-net; 30s timeout; Gateway soft-skip inside inventory.Load.
func loadInventory(ctx context.Context, agentAddress string) (liquidity.Inventory, error) {
	rpcs, err := rpcenv.LoadEVMTestnetExecuteRPCs()
	if err != nil {
		return liquidity.Inventory{}, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"inventory: RPC configuration unavailable")
	}
	if len(rpcs) == 0 {
		return liquidity.Inventory{}, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"inventory: RPC map required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return inventory.Load(ctx, inventory.Config{
		AgentAddress: agentAddress,
		RPCs:         rpcs,
		GatewayAPI:   strings.TrimSpace(os.Getenv("GATEWAY_API_BASE")),
	})
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}