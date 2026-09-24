// Package rpcenv loads JSON-RPC / Solana RPC URLs from environment variables.
// Values are never logged. EVM execute only uses GatewayOK testnet CAIP-2 keys.
package rpcenv

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
)

// Friendly env names (placeholders) → CAIP-2 chain id.
const (
	EnvBaseSepolia     = "RPC_URL_BASE_SEPOLIA"
	EnvArbitrumSepolia = "RPC_URL_ARBITRUM_SEPOLIA"
	EnvArcTestnet      = "RPC_URL_ARC_TESTNET"
	EnvSolanaDevnet    = "RPC_URL_SOLANA_DEVNET"
)

// Well-known CAIP-2 ids for registered / planned corridors.
const (
	CAIP2BaseSepolia     = "eip155:84532"
	CAIP2ArbitrumSepolia = "eip155:421614"
	CAIP2ArcTestnet      = "eip155:5042002"
	// Solana Devnet CAIP-2 (registry / execute not yet EVM-style; URL stored for operators).
	CAIP2SolanaDevnet = "solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1"
)

// NamedEnvToCAIP2 maps operator-friendly env var names to CAIP-2.
var NamedEnvToCAIP2 = map[string]string{
	EnvBaseSepolia:     CAIP2BaseSepolia,
	EnvArbitrumSepolia: CAIP2ArbitrumSepolia,
	EnvArcTestnet:      CAIP2ArcTestnet,
	EnvSolanaDevnet:    CAIP2SolanaDevnet,
}

// LoadFromEnv merges RPC maps from:
//  1. RPC_URLS_JSON object (CAIP-2 → URL)
//  2. Named placeholders: RPC_URL_BASE_SEPOLIA, RPC_URL_ARBITRUM_SEPOLIA,
//     RPC_URL_ARC_TESTNET, RPC_URL_SOLANA_DEVNET
//  3. RPC_URL_eip155_<id> → eip155:<id>
//
// Later sources override earlier for the same CAIP-2. Empty values are refused.
// Never logs URLs.
func LoadFromEnv() (map[string]string, error) {
	out := map[string]string{}

	if raw := strings.TrimSpace(os.Getenv("RPC_URLS_JSON")); raw != "" {
		var m map[string]string
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, fmt.Errorf("RPC_URLS_JSON: invalid JSON object")
		}
		for k, v := range m {
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			if k == "" || v == "" {
				return nil, fmt.Errorf("RPC_URLS_JSON: empty key or value refused")
			}
			out[k] = v
		}
	}

	for envName, caip := range NamedEnvToCAIP2 {
		if v := strings.TrimSpace(os.Getenv(envName)); v != "" {
			out[caip] = v
		}
	}

	const prefix = "RPC_URL_"
	for _, e := range os.Environ() {
		eq := strings.IndexByte(e, '=')
		if eq <= 0 {
			continue
		}
		name, val := e[:eq], strings.TrimSpace(e[eq+1:])
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		// Named placeholders already handled above; skip re-mapping as eip155.
		if _, named := NamedEnvToCAIP2[name]; named {
			continue
		}
		rest := strings.TrimPrefix(name, prefix)
		if rest == "" {
			continue
		}
		if val == "" {
			// Unset / blank placeholder — ignore (named vars already skip empty).
			continue
		}
		var caip string
		if strings.HasPrefix(rest, "eip155_") {
			caip = "eip155:" + strings.TrimPrefix(rest, "eip155_")
		} else if strings.HasPrefix(rest, "solana_") {
			// RPC_URL_solana_* → solana:* (ops alias; execute filters non-EVM)
			caip = "solana:" + strings.TrimPrefix(rest, "solana_")
		} else {
			// Unknown shapes (e.g. RPC_URL_FOO) — skip rather than invent bad CAIP-2
			continue
		}
		out[caip] = val
	}
	return out, nil
}

// LoadEVMTestnetExecuteRPCs returns only chains eligible for DepositExecutor
// (registered GatewayOK testnets). Solana and mainnet entries are dropped, not errors.
func LoadEVMTestnetExecuteRPCs() (map[string]string, error) {
	all, err := LoadFromEnv()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(all))
	for caip, url := range all {
		if liquidity.IsTestnetExecutableChain(caip) {
			out[caip] = url
		}
	}
	return out, nil
}