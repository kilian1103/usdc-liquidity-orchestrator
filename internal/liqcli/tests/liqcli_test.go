package liqcli_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/liqcli"
	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/types"
)

const planJSON = `{
  "required": {
    "protocol": "x402",
    "chain_caip2": "eip155:5042002",
    "asset": "0x3600000000000000000000000000000000000000",
    "amount_atomic": "42000000",
    "pay_to": "0xMerchantPayTo000000000000000000000001",
    "pay_to_role": "merchant"
  },
  "inventory": {
    "agent_address": "0xAgentSelf000000000000000000000000000001",
    "balances": [
      {"asset": "USDC", "amount_atomic": "100000000", "location": "circle_gateway"},
      {"chain_caip2": "eip155:5042002", "asset": "0x3600000000000000000000000000000000000000",
       "amount_atomic": "20000000", "location": "native"}
    ]
  },
  "orchestration": {
    "target_chain_caip2": "eip155:5042002",
    "prefer_rail": "circle_gateway"
  },
  "execute": false
}`

func TestMain_UsageExit2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := liqcli.Main(nil, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr.String(), "Usage")
}

func TestMain_UnknownCommandExit2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{"nope"}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 2, code)
}

func TestMain_Version(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{"version"}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout.String(), liqcli.Version)
}

func TestMain_DryPlan(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{"plan", "-f", "-"}, strings.NewReader(planJSON), &stdout, &stderr)
	assert.Equal(t, 0, code)
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &resp))
	assert.Nil(t, resp.Error)
	assert.True(t, resp.Plan.DryRun)
	assert.Equal(t, "circle_gateway_withdraw", resp.Plan.Action)
	assert.Equal(t, "agent_self", resp.Plan.Steps[0].RecipientRole)
}

func TestMain_ExecuteUnconfigured(t *testing.T) {
	t.Setenv("ENABLE_TESTNET_EXECUTE", "")
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{"plan", "--execute", "-f", "-"}, strings.NewReader(planJSON), &stdout, &stderr)
	assert.Equal(t, 1, code)
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, liqerr.CodeLiquidityRailUnavailable, resp.Error.Code)
	assert.True(t, resp.Plan.DryRun)
	// Privacy: stderr must not contain agent_address or balances.
	note := stderr.String()
	assert.NotContains(t, note, "0xAgentSelf")
	assert.NotContains(t, note, "100000000")
}

func TestMain_BodyOver1MiB(t *testing.T) {
	var stdout, stderr bytes.Buffer
	big := strings.Repeat("a", (1<<20)+10)
	code := liqcli.Main([]string{"plan", "-f", "-"}, strings.NewReader(big), &stdout, &stderr)
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "1 MiB")
}

func TestMain_Chains(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{"chains"}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 0, code)
	var body types.ChainsResponse
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &body))
	assert.NotEmpty(t, body.Chains)
}

func TestValidateInventoryArgs(t *testing.T) {
	tests := []struct {
		name      string
		flagAgent string
		envAgent  string
		wantErr   bool
	}{
		{"flag_ok", "0x" + strings.Repeat("ab", 20), "", false},
		{"env_ok", "", "0x" + strings.Repeat("cd", 20), false},
		{"missing", "", "", true},
		{"short", "0xabc", "", true},
		{"nonhex", "0x" + strings.Repeat("zz", 20), "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := liqcli.ValidateInventoryArgs(tc.flagAgent, tc.envAgent)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.NotEmpty(t, cfg.AgentAddress)
		})
	}
}

func TestValidateInventoryRPCs(t *testing.T) {
	require.Error(t, liqcli.ValidateInventoryRPCs(nil))
	require.Error(t, liqcli.ValidateInventoryRPCs(map[string]string{}))
	require.NoError(t, liqcli.ValidateInventoryRPCs(map[string]string{"eip155:84532": "https://x"}))
}

func TestMain_InventoryMissingAgent_NoNetwork(t *testing.T) {
	t.Setenv("AGENT_ADDRESS", "")
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{"inventory"}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "agent address")
}

