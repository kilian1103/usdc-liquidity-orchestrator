package execenv_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/execenv"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
)

func TestIsLoopbackListen(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8088", true},
		{"localhost:8088", true},
		{"[::1]:8088", true},
		{":8088", false},
		{"0.0.0.0:8088", false},
		{"192.168.1.1:8088", false},
	}
	for _, tc := range tests {
		t.Run(tc.addr, func(t *testing.T) {
			assert.Equal(t, tc.want, execenv.IsLoopbackListen(tc.addr))
		})
	}
}

func TestBuildExecutor_EnableOff_Unconfigured(t *testing.T) {
	t.Setenv("ENABLE_TESTNET_EXECUTE", "")
	t.Setenv("AGENT_PRIVATE_KEY", "")
	ex, err := execenv.BuildExecutor(execenv.Options{})
	require.NoError(t, err)
	_, ok := ex.(liquidity.UnconfiguredExecutor)
	assert.True(t, ok, "ENABLE≠1 must return UnconfiguredExecutor")
}

func TestBuildExecutor_EnableOn_IncompleteKey(t *testing.T) {
	t.Setenv("ENABLE_TESTNET_EXECUTE", "1")
	t.Setenv("AGENT_PRIVATE_KEY", "")
	t.Setenv("RPC_URL_BASE_SEPOLIA", "https://example.invalid")
	ex, err := execenv.BuildExecutor(execenv.Options{})
	assert.Nil(t, ex)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AGENT_PRIVATE_KEY")
}

func TestBuildExecutor_EnableOn_NoRPC(t *testing.T) {
	t.Setenv("ENABLE_TESTNET_EXECUTE", "1")
	t.Setenv("AGENT_PRIVATE_KEY", "0x"+hex64("ab"))
	t.Setenv("RPC_URL_BASE_SEPOLIA", "")
	t.Setenv("RPC_URL_ARBITRUM_SEPOLIA", "")
	t.Setenv("RPC_URL_ARC_TESTNET", "")
	t.Setenv("RPC_URLS_JSON", "")
	// Best-effort clear alternate CAIP-2 env keys so residual process env does not supply RPCs.
	for _, e := range os.Environ() {
		eq := strings.IndexByte(e, '=')
		if eq <= 0 {
			continue
		}
		name := e[:eq]
		if strings.HasPrefix(name, "RPC_URL_eip155_") || strings.HasPrefix(name, "RPC_URL_solana_") {
			t.Setenv(name, "")
		}
	}
	ex, err := execenv.BuildExecutor(execenv.Options{})
	assert.Nil(t, ex)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RPC")
}

func TestBuildExecutor_EnableOn_NonLoopbackHTTP(t *testing.T) {
	t.Setenv("ENABLE_TESTNET_EXECUTE", "1")
	t.Setenv("AGENT_PRIVATE_KEY", "0x"+hex64("cd"))
	t.Setenv("RPC_URL_BASE_SEPOLIA", "https://example.invalid")
	ex, err := execenv.BuildExecutor(execenv.Options{RequireLoopbackListen: ":8088"})
	assert.Nil(t, ex)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loopback")
}

func TestBuildExecutor_EnableOn_CLINoLoopbackRequired(t *testing.T) {
	// CLI passes empty RequireLoopbackListen; still needs valid key + RPC.
	// Invalid key shape fails at DepositExecutor construction (sanitized path).
	t.Setenv("ENABLE_TESTNET_EXECUTE", "1")
	t.Setenv("AGENT_PRIVATE_KEY", "not-a-key")
	t.Setenv("RPC_URL_BASE_SEPOLIA", "https://example.invalid")
	ex, err := execenv.BuildExecutor(execenv.Options{})
	assert.Nil(t, ex)
	require.Error(t, err)
}

func TestGuardFromEnv_InvalidMax(t *testing.T) {
	t.Setenv("MAX_AMOUNT_ATOMIC", "not-a-number")
	_, err := execenv.GuardFromEnv()
	require.Error(t, err)
}

func TestGuardFromEnv_ValidMax(t *testing.T) {
	t.Setenv("MAX_AMOUNT_ATOMIC", "1000")
	g, err := execenv.GuardFromEnv()
	require.NoError(t, err)
	require.NotNil(t, g)
	assert.True(t, g.MaxAmountAtomic.IsPositive())
}

