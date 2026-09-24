// Package execenv builds the dual-gated testnet Executor from process environment.
// Never logs keys, agent addresses, balances, calldata, or RPC URLs.
package execenv

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/rpcenv"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/execonchain"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
)

// Options configures BuildExecutor.
type Options struct {
	// RequireLoopbackListen, when non-empty and ENABLE_TESTNET_EXECUTE=1,
	// must bind only to loopback (HTTP server). Empty skips the check (CLI).
	RequireLoopbackListen string
	// PrivateKeyHex, when non-empty, overrides AGENT_PRIVATE_KEY. Never log.
	PrivateKeyHex string
	// RPCs optional overlay after env load; flag/CAIP-2 keys win. Only
	// testnet-executable chains are kept for DepositExecutor.
	RPCs map[string]string
}

// BuildExecutor returns UnconfiguredExecutor unless dual-gated testnet execute is on.
//
//	ENABLE≠1 → UnconfiguredExecutor, nil error
//	ENABLE=1 incomplete → nil, error
//	ENABLE=1 complete → DepositExecutor
func BuildExecutor(opts Options) (liquidity.Executor, error) {
	guard, err := GuardFromEnv()
	if err != nil {
		return nil, err
	}
	if os.Getenv("ENABLE_TESTNET_EXECUTE") != "1" {
		return liquidity.UnconfiguredExecutor{Guard: guard}, nil
	}
	if opts.RequireLoopbackListen != "" && !IsLoopbackListen(opts.RequireLoopbackListen) {
		return nil, fmt.Errorf("ENABLE_TESTNET_EXECUTE requires loopback LISTEN_ADDR (127.0.0.1, ::1, or localhost)")
	}
	key := strings.TrimSpace(opts.PrivateKeyHex)
	if key == "" {
		key = strings.TrimSpace(os.Getenv("AGENT_PRIVATE_KEY"))
	}
	if key == "" {
		return nil, fmt.Errorf("AGENT_PRIVATE_KEY required when ENABLE_TESTNET_EXECUTE=1")
	}
	rpcs, err := rpcenv.LoadEVMTestnetExecuteRPCs()
	if err != nil {
		return nil, err
	}
	if rpcs == nil {
		rpcs = map[string]string{}
	}
	for caip, url := range opts.RPCs {
		caip, url = strings.TrimSpace(caip), strings.TrimSpace(url)
		if caip == "" || url == "" {
			continue
		}
		if liquidity.IsTestnetExecutableChain(caip) {
			rpcs[caip] = url
		}
	}
	if len(rpcs) == 0 {
		return nil, fmt.Errorf("testnet EVM RPC required when ENABLE_TESTNET_EXECUTE=1 (RPC_URL_BASE_SEPOLIA / ARBITRUM_SEPOLIA / ARC_TESTNET, RPC_URL_eip155_*, or RPC_URLS_JSON)")
	}
	gwAPI := strings.TrimSpace(os.Getenv("GATEWAY_API_BASE"))
	ex, err := execonchain.NewDepositExecutor(execonchain.Config{
		PrivateKeyHex: key,
		RPCs:          rpcs,
		Guard:         guard,
		GatewayAPI:    gwAPI,
	})
	if err != nil {
		return nil, err
	}
	return ex, nil
}

// GuardFromEnv builds a Guard with optional MAX_AMOUNT_ATOMIC cap.
func GuardFromEnv() (*liquidity.Guard, error) {
	g := &liquidity.Guard{}
	if raw := strings.TrimSpace(os.Getenv("MAX_AMOUNT_ATOMIC")); raw != "" {
		d, err := decimal.NewFromString(raw)
		if err != nil || !d.IsPositive() {
			return nil, fmt.Errorf("MAX_AMOUNT_ATOMIC: must be a positive decimal integer (atomic units)")
		}
		g.MaxAmountAtomic = d
	}
	return g, nil
}

// IsLoopbackListen accepts host parts that bind only to loopback.
// Bare ":port" is NOT loopback (binds all interfaces) — refuse for execute.
func IsLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = strings.Trim(addr, "[]")
		if i := strings.LastIndex(addr, ":"); i > 0 && !strings.Contains(addr, "]") {
			host = addr[:i]
		}
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}