func TestMain_InventoryMissingRPC_NoNetwork(t *testing.T) {
	t.Setenv("AGENT_ADDRESS", "0x"+strings.Repeat("11", 20))
	t.Setenv("RPC_URL_BASE_SEPOLIA", "")
	t.Setenv("RPC_URL_ARBITRUM_SEPOLIA", "")
	t.Setenv("RPC_URL_ARC_TESTNET", "")
	t.Setenv("RPC_URLS_JSON", "")
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{"inventory"}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "RPC")
}

func TestHumanUSDCToAtomic(t *testing.T) {
	t.Setenv("USDC_SCALE_FACTOR", "10") // must not affect easy conversion
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"42", "42", "42000000", false},
		{"42.5", "42.5", "42500000", false},
		{"one_micro", "0.000001", "1", false},
		{"six_frac", "1.234567", "1234567", false},
		{"seven_frac", "1.2345678", "", true},
		{"zero", "0", "", true},
		{"neg", "-1", "", true},
		{"empty", "", "", true},
		{"spaces", "  2  ", "2000000", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := liqcli.HumanUSDCToAtomic(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.String())
		})
	}
}

func TestParseAmountExclusive(t *testing.T) {
	d, err := liqcli.ParseAmountExclusive("42", "")
	require.NoError(t, err)
	assert.Equal(t, "42000000", d.String())

	d, err = liqcli.ParseAmountExclusive("", "1000")
	require.NoError(t, err)
	assert.Equal(t, "1000", d.String())

	_, err = liqcli.ParseAmountExclusive("1", "1")
	require.Error(t, err)
	_, err = liqcli.ParseAmountExclusive("", "")
	require.Error(t, err)
	_, err = liqcli.ParseAmountExclusive("", "1.5")
	require.Error(t, err)
	_, err = liqcli.ParseAmountExclusive("", "0")
	require.Error(t, err)
}

func TestParseChainAmountKV_AndRPC(t *testing.T) {
	caip, amt, err := liqcli.ParseChainAmountKV("6=20", true)
	require.NoError(t, err)
	assert.Equal(t, "eip155:84532", caip)
	assert.Equal(t, "20000000", amt.String())

	caip, amt, err = liqcli.ParseChainAmountKV("base-sepolia=1.5", true)
	require.NoError(t, err)
	assert.Equal(t, "eip155:84532", caip)
	assert.Equal(t, "1500000", amt.String())

	_, _, err = liqcli.ParseChainAmountKV("nope", true)
	require.Error(t, err)

	caip, url, err := liqcli.ParseRPCOverride("6=https://example.invalid/rpc", true)
	require.NoError(t, err)
	assert.Equal(t, "eip155:84532", caip)
	assert.Equal(t, "https://example.invalid/rpc", url)

	_, _, err = liqcli.ParseRPCOverride("6=", true)
	require.Error(t, err)
}