func TestBuildExecutor_PrivateKeyHexOverride(t *testing.T) {
	t.Setenv("ENABLE_TESTNET_EXECUTE", "1")
	t.Setenv("AGENT_PRIVATE_KEY", "") // empty env; flag/opts must supply
	t.Setenv("RPC_URL_BASE_SEPOLIA", "https://example.invalid")
	// Invalid key shape still fails at DepositExecutor — proves opts key was used (env empty).
	ex, err := execenv.BuildExecutor(execenv.Options{
		PrivateKeyHex: "not-a-key",
	})
	assert.Nil(t, ex)
	require.Error(t, err)
	// Must not require "AGENT_PRIVATE_KEY required" — opts path engaged.
	assert.NotContains(t, err.Error(), "AGENT_PRIVATE_KEY required")
}

func TestBuildExecutor_RPCsOverlayWins(t *testing.T) {
	t.Setenv("ENABLE_TESTNET_EXECUTE", "1")
	t.Setenv("AGENT_PRIVATE_KEY", "0x"+hex64("ab"))
	// No env RPCs — overlay alone must satisfy len(rpcs)>0 for testnet-exec chain.
	t.Setenv("RPC_URL_BASE_SEPOLIA", "")
	t.Setenv("RPC_URL_ARBITRUM_SEPOLIA", "")
	t.Setenv("RPC_URL_ARC_TESTNET", "")
	t.Setenv("RPC_URLS_JSON", "")
	for _, e := range os.Environ() {
		eq := strings.IndexByte(e, '=')
		if eq <= 0 {
			continue
		}
		name := e[:eq]
		if strings.HasPrefix(name, "RPC_URL_eip155_") || strings.HasPrefix(name, "RPC_URL_solana_") {
			t.Setenv(name, "")
		}
	}
	ex, err := execenv.BuildExecutor(execenv.Options{
		PrivateKeyHex: "0x" + hex64("ab"),
		RPCs: map[string]string{
			"eip155:84532": "https://overlay.example.invalid",
		},
	})
	// Key may still fail crypto parse depending on hex seed; overlay must not be "RPC required".
	if err != nil {
		assert.NotContains(t, err.Error(), "RPC required")
		assert.NotContains(t, err.Error(), "testnet EVM RPC required")
	}
	_ = ex
}

func TestBuildExecutor_RPCsOverlayIgnoredMainnet(t *testing.T) {
	t.Setenv("ENABLE_TESTNET_EXECUTE", "1")
	t.Setenv("AGENT_PRIVATE_KEY", "0x"+hex64("cd"))
	t.Setenv("RPC_URL_BASE_SEPOLIA", "")
	t.Setenv("RPC_URL_ARBITRUM_SEPOLIA", "")
	t.Setenv("RPC_URL_ARC_TESTNET", "")
	t.Setenv("RPC_URLS_JSON", "")
	for _, e := range os.Environ() {
		eq := strings.IndexByte(e, '=')
		if eq <= 0 {
			continue
		}
		name := e[:eq]
		if strings.HasPrefix(name, "RPC_URL_eip155_") || strings.HasPrefix(name, "RPC_URL_solana_") {
			t.Setenv(name, "")
		}
	}
	// Mainnet CAIP-2 only — filtered out → still no RPC.
	ex, err := execenv.BuildExecutor(execenv.Options{
		RPCs: map[string]string{
			"eip155:8453": "https://mainnet.example.invalid",
		},
	})
	assert.Nil(t, ex)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RPC")
}

func TestBuildExecutor_EnableOff_StillUnconfigured_WithOpts(t *testing.T) {
	t.Setenv("ENABLE_TESTNET_EXECUTE", "")
	ex, err := execenv.BuildExecutor(execenv.Options{
		PrivateKeyHex: "0x" + hex64("ee"),
		RPCs:          map[string]string{"eip155:84532": "https://x"},
	})
	require.NoError(t, err)
	_, ok := ex.(liquidity.UnconfiguredExecutor)
	assert.True(t, ok)
}

func hex64(seed string) string {
	// 64 hex chars for a plausible ECDSA private key shape (may still fail crypto parse).
	out := ""
	for len(out) < 64 {
		out += seed
	}
	return out[:64]
}