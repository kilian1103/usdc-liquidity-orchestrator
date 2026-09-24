package liquidity_test

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
)

const (
	arbSepCAIP2 = "eip155:421614"
	arbSepUSDC  = "0x75faf114eafb1BDbe2F0316DF893fd58CE46AA4d"
)

func paymentFundingHappy(t *testing.T) (liquidity.Required, liquidity.Inventory, []liquidity.FundingSource) {
	t.Helper()
	// scale=10: logical 400 USDC → real 40e6; sources 30e6 + 10e6
	req := liquidity.Required{
		Protocol:            "x402",
		ChainCAIP2:          baseSepCAIP2,
		Asset:               baseSepUSDC,
		AmountAtomic:        decimal.RequireFromString("40000000"),
		AmountSource:        liquidity.AmountSourceProbe,
		AmountLogicalAtomic: decimal.RequireFromString("400000000"),
		ScaleFactor:         10,
	}
	sources := []liquidity.FundingSource{
		{
			ChainCAIP2:          baseSepCAIP2,
			AmountAtomic:        decimal.RequireFromString("30000000"),
			AmountLogicalAtomic: decimal.RequireFromString("300000000"),
		},
		{
			ChainCAIP2:          arbSepCAIP2,
			AmountAtomic:        decimal.RequireFromString("10000000"),
			AmountLogicalAtomic: decimal.RequireFromString("100000000"),
		},
	}
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{
			{ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC, AmountAtomic: decimal.RequireFromString("30000000"), Location: liquidity.LocationNative},
			{ChainCAIP2: arbSepCAIP2, Asset: arbSepUSDC, AmountAtomic: decimal.RequireFromString("10000000"), Location: liquidity.LocationNative},
		},
	}
	return req, inv, sources
}

func TestPlanPaymentFunding_T1_MultiSourceDepositsOnly(t *testing.T) {
	req, inv, sources := paymentFundingHappy(t)
	p, err := liquidity.PlanPaymentFunding(req, inv, sources, nil)
	require.NoError(t, err)
	assert.Equal(t, liquidity.ActionCircleGatewayDeposit, p.Action)
	assert.Contains(t, p.Reason, "Phase A")
	// two deposits only (no withdraw)
	require.Len(t, p.Steps, 2)
	assert.Equal(t, liquidity.StepKindCircleGatewayDeposit, p.Steps[0].Kind)
	assert.Equal(t, liquidity.StepKindCircleGatewayDeposit, p.Steps[1].Kind)

	sumDep := decimal.Zero
	for _, s := range p.Steps {
		sumDep = sumDep.Add(s.AmountAtomic)
		assert.Equal(t, agentAddr, s.Recipient)
		assert.Equal(t, liquidity.RecipientRoleAgentSelf, s.RecipientRole)
		assert.NotEqual(t, liquidity.StepKindCircleGatewayWithdraw, s.Kind)
	}
	assert.True(t, sumDep.Equal(req.AmountAtomic))
}

func TestPlanPaymentFunding_T5_EmptyPayTo_OK(t *testing.T) {
	req, inv, sources := paymentFundingHappy(t)
	req.PayTo = ""
	p, err := liquidity.PlanPaymentFunding(req, inv, sources, nil)
	require.NoError(t, err)
	assert.Equal(t, liquidity.ActionCircleGatewayDeposit, p.Action)
}

func TestPlanPaymentFunding_T6_EmptyAgent(t *testing.T) {
	req, inv, sources := paymentFundingHappy(t)
	inv.AgentAddress = ""
	_, err := liquidity.PlanPaymentFunding(req, inv, sources, nil)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
}

func TestPlanPaymentFunding_T7_AgentEqualsPayTo(t *testing.T) {
	req, inv, sources := paymentFundingHappy(t)
	req.PayTo = merchantPayTo
	inv.AgentAddress = merchantPayTo
	_, err := liquidity.PlanPaymentFunding(req, inv, sources, nil)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
}