func TestDetectPlanMode(t *testing.T) {
	mk := func(args ...string) (*flag.FlagSet, error) {
		fs := flag.NewFlagSet("plan", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		_ = fs.String("f", "-", "")
		_ = fs.String("dest", "", "")
		_ = fs.String("amount", "", "")
		_ = fs.Bool("live", false, "")
		_ = fs.Bool("execute", false, "")
		_ = fs.String("agent", "", "")
		err := fs.Parse(args)
		return fs, err
	}

	fs, err := mk("-f", "plan.json")
	require.NoError(t, err)
	mode, err := liqcli.DetectPlanMode(fs)
	require.NoError(t, err)
	assert.Equal(t, liqcli.ModeJSON, mode)

	fs, err = mk("--dest", "26", "--amount", "1")
	require.NoError(t, err)
	mode, err = liqcli.DetectPlanMode(fs)
	require.NoError(t, err)
	assert.Equal(t, liqcli.ModeEasy, mode)

	fs, err = mk("--dest", "26", "-f", "x.json")
	require.NoError(t, err)
	_, err = liqcli.DetectPlanMode(fs)
	require.Error(t, err)

	// agent/execute alone do not force easy
	fs, err = mk("--agent", "0x"+strings.Repeat("11", 20), "--execute")
	require.NoError(t, err)
	mode, err = liqcli.DetectPlanMode(fs)
	require.NoError(t, err)
	assert.Equal(t, liqcli.ModeJSON, mode)

	// --live alone forces easy
	fs, err = mk("--live")
	require.NoError(t, err)
	mode, err = liqcli.DetectPlanMode(fs)
	require.NoError(t, err)
	assert.Equal(t, liqcli.ModeEasy, mode)
}

func TestResolveAgentIdentity(t *testing.T) {
	// Fixed test key (not used on-chain).
	const priv = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	// Anvil/Hardhat account #0
	const addr = "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"

	agent, key, err := liqcli.ResolveAgentIdentity("", priv, "", "")
	require.NoError(t, err)
	assert.True(t, strings.EqualFold(agent, addr))
	assert.Equal(t, priv, key)

	// flag key wins env
	agent, key, err = liqcli.ResolveAgentIdentity("", priv, "", "0x"+strings.Repeat("11", 32))
	require.NoError(t, err)
	assert.True(t, strings.EqualFold(agent, addr))
	assert.Equal(t, priv, key)

	// mismatch refuse
	_, _, err = liqcli.ResolveAgentIdentity("0x"+strings.Repeat("22", 20), priv, "", "")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), strings.TrimPrefix(priv, "0x"))

	// missing
	_, _, err = liqcli.ResolveAgentIdentity("", "", "", "")
	require.Error(t, err)
}

func TestMain_EasyPlanDry(t *testing.T) {
	agent := "0x" + strings.Repeat("a1", 20)
	var stdout, stderr bytes.Buffer
	// Empty stdin must not hang — easy mode does not read body.
	code := liqcli.Main([]string{
		"plan",
		"--agent", agent,
		"--dest", "arc-testnet",
		"--amount", "42",
		"--sources", "base-sepolia",
		"--balance", "base-sepolia=100",
		"--gateway-balance", "100",
	}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 0, code, "stderr=%s", stderr.String())
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &resp))
	assert.Nil(t, resp.Error)
	assert.True(t, resp.Plan.DryRun)
	assert.True(t, resp.Plan.InventoryUnverified)
	assert.False(t, resp.Plan.Executed)
	assert.NotEqual(t, "payment_funding", resp.Plan.Action)
	assert.NotContains(t, resp.Plan.Action, "payment")
	require.NotEmpty(t, resp.Plan.Steps)
	assert.Equal(t, "agent_self", resp.Plan.Steps[0].RecipientRole)
	// Phase B: gateway balance covers → withdraw only
	assert.Equal(t, "circle_gateway_withdraw", resp.Plan.Action)
	require.NotNil(t, resp.Plan.Required)
	assert.Equal(t, "42000000", resp.Plan.Required.AmountAtomic)
}

func TestMain_EasyPlanIncomplete_NoStdinHang(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// Missing amount → exit 2; empty stdin not consumed for hang.
	code := liqcli.Main([]string{
		"plan",
		"--agent", "0x" + strings.Repeat("11", 20),
		"--dest", "26",
	}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr.String(), "amount")
}

func TestMain_UsageListsDepositMove(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{"help"}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 0, code)
	assert.Contains(t, stderr.String(), "deposit")
	assert.Contains(t, stderr.String(), "move")
	assert.Contains(t, stderr.String(), "Phase A")
	assert.Contains(t, stderr.String(), "Phase B")
	assert.Contains(t, stderr.String(), "13–19m")
}

func TestMain_EasyDepositDry(t *testing.T) {
	agent := "0x" + strings.Repeat("d1", 20)
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{
		"deposit",
		"--agent", agent,
		"--source", "arc-testnet",
		"--amount", "1",
		"--balance", "arc-testnet=10",
	}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 0, code, "stderr=%s", stderr.String())
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &resp))
	assert.Nil(t, resp.Error)
	assert.True(t, resp.Plan.DryRun)
	assert.Equal(t, "circle_gateway_deposit", resp.Plan.Action)
	assert.Nil(t, resp.Plan.Required)
	require.NotEmpty(t, resp.Plan.Steps)
	assert.Equal(t, "agent_self", resp.Plan.Steps[0].RecipientRole)
	assert.Equal(t, "1000000", resp.Plan.Steps[0].AmountAtomic)
	// privacy: no agent address in stderr notes on success path either
	assert.NotContains(t, stderr.String(), agent)
}

