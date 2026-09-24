package liquidity_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/types"
)

func TestPlanGatewayDeposit_HappyExactN(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: arcCAIP2, Asset: arcUSDC,
			AmountAtomic: decimal.RequireFromString("5000"), Location: liquidity.LocationNative,
		}},
	}
	p, err := liquidity.PlanGatewayDeposit(inv, arcCAIP2, decimal.RequireFromString("1000"), nil, nil)
	require.NoError(t, err)
	assert.Equal(t, liquidity.ActionCircleGatewayDeposit, p.Action)
	assert.True(t, p.DryRun)
	assert.False(t, p.Executed)
	assert.Empty(t, p.Required.PayTo)
	assert.Nil(t, p.Fee)
	require.Len(t, p.Steps, 1)
	s := p.Steps[0]
	assert.Equal(t, liquidity.StepKindCircleGatewayDeposit, s.Kind)
	assert.Equal(t, arcCAIP2, s.FromChainCAIP2)
	assert.True(t, s.AmountAtomic.Equal(decimal.RequireFromString("1000")))
	assert.Equal(t, agentAddr, s.Recipient)
	assert.Equal(t, liquidity.RecipientRoleAgentSelf, s.RecipientRole)
	require.Len(t, s.PrepareCalls, 2)
}

func TestPlanGatewayDeposit_NativeEqualsN_OK(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC,
			AmountAtomic: decimal.RequireFromString("1000"), Location: liquidity.LocationNative,
		}},
	}
	p, err := liquidity.PlanGatewayDeposit(inv, baseSepCAIP2, decimal.RequireFromString("1000"), nil, nil)
	require.NoError(t, err)
	require.Len(t, p.Steps, 1)
	assert.True(t, p.Steps[0].AmountAtomic.Equal(decimal.RequireFromString("1000")))
}

func TestPlanGatewayDeposit_Underfunded_HardError(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: arcCAIP2, Asset: arcUSDC,
			AmountAtomic: decimal.RequireFromString("100"), Location: liquidity.LocationNative,
		}},
	}
	_, err := liquidity.PlanGatewayDeposit(inv, arcCAIP2, decimal.RequireFromString("1000"), nil, nil)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInsufficientLiquidity, liqerr.CodeOf(err))
}

func TestPlanGatewayDeposit_MissingSourceAgentAmount(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: arcCAIP2, Asset: arcUSDC,
			AmountAtomic: decimal.RequireFromString("1000"), Location: liquidity.LocationNative,
		}},
	}
	_, err := liquidity.PlanGatewayDeposit(inv, "", decimal.RequireFromString("1000"), nil, nil)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))

	_, err = liquidity.PlanGatewayDeposit(liquidity.Inventory{}, arcCAIP2, decimal.RequireFromString("1000"), nil, nil)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))

	_, err = liquidity.PlanGatewayDeposit(inv, arcCAIP2, decimal.Zero, nil, nil)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
}

func TestPlanGatewayDeposit_NonGatewayOK_AndAllowFalse(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: "eip155:999999", Asset: "USDC",
			AmountAtomic: decimal.RequireFromString("1000"), Location: liquidity.LocationNative,
		}},
	}
	_, err := liquidity.PlanGatewayDeposit(inv, "eip155:999999", decimal.RequireFromString("1000"), nil, nil)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))

	f := false
	orch := &liquidity.Orchestration{AllowCircleGateway: &f}
	inv2 := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: arcCAIP2, Asset: arcUSDC,
			AmountAtomic: decimal.RequireFromString("1000"), Location: liquidity.LocationNative,
		}},
	}
	_, err = liquidity.PlanGatewayDeposit(inv2, arcCAIP2, decimal.RequireFromString("1000"), orch, nil)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
}

func TestPlanGatewayDeposit_SourceAllowlist(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{
			{ChainCAIP2: arcCAIP2, Asset: arcUSDC, AmountAtomic: decimal.RequireFromString("1000"), Location: liquidity.LocationNative},
			{ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC, AmountAtomic: decimal.RequireFromString("9000"), Location: liquidity.LocationNative},
		},
	}
	orch := &liquidity.Orchestration{SourceChainCAIP2s: []string{baseSepCAIP2}}
	_, err := liquidity.PlanGatewayDeposit(inv, arcCAIP2, decimal.RequireFromString("1000"), orch, nil)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))

	p, err := liquidity.PlanGatewayDeposit(inv, baseSepCAIP2, decimal.RequireFromString("1000"), orch, nil)
	require.NoError(t, err)
	assert.Equal(t, baseSepCAIP2, p.Steps[0].FromChainCAIP2)
	// Exact N, not full multi-row balance on other chain.
	assert.True(t, p.Steps[0].AmountAtomic.Equal(decimal.RequireFromString("1000")))
}