func TestPlanPaymentFunding_T8_DestChainSourceOK(t *testing.T) {
	req := liquidity.Required{
		Protocol:     "x402",
		ChainCAIP2:   baseSepCAIP2,
		Asset:        baseSepUSDC,
		PayTo:        merchantPayTo,
		AmountAtomic: decimal.RequireFromString("5000000"),
		AmountSource: liquidity.AmountSourceProbe,
		ScaleFactor:  1,
	}
	sources := []liquidity.FundingSource{{
		ChainCAIP2:   baseSepCAIP2,
		AmountAtomic: decimal.RequireFromString("5000000"),
	}}
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC,
			AmountAtomic: decimal.RequireFromString("5000000"), Location: liquidity.LocationNative,
		}},
	}
	p, err := liquidity.PlanPaymentFunding(req, inv, sources, nil)
	require.NoError(t, err)
	require.Len(t, p.Steps, 1)
	assert.Equal(t, liquidity.StepKindCircleGatewayDeposit, p.Steps[0].Kind)
	assert.Equal(t, baseSepCAIP2, p.Steps[0].FromChainCAIP2)
}

func TestPlanPaymentFunding_T9_AgentSelfPrepareCalls(t *testing.T) {
	req, inv, sources := paymentFundingHappy(t)
	p, err := liquidity.PlanPaymentFunding(req, inv, sources, nil)
	require.NoError(t, err)
	for _, s := range p.Steps {
		if s.Kind != liquidity.StepKindCircleGatewayDeposit {
			continue
		}
		require.Len(t, s.PrepareCalls, 2)
		assert.Equal(t, "approve", s.PrepareCalls[0].Method)
		assert.Equal(t, "deposit", s.PrepareCalls[1].Method)
		assert.Equal(t, agentAddr, s.Recipient)
		assert.Equal(t, liquidity.RecipientRoleAgentSelf, s.RecipientRole)
		assert.NotEqual(t, merchantPayTo, s.Recipient)
	}
}

func TestPlanPaymentFunding_T10_DryStamps(t *testing.T) {
	req, inv, sources := paymentFundingHappy(t)
	p, err := liquidity.PlanPaymentFunding(req, inv, sources, nil)
	require.NoError(t, err)
	assert.True(t, p.DryRun)
	assert.False(t, p.Executed)
	assert.True(t, p.InventoryAsserted)
	assert.True(t, p.InventoryUnverified)
}

func TestPlanPaymentFunding_T13_PlanToWire_DualAmounts(t *testing.T) {
	req, inv, sources := paymentFundingHappy(t)
	p, err := liquidity.PlanPaymentFunding(req, inv, sources, nil)
	require.NoError(t, err)
	w := liquidity.PlanToWire(p)
	require.NotNil(t, w.Required)
	assert.Equal(t, "40000000", w.Required.AmountAtomic)
	assert.Equal(t, "400000000", w.Required.AmountLogicalAtomic)
	assert.Equal(t, int64(10), w.Required.ScaleFactor)
	assert.True(t, w.DryRun)
	assert.False(t, w.Executed)

	var sawDeposit bool
	for _, s := range w.Steps {
		assert.NotEmpty(t, s.AmountAtomic)
		assert.NotEqual(t, liquidity.StepKindCircleGatewayWithdraw, s.Kind)
		if s.Kind == liquidity.StepKindCircleGatewayDeposit {
			sawDeposit = true
			assert.NotEmpty(t, s.AmountLogicalAtomic)
			assert.Equal(t, int64(10), s.ScaleFactor)
			require.NotEmpty(t, s.PrepareCalls)
		}
	}
	assert.True(t, sawDeposit)
}

func TestPlanPaymentFunding_SumMismatch_InvalidQuery(t *testing.T) {
	req, inv, sources := paymentFundingHappy(t)
	sources[1].AmountAtomic = decimal.RequireFromString("5000000") // break sum
	_, err := liquidity.PlanPaymentFunding(req, inv, sources, nil)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
}

func TestPlanPaymentFunding_T14_ShortfallPathStillWorks(t *testing.T) {
	// Smoke: PlanOrchestration Phase B shortfall alongside payment funding Phase A.
	req := baseRequired()
	req.AmountAtomic = decimal.RequireFromString("42000000")
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{
			{ChainCAIP2: arbCAIP2, Asset: arbUSDC, AmountAtomic: decimal.RequireFromString("30000000"), Location: liquidity.LocationNative},
			{ChainCAIP2: baseCAIP2, Asset: baseUSDC, AmountAtomic: decimal.RequireFromString("20000000"), Location: liquidity.LocationNative},
		},
	}
	p, err := liquidity.PlanLiquidity(req, inv, nil)
	require.NoError(t, err)
	assert.Equal(t, liquidity.ActionCCTPFast, p.Action)
	require.Len(t, p.Steps, 2)
	assert.True(t, p.Steps[0].AmountAtomic.Equal(decimal.RequireFromString("22000000")),
		"shortfall-only must still move 22 USDC not full 42")
}