func TestMain_EasyDepositUnderfund_Exit1(t *testing.T) {
	agent := "0x" + strings.Repeat("d2", 20)
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{
		"deposit",
		"--agent", agent,
		"--source", "arc-testnet",
		"--amount", "100",
		"--balance", "arc-testnet=1",
	}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 1, code)
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, liqerr.CodeInsufficientLiquidity, resp.Error.Code)
	assert.NotContains(t, stderr.String(), agent)
	assert.NotContains(t, stderr.String(), "1000000")
}

func TestMain_EasyDepositMissingSource_Exit2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{
		"deposit",
		"--agent", "0x" + strings.Repeat("11", 20),
		"--amount", "1",
		"--balance", "arc-testnet=10",
	}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr.String(), "source")
}

func TestMain_EasyDepositMultiDry(t *testing.T) {
	agent := "0x" + strings.Repeat("d3", 20)
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{
		"deposit",
		"--agent", agent,
		"--from", "base-sepolia=3",
		"--from", "arbitrum-sepolia=2",
		"--balance", "base-sepolia=3",
		"--balance", "arbitrum-sepolia=2",
	}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 0, code, "stderr=%s", stderr.String())
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &resp))
	assert.Nil(t, resp.Error)
	assert.True(t, resp.Plan.DryRun)
	assert.Equal(t, "circle_gateway_deposit", resp.Plan.Action)
	require.Len(t, resp.Plan.Steps, 2)
	// Sorted CAIP-2: arb 421614 then base 84532
	assert.Equal(t, "2000000", resp.Plan.Steps[0].AmountAtomic)
	assert.Equal(t, "3000000", resp.Plan.Steps[1].AmountAtomic)
	assert.NotContains(t, stderr.String(), agent)
}

func TestMain_EasyDepositMultiUnderfund_Exit1(t *testing.T) {
	agent := "0x" + strings.Repeat("d4", 20)
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{
		"deposit",
		"--agent", agent,
		"--from", "base-sepolia=3",
		"--from", "arbitrum-sepolia=2",
		"--balance", "base-sepolia=3",
		"--balance", "arbitrum-sepolia=1",
	}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 1, code)
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, liqerr.CodeInsufficientLiquidity, resp.Error.Code)
	assert.NotContains(t, stderr.String(), agent)
}

func TestMain_EasyDepositFromWithSource_Exit2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{
		"deposit",
		"--agent", "0x" + strings.Repeat("11", 20),
		"--from", "base-sepolia=3",
		"--source", "base-sepolia",
		"--amount", "1",
		"--balance", "base-sepolia=10",
	}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr.String(), "exclusive")
}

func TestMain_EasyDepositFromDuplicateChain_Merges(t *testing.T) {
	// --from base=1 --from base=2 → human USDC ×10^6 → one step 3000000 atomic.
	agent := "0x" + strings.Repeat("d5", 20)
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{
		"deposit",
		"--agent", agent,
		"--from", "base-sepolia=1",
		"--from", "base-sepolia=2",
		"--balance", "base-sepolia=3",
	}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 0, code, "stderr=%s", stderr.String())
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &resp))
	assert.Nil(t, resp.Error)
	assert.Equal(t, "circle_gateway_deposit", resp.Plan.Action)
	require.Len(t, resp.Plan.Steps, 1)
	assert.Equal(t, "eip155:84532", resp.Plan.Steps[0].FromChainCAIP2)
	assert.Equal(t, "3000000", resp.Plan.Steps[0].AmountAtomic)
	assert.NotContains(t, stderr.String(), agent)
}