func TestPlanGatewayDeposit_MaxAmountAtomic(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: arcCAIP2, Asset: arcUSDC,
			AmountAtomic: decimal.RequireFromString("5000"), Location: liquidity.LocationNative,
		}},
	}
	g := &liquidity.Guard{MaxAmountAtomic: decimal.RequireFromString("100")}
	_, err := liquidity.PlanGatewayDeposit(inv, arcCAIP2, decimal.RequireFromString("1000"), nil, g)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInsufficientLiquidity, liqerr.CodeOf(err))
}

func TestPlanGatewayDeposit_PlanToWire_OmitsRequired(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: arcCAIP2, Asset: arcUSDC,
			AmountAtomic: decimal.RequireFromString("1000"), Location: liquidity.LocationNative,
		}},
	}
	p, err := liquidity.PlanGatewayDeposit(inv, arcCAIP2, decimal.RequireFromString("500"), nil, nil)
	require.NoError(t, err)
	w := liquidity.PlanToWire(p)
	assert.Nil(t, w.Required)
	assert.Empty(t, w.AmountSource)
	assert.Equal(t, string(liquidity.ActionCircleGatewayDeposit), w.Action)
}

func TestPlanGatewayDeposits_MultiHappy(t *testing.T) {
	const arbSepCAIP2 = "eip155:421614"
	const arbSepUSDC = "0x75faf114eafb1BDbe2F0316DF893fd58CE46AA4d"
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{
			{ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC, AmountAtomic: decimal.RequireFromString("3000000"), Location: liquidity.LocationNative},
			{ChainCAIP2: arbSepCAIP2, Asset: arbSepUSDC, AmountAtomic: decimal.RequireFromString("2000000"), Location: liquidity.LocationNative},
		},
	}
	sources := []liquidity.FundingSource{
		{ChainCAIP2: baseSepCAIP2, AmountAtomic: decimal.RequireFromString("3000000")},
		{ChainCAIP2: arbSepCAIP2, AmountAtomic: decimal.RequireFromString("2000000")},
	}
	p, err := liquidity.PlanGatewayDeposits(inv, sources, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, liquidity.ActionCircleGatewayDeposit, p.Action)
	assert.True(t, p.DryRun)
	assert.False(t, p.Executed)
	require.Len(t, p.Steps, 2)
	// Sorted by CAIP-2 ascending: 421614 < 84532
	assert.Equal(t, arbSepCAIP2, p.Steps[0].FromChainCAIP2)
	assert.True(t, p.Steps[0].AmountAtomic.Equal(decimal.RequireFromString("2000000")))
	assert.Equal(t, baseSepCAIP2, p.Steps[1].FromChainCAIP2)
	assert.True(t, p.Steps[1].AmountAtomic.Equal(decimal.RequireFromString("3000000")))
	for _, s := range p.Steps {
		assert.Equal(t, liquidity.StepKindCircleGatewayDeposit, s.Kind)
		assert.Equal(t, agentAddr, s.Recipient)
		assert.Equal(t, liquidity.RecipientRoleAgentSelf, s.RecipientRole)
		require.Len(t, s.PrepareCalls, 2)
	}
	w := liquidity.PlanToWire(p)
	assert.Nil(t, w.Required)
	assert.Equal(t, string(liquidity.ActionCircleGatewayDeposit), w.Action)
	require.Len(t, w.Steps, 2)
}

