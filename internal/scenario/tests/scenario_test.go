package scenario_test

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/scenario"
	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
)

const (
	agentAddr     = "0xAgentSelf000000000000000000000000000001"
	merchantPayTo = "0xMerchantPayTo000000000000000000000001"
)

func clearScenarioEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		scenario.EnvScaleFactor,
		scenario.EnvPaymentProtocol,
		scenario.EnvPaymentChain,
		scenario.EnvPaymentAmountUSDC,
		scenario.EnvSourceAmountBaseSepolia,
		scenario.EnvSourceAmountArbSepolia,
		scenario.EnvSourceAmountArcTestnet,
		scenario.EnvSourceMode,
		scenario.EnvAgentAddress,
		scenario.EnvAgentPrivateKey,
	} {
		t.Setenv(k, "")
	}
}

func setHappyScenario(t *testing.T) {
	t.Helper()
	clearScenarioEnv(t)
	t.Setenv(scenario.EnvScaleFactor, "10")
	t.Setenv(scenario.EnvPaymentProtocol, "x402")
	t.Setenv(scenario.EnvPaymentChain, "eip155:84532")
	t.Setenv(scenario.EnvPaymentAmountUSDC, "400")
	t.Setenv(scenario.EnvSourceAmountBaseSepolia, "300")
	t.Setenv(scenario.EnvSourceAmountArbSepolia, "100")
	t.Setenv(scenario.EnvSourceAmountArcTestnet, "0")
	t.Setenv(scenario.EnvAgentAddress, agentAddr)
}

func TestLoadFromEnv_T1_HappyScale10(t *testing.T) {
	setHappyScenario(t)
	s, err := scenario.LoadFromEnv()
	require.NoError(t, err)
	assert.Equal(t, int64(10), s.ScaleFactor)
	assert.Equal(t, "eip155:84532", s.PaymentChainCAIP2)
	assert.True(t, s.PaymentRealAtomic.Equal(decimal.RequireFromString("40000000")))
	require.Len(t, s.Sources, 2)
	sum := decimal.Zero
	for _, src := range s.Sources {
		sum = sum.Add(src.RealAtomic)
	}
	assert.True(t, sum.Equal(s.PaymentRealAtomic))

	inv := s.BuildAssertedInventory()
	assert.Equal(t, agentAddr, inv.AgentAddress)
	require.Len(t, inv.Balances, 2)
	req := s.BuildRequired()
	assert.True(t, req.AmountAtomic.Equal(s.PaymentRealAtomic))
	assert.True(t, req.AmountLogicalAtomic.Equal(s.PaymentLogicalAtomic))
	assert.Equal(t, int64(10), req.ScaleFactor)
}

func TestLoadFromEnv_T2_FloorDesync_InvalidQuery(t *testing.T) {
	clearScenarioEnv(t)
	t.Setenv(scenario.EnvScaleFactor, "3")
	t.Setenv(scenario.EnvPaymentChain, "eip155:84532")
	t.Setenv(scenario.EnvPaymentAmountUSDC, "4")
	t.Setenv(scenario.EnvSourceAmountBaseSepolia, "2")
	t.Setenv(scenario.EnvSourceAmountArbSepolia, "2")
	t.Setenv(scenario.EnvAgentAddress, agentAddr)

	_, err := scenario.LoadFromEnv()
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
	assert.Contains(t, err.Error(), "payment_real")
}

func TestLoadFromEnv_T3_ScaleLEZero(t *testing.T) {
	setHappyScenario(t)
	t.Setenv(scenario.EnvScaleFactor, "0")
	_, err := scenario.LoadFromEnv()
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))

	t.Setenv(scenario.EnvScaleFactor, "-1")
	_, err = scenario.LoadFromEnv()
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
}

func TestLoadFromEnv_T4_SumMismatch(t *testing.T) {
	setHappyScenario(t)
	t.Setenv(scenario.EnvSourceAmountArbSepolia, "50") // 300+50 != 400
	_, err := scenario.LoadFromEnv()
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
	assert.Contains(t, err.Error(), "logical")
}

func TestLoadFromEnv_T5_NoPayToRequired(t *testing.T) {
	setHappyScenario(t)
	s, err := scenario.LoadFromEnv()
	require.NoError(t, err)
	assert.Empty(t, s.BuildRequired().PayTo)
}

func TestLoadFromEnv_T6_EmptyAgent(t *testing.T) {
	setHappyScenario(t)
	t.Setenv(scenario.EnvAgentAddress, "")
	t.Setenv(scenario.EnvAgentPrivateKey, "")
	_, err := scenario.LoadFromEnv()
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
	assert.Contains(t, err.Error(), "agent")
}

func TestLoadFromEnv_T8_DestChainSourceOK(t *testing.T) {
	// All sources on payment chain (Base Sepolia) — allowed for full-funding.
	clearScenarioEnv(t)
	t.Setenv(scenario.EnvScaleFactor, "1")
	t.Setenv(scenario.EnvPaymentChain, "eip155:84532")
	t.Setenv(scenario.EnvPaymentAmountUSDC, "50")
	t.Setenv(scenario.EnvSourceAmountBaseSepolia, "50")
	t.Setenv(scenario.EnvAgentAddress, agentAddr)

	s, err := scenario.LoadFromEnv()
	require.NoError(t, err)
	require.Len(t, s.Sources, 1)
	assert.Equal(t, "eip155:84532", s.Sources[0].ChainCAIP2)
	assert.Equal(t, s.PaymentChainCAIP2, s.Sources[0].ChainCAIP2)

	inv := s.BuildAssertedInventory()
	req := s.BuildRequired()
	p, err := liquidity.PlanPaymentFunding(req, inv, s.FundingSources(), nil)
	require.NoError(t, err)
	assert.Equal(t, liquidity.ActionCircleGatewayDeposit, p.Action)
	// Phase A: deposit only
	require.Len(t, p.Steps, 1)
	assert.Equal(t, liquidity.StepKindCircleGatewayDeposit, p.Steps[0].Kind)
	assert.Equal(t, "eip155:84532", p.Steps[0].FromChainCAIP2)
}

func TestLoadFromEnv_T11_SourceModeAuto_NotImplemented(t *testing.T) {
	setHappyScenario(t)
	t.Setenv(scenario.EnvSourceMode, "auto")
	_, err := scenario.LoadFromEnv()
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
	assert.Contains(t, err.Error(), "not implemented")
}

func TestLoadFromEnv_AgentKeyMismatch_Refuse(t *testing.T) {
	setHappyScenario(t)
	// Ephemeral key via crypto.GenerateKey — never a fixed fixture key.
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	t.Setenv(scenario.EnvAgentPrivateKey, "0x"+common.Bytes2Hex(crypto.FromECDSA(key)))
	_, err = scenario.LoadFromEnv()
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
	assert.Contains(t, err.Error(), "does not match")
}