func TestMain_DepositJSONMultiMode(t *testing.T) {
	body := `{
  "sources": [
    {"chain_caip2": "eip155:84532", "amount_atomic": "3000000"},
    {"chain_caip2": "eip155:421614", "amount_atomic": "2000000"}
  ],
  "inventory": {
    "agent_address": "0xAgentSelf000000000000000000000000000001",
    "balances": [
      {"chain_caip2": "eip155:84532", "asset": "0x036CbD53842c5426634e7929541eC2318f3dCF7e",
       "amount_atomic": "3000000", "location": "native"},
      {"chain_caip2": "eip155:421614", "asset": "0x75faf114eafb1BDbe2F0316DF893fd58CE46AA4d",
       "amount_atomic": "2000000", "location": "native"}
    ]
  }
}`
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{"deposit", "-f", "-"}, strings.NewReader(body), &stdout, &stderr)
	assert.Equal(t, 0, code, "stderr=%s", stderr.String())
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &resp))
	assert.Equal(t, "circle_gateway_deposit", resp.Plan.Action)
	require.Len(t, resp.Plan.Steps, 2)
}

func TestMain_EasyMoveDry(t *testing.T) {
	agent := "0x" + strings.Repeat("e1", 20)
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{
		"move",
		"--agent", agent,
		"--dest", "arc-testnet",
		"--amount", "1",
		"--gateway-balance", "10",
	}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 0, code, "stderr=%s", stderr.String())
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &resp))
	assert.Nil(t, resp.Error)
	assert.True(t, resp.Plan.DryRun)
	assert.Equal(t, "circle_gateway_withdraw", resp.Plan.Action)
	require.NotNil(t, resp.Plan.Required)
	assert.Equal(t, "self", resp.Plan.AmountSource)
	assert.Empty(t, resp.Plan.Required.PayTo)
	assert.NotContains(t, stderr.String(), agent)
}

func TestMain_EasyMoveInsufficient_Exit0(t *testing.T) {
	agent := "0x" + strings.Repeat("e2", 20)
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{
		"move",
		"--agent", agent,
		"--dest", "arc-testnet",
		"--amount", "100",
		"--balance", "arc-testnet=0.000001",
	}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 0, code, "stderr=%s", stderr.String())
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &resp))
	assert.Nil(t, resp.Error)
	assert.Equal(t, "insufficient", resp.Plan.Action)
	assert.NotContains(t, stderr.String(), agent)
}

func TestMain_EasyMoveMissingDest_Exit2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{
		"move",
		"--agent", "0x" + strings.Repeat("11", 20),
		"--amount", "1",
		"--gateway-balance", "10",
	}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr.String(), "dest")
}

func TestMain_DepositExclusiveWithFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{
		"deposit",
		"--source", "arc-testnet",
		"--amount", "1",
		"--agent", "0x" + strings.Repeat("11", 20),
		"-f", "examples/plan.json",
	}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr.String(), "exclusive")
}

func TestMain_DepositJSONMode(t *testing.T) {
	body := `{
  "source_chain_caip2": "eip155:5042002",
  "amount_atomic": "500",
  "inventory": {
    "agent_address": "0xAgentSelf000000000000000000000000000001",
    "balances": [
      {"chain_caip2": "eip155:5042002", "asset": "0x3600000000000000000000000000000000000000",
       "amount_atomic": "1000", "location": "native"}
    ]
  }
}`
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{"deposit", "-f", "-"}, strings.NewReader(body), &stdout, &stderr)
	assert.Equal(t, 0, code, "stderr=%s", stderr.String())
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &resp))
	assert.Equal(t, "circle_gateway_deposit", resp.Plan.Action)
}

func TestMain_EasyExclusiveWithFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{
		"plan",
		"--dest", "26",
		"--amount", "1",
		"--agent", "0x" + strings.Repeat("11", 20),
		"-f", "examples/plan.json",
	}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr.String(), "exclusive")
}

func TestMain_EasyLivePlusBalance_Refuse(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{
		"plan",
		"--agent", "0x" + strings.Repeat("11", 20),
		"--dest", "26",
		"--amount", "1",
		"--balance", "6=10",
		"--live",
	}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr.String(), "live")
}