func TestPlanGatewayDeposits_UnderfundOneSource(t *testing.T) {
	const arbSepCAIP2 = "eip155:421614"
	const arbSepUSDC = "0x75faf114eafb1BDbe2F0316DF893fd58CE46AA4d"
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{
			{ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC, AmountAtomic: decimal.RequireFromString("3000000"), Location: liquidity.LocationNative},
			{ChainCAIP2: arbSepCAIP2, Asset: arbSepUSDC, AmountAtomic: decimal.RequireFromString("100"), Location: liquidity.LocationNative},
		},
	}
	sources := []liquidity.FundingSource{
		{ChainCAIP2: baseSepCAIP2, AmountAtomic: decimal.RequireFromString("3000000")},
		{ChainCAIP2: arbSepCAIP2, AmountAtomic: decimal.RequireFromString("2000000")},
	}
	_, err := liquidity.PlanGatewayDeposits(inv, sources, nil, nil)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInsufficientLiquidity, liqerr.CodeOf(err))
}

func TestPlanGatewayDeposits_EmptySources(t *testing.T) {
	inv := liquidity.Inventory{AgentAddress: agentAddr}
	_, err := liquidity.PlanGatewayDeposits(inv, nil, nil, nil)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
}

func TestPlanGatewayDeposits_Allowlist(t *testing.T) {
	const arbSepCAIP2 = "eip155:421614"
	const arbSepUSDC = "0x75faf114eafb1BDbe2F0316DF893fd58CE46AA4d"
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{
			{ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC, AmountAtomic: decimal.RequireFromString("3000000"), Location: liquidity.LocationNative},
			{ChainCAIP2: arbSepCAIP2, Asset: arbSepUSDC, AmountAtomic: decimal.RequireFromString("2000000"), Location: liquidity.LocationNative},
		},
	}
	orch := &liquidity.Orchestration{SourceChainCAIP2s: []string{baseSepCAIP2}}
	_, err := liquidity.PlanGatewayDeposits(inv, []liquidity.FundingSource{
		{ChainCAIP2: baseSepCAIP2, AmountAtomic: decimal.RequireFromString("1000000")},
		{ChainCAIP2: arbSepCAIP2, AmountAtomic: decimal.RequireFromString("1000000")},
	}, orch, nil)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))

	p, err := liquidity.PlanGatewayDeposits(inv, []liquidity.FundingSource{
		{ChainCAIP2: baseSepCAIP2, AmountAtomic: decimal.RequireFromString("1000000")},
	}, orch, nil)
	require.NoError(t, err)
	require.Len(t, p.Steps, 1)
	assert.Equal(t, baseSepCAIP2, p.Steps[0].FromChainCAIP2)
}

func TestPlanGatewayDeposits_AllowGatewayFalse(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC,
			AmountAtomic: decimal.RequireFromString("1000000"), Location: liquidity.LocationNative,
		}},
	}
	f := false
	orch := &liquidity.Orchestration{AllowCircleGateway: &f}
	_, err := liquidity.PlanGatewayDeposits(inv, []liquidity.FundingSource{
		{ChainCAIP2: baseSepCAIP2, AmountAtomic: decimal.RequireFromString("1000000")},
	}, orch, nil)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
}

func TestPlanGatewayDeposits_MergeDuplicateChains(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC,
			AmountAtomic: decimal.RequireFromString("5000000"), Location: liquidity.LocationNative,
		}},
	}
	sources := []liquidity.FundingSource{
		{ChainCAIP2: baseSepCAIP2, AmountAtomic: decimal.RequireFromString("1000000")},
		{ChainCAIP2: baseSepCAIP2, AmountAtomic: decimal.RequireFromString("2000000")},
	}
	p, err := liquidity.PlanGatewayDeposits(inv, sources, nil, nil)
	require.NoError(t, err)
	require.Len(t, p.Steps, 1)
	assert.Equal(t, baseSepCAIP2, p.Steps[0].FromChainCAIP2)
	assert.True(t, p.Steps[0].AmountAtomic.Equal(decimal.RequireFromString("3000000")))
	assert.Equal(t, agentAddr, p.Steps[0].Recipient)
	assert.Equal(t, liquidity.RecipientRoleAgentSelf, p.Steps[0].RecipientRole)
}

func TestPlanGatewayDeposits_MergeThenUnderfund(t *testing.T) {
	// Each row alone fits native; merged sum does not → insufficient_liquidity.
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC,
			AmountAtomic: decimal.RequireFromString("2500000"), Location: liquidity.LocationNative,
		}},
	}
	sources := []liquidity.FundingSource{
		{ChainCAIP2: baseSepCAIP2, AmountAtomic: decimal.RequireFromString("1500000")},
		{ChainCAIP2: baseSepCAIP2, AmountAtomic: decimal.RequireFromString("1500000")},
	}
	_, err := liquidity.PlanGatewayDeposits(inv, sources, nil, nil)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInsufficientLiquidity, liqerr.CodeOf(err))
}

