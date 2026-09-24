package rpcenv_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/rpcenv"
)

func TestLoadFromEnv_NamedPlaceholders(t *testing.T) {
	t.Setenv("RPC_URLS_JSON", "")
	t.Setenv(rpcenv.EnvBaseSepolia, "https://base-sepolia.example")
	t.Setenv(rpcenv.EnvArbitrumSepolia, "https://arb-sepolia.example")
	t.Setenv(rpcenv.EnvArcTestnet, "https://arc-testnet.example")
	t.Setenv(rpcenv.EnvSolanaDevnet, "https://api.devnet.solana.com")
	// Clear CAIP-style to avoid pollution from ambient env in CI if any.
	t.Setenv("RPC_URL_eip155_84532", "")

	m, err := rpcenv.LoadFromEnv()
	require.NoError(t, err)
	assert.Equal(t, "https://base-sepolia.example", m[rpcenv.CAIP2BaseSepolia])
	assert.Equal(t, "https://arb-sepolia.example", m[rpcenv.CAIP2ArbitrumSepolia])
	assert.Equal(t, "https://arc-testnet.example", m[rpcenv.CAIP2ArcTestnet])
	assert.Equal(t, "https://api.devnet.solana.com", m[rpcenv.CAIP2SolanaDevnet])
}

func TestLoadEVMTestnetExecuteRPCs_DropsSolana(t *testing.T) {
	t.Setenv(rpcenv.EnvBaseSepolia, "https://base-sepolia.example")
	t.Setenv(rpcenv.EnvSolanaDevnet, "https://api.devnet.solana.com")
	t.Setenv(rpcenv.EnvArbitrumSepolia, "")
	t.Setenv(rpcenv.EnvArcTestnet, "")
	t.Setenv("RPC_URLS_JSON", "")

	m, err := rpcenv.LoadEVMTestnetExecuteRPCs()
	require.NoError(t, err)
	assert.Equal(t, "https://base-sepolia.example", m[rpcenv.CAIP2BaseSepolia])
	_, hasSol := m[rpcenv.CAIP2SolanaDevnet]
	assert.False(t, hasSol, "Solana must not enter EVM deposit execute map")
}

func TestLoadFromEnv_CAIPStyleStillWorks(t *testing.T) {
	t.Setenv(rpcenv.EnvBaseSepolia, "")
	t.Setenv(rpcenv.EnvArbitrumSepolia, "")
	t.Setenv(rpcenv.EnvArcTestnet, "")
	t.Setenv(rpcenv.EnvSolanaDevnet, "")
	t.Setenv("RPC_URLS_JSON", "")
	t.Setenv("RPC_URL_eip155_84532", "https://via-caip.example")

	m, err := rpcenv.LoadFromEnv()
	require.NoError(t, err)
	assert.Equal(t, "https://via-caip.example", m[rpcenv.CAIP2BaseSepolia])
}

func TestLoadFromEnv_JSONAndNamedMerge(t *testing.T) {
	t.Setenv("RPC_URLS_JSON", `{"eip155:84532":"https://from-json.example"}`)
	t.Setenv(rpcenv.EnvBaseSepolia, "https://named-wins.example")
	t.Setenv(rpcenv.EnvArbitrumSepolia, "")
	t.Setenv(rpcenv.EnvArcTestnet, "")
	t.Setenv(rpcenv.EnvSolanaDevnet, "")
	t.Setenv("RPC_URL_eip155_84532", "")

	m, err := rpcenv.LoadFromEnv()
	require.NoError(t, err)
	// Named placeholder applied after JSON → overrides.
	assert.Equal(t, "https://named-wins.example", m[rpcenv.CAIP2BaseSepolia])
}