func TestMain_EasyMainnetLive_Refuse(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{
		"plan",
		"--agent", "0x" + strings.Repeat("11", 20),
		"--dest", "6",
		"--amount", "1",
		"--mainnet",
		"--live",
	}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr.String(), "mainnet")
}

func TestMain_EasyAgentNoPayToFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{"help"}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 0, code)
	assert.NotContains(t, stderr.String(), "pay-to")
}

func TestMain_EasyLiveWithoutRPC_NoNetwork(t *testing.T) {
	t.Setenv("RPC_URL_BASE_SEPOLIA", "")
	t.Setenv("RPC_URL_ARBITRUM_SEPOLIA", "")
	t.Setenv("RPC_URL_ARC_TESTNET", "")
	t.Setenv("RPC_URLS_JSON", "")
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{
		"plan",
		"--agent", "0x" + strings.Repeat("11", 20),
		"--dest", "26",
		"--amount", "1",
		"--live",
	}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "RPC")
	// no network success path; stderr sanitized
	assert.NotContains(t, stderr.String(), "0x"+strings.Repeat("11", 20))
}

func TestMain_EasyLiveWithoutExecute_StillDry(t *testing.T) {
	// --live without --execute must not execute; without RPCs fails before plan.
	// Use asserted path to prove Live does not set Execute: dry stamps.
	// (live path needs RPC; here we only assert Execute is independent via BuildPlanRequestFromEasy)
	agent := "0x" + strings.Repeat("c3", 20)
	inv, err := liqcli.BuildAssertedInventory(agent, []string{"6=50"}, "100", true)
	require.NoError(t, err)
	req, err := liqcli.BuildPlanRequestFromEasy(liqcli.EasyPlanInput{
		EasyCommon: liqcli.EasyCommon{
			Dest:    "26",
			Testnet: true,
			Live:    true, // Live true but Execute false
			Execute: false,
		},
		Amount: "42",
	}, inv)
	require.NoError(t, err)
	assert.False(t, req.Execute)
}

func TestMain_EasyPrivateKeyNeverLogged(t *testing.T) {
	const priv = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	var stdout, stderr bytes.Buffer
	// Incomplete easy (missing dest) after key set — failure notes must not echo key.
	code := liqcli.Main([]string{
		"plan",
		"--private-key", priv,
		"--amount", "1",
	}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 2, code)
	out := stdout.String() + stderr.String()
	assert.NotContains(t, out, priv)
	assert.NotContains(t, out, strings.TrimPrefix(priv, "0x"))
}

func TestMain_ConsolidateEasy(t *testing.T) {
	agent := "0x" + strings.Repeat("e5", 20)
	var stdout, stderr bytes.Buffer
	code := liqcli.Main([]string{
		"consolidate",
		"--agent", agent,
		"--balance", "base-sepolia=10",
		"--balance", "arbitrum-sepolia=5",
	}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 0, code, "stderr=%s", stderr.String())
	var resp types.PlanResponse
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &resp))
	assert.Nil(t, resp.Error)
	assert.True(t, resp.Plan.DryRun)
	assert.Equal(t, "circle_gateway_consolidate", resp.Plan.Action)
	require.NotEmpty(t, resp.Plan.Steps)
	assert.Equal(t, "agent_self", resp.Plan.Steps[0].RecipientRole)
}

func TestBuildAssertedInventory(t *testing.T) {
	agent := "0x" + strings.Repeat("f6", 20)
	inv, err := liqcli.BuildAssertedInventory(agent, []string{"26=5", "6=10"}, "80", true)
	require.NoError(t, err)
	assert.Equal(t, agent, inv.AgentAddress)
	require.Len(t, inv.Balances, 3)
	assert.Equal(t, "5000000", inv.Balances[0].AmountAtomic)
	assert.Equal(t, "native", inv.Balances[0].Location)
	assert.Equal(t, "circle_gateway", inv.Balances[2].Location)
	assert.Equal(t, "USDC", inv.Balances[2].Asset)
	assert.Equal(t, "80000000", inv.Balances[2].AmountAtomic)
}