func TestPlanGatewayDeposits_MaxAmountAtomic_PerStep(t *testing.T) {
	const arbSepCAIP2 = "eip155:421614"
	const arbSepUSDC = "0x75faf114eafb1BDbe2F0316DF893fd58CE46AA4d"
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{
			{ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC, AmountAtomic: decimal.RequireFromString("3000000"), Location: liquidity.LocationNative},
			{ChainCAIP2: arbSepCAIP2, Asset: arbSepUSDC, AmountAtomic: decimal.RequireFromString("2000000"), Location: liquidity.LocationNative},
		},
	}
	// Cap is per-step: plan total 5e6 would exceed, but each step alone is under the cap when capped high enough.
	gOK := &liquidity.Guard{MaxAmountAtomic: decimal.RequireFromString("3000000")}
	p, err := liquidity.PlanGatewayDeposits(inv, []liquidity.FundingSource{
		{ChainCAIP2: baseSepCAIP2, AmountAtomic: decimal.RequireFromString("3000000")},
		{ChainCAIP2: arbSepCAIP2, AmountAtomic: decimal.RequireFromString("2000000")},
	}, nil, gOK)
	require.NoError(t, err)
	require.Len(t, p.Steps, 2)

	// One step above MaxAmountAtomic fails even if other steps are small.
	gFail := &liquidity.Guard{MaxAmountAtomic: decimal.RequireFromString("2500000")}
	_, err = liquidity.PlanGatewayDeposits(inv, []liquidity.FundingSource{
		{ChainCAIP2: baseSepCAIP2, AmountAtomic: decimal.RequireFromString("3000000")},
		{ChainCAIP2: arbSepCAIP2, AmountAtomic: decimal.RequireFromString("2000000")},
	}, nil, gFail)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInsufficientLiquidity, liqerr.CodeOf(err))
}

func TestUnconfiguredExecutor_Deposit_RailUnavailable(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: arcCAIP2, Asset: arcUSDC,
			AmountAtomic: decimal.RequireFromString("1000"), Location: liquidity.LocationNative,
		}},
	}
	p, err := liquidity.PlanGatewayDeposit(inv, arcCAIP2, decimal.RequireFromString("500"), nil, nil)
	require.NoError(t, err)
	_, err = liquidity.UnconfiguredExecutor{}.Execute(context.Background(), p)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeLiquidityRailUnavailable, liqerr.CodeOf(err))
}

func TestUnconfiguredExecutor_MultiDeposit_RailUnavailable(t *testing.T) {
	const arbSepCAIP2 = "eip155:421614"
	const arbSepUSDC = "0x75faf114eafb1BDbe2F0316DF893fd58CE46AA4d"
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{
			{ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC, AmountAtomic: decimal.RequireFromString("3000000"), Location: liquidity.LocationNative},
			{ChainCAIP2: arbSepCAIP2, Asset: arbSepUSDC, AmountAtomic: decimal.RequireFromString("2000000"), Location: liquidity.LocationNative},
		},
	}
	p, err := liquidity.PlanGatewayDeposits(inv, []liquidity.FundingSource{
		{ChainCAIP2: baseSepCAIP2, AmountAtomic: decimal.RequireFromString("3000000")},
		{ChainCAIP2: arbSepCAIP2, AmountAtomic: decimal.RequireFromString("2000000")},
	}, nil, nil)
	require.NoError(t, err)
	require.Len(t, p.Steps, 2)
	_, err = liquidity.UnconfiguredExecutor{}.Execute(context.Background(), p)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeLiquidityRailUnavailable, liqerr.CodeOf(err))
}

func TestTypes_LandRequired_OmitsPayToKeys(t *testing.T) {
	r := types.Required{
		ChainCAIP2:   arcCAIP2,
		Asset:        arcUSDC,
		AmountAtomic: "1000",
		Source:       "self",
	}
	b, err := json.Marshal(r)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "pay_to")
	assert.Contains(t, string(b), "amount_atomic")
}