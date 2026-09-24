package liquidity_test

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
)

func TestPlanConsolidate_MultiChain_FullBalance_CAIPAscending(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{
			// deliberate non-ascending input order
			{ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC, AmountAtomic: decimal.RequireFromString("3000000"), Location: liquidity.LocationNative},
			{ChainCAIP2: arcCAIP2, Asset: arcUSDC, AmountAtomic: decimal.RequireFromString("1000000"), Location: liquidity.LocationNative},
			{ChainCAIP2: "eip155:421614", Asset: "0x75faf114eafb1BDbe2F0316DF893fd58CE46AA4d", AmountAtomic: decimal.RequireFromString("2000000"), Location: liquidity.LocationNative},
		},
	}
	p, err := liquidity.PlanConsolidate(inv, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, liquidity.ActionCircleGatewayConsolidate, p.Action)
	assert.True(t, p.DryRun)
	assert.False(t, p.Executed)
	assert.True(t, p.InventoryAsserted)
	assert.True(t, p.InventoryUnverified)
	assert.Empty(t, p.Required.PayTo)
	require.Len(t, p.Steps, 3)
	// CAIP-2 ascending: 421614 < 5042002 < 84532
	assert.Equal(t, "eip155:421614", p.Steps[0].FromChainCAIP2)
	assert.Equal(t, arcCAIP2, p.Steps[1].FromChainCAIP2)
	assert.Equal(t, baseSepCAIP2, p.Steps[2].FromChainCAIP2)
	assert.True(t, p.Steps[0].AmountAtomic.Equal(decimal.RequireFromString("2000000")))
	assert.True(t, p.Steps[1].AmountAtomic.Equal(decimal.RequireFromString("1000000")))
	assert.True(t, p.Steps[2].AmountAtomic.Equal(decimal.RequireFromString("3000000")))
	for _, s := range p.Steps {
		assert.Equal(t, liquidity.StepKindCircleGatewayDeposit, s.Kind)
		assert.Equal(t, agentAddr, s.Recipient)
		assert.Equal(t, liquidity.RecipientRoleAgentSelf, s.RecipientRole)
		require.Len(t, s.PrepareCalls, 2)
	}
}

func TestPlanConsolidate_SourceAllowlist(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{
			{ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC, AmountAtomic: decimal.RequireFromString("3000000"), Location: liquidity.LocationNative},
			{ChainCAIP2: arcCAIP2, Asset: arcUSDC, AmountAtomic: decimal.RequireFromString("1000000"), Location: liquidity.LocationNative},
		},
	}
	orch := &liquidity.Orchestration{SourceChainCAIP2s: []string{arcCAIP2}}
	p, err := liquidity.PlanConsolidate(inv, orch, nil)
	require.NoError(t, err)
	require.Len(t, p.Steps, 1)
	assert.Equal(t, arcCAIP2, p.Steps[0].FromChainCAIP2)
	assert.True(t, p.Steps[0].AmountAtomic.Equal(decimal.RequireFromString("1000000")))
}

func TestPlanConsolidate_SkipNonGateway_AndGatewayLocation(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{
			// already in gateway — not consolidatable
			{Asset: "USDC", AmountAtomic: decimal.RequireFromString("9000000"), Location: liquidity.LocationCircleGateway},
			// unknown EVM
			{ChainCAIP2: "eip155:999999", Asset: "USDC", AmountAtomic: decimal.RequireFromString("1000"), Location: liquidity.LocationNative},
			// eligible
			{ChainCAIP2: arcCAIP2, Asset: arcUSDC, AmountAtomic: decimal.RequireFromString("500"), Location: liquidity.LocationNative},
		},
	}
	p, err := liquidity.PlanConsolidate(inv, nil, nil)
	require.NoError(t, err)
	require.Len(t, p.Steps, 1)
	assert.Equal(t, arcCAIP2, p.Steps[0].FromChainCAIP2)
}

func TestPlanConsolidate_SumMultiRowSameChain(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{
			{ChainCAIP2: arcCAIP2, Asset: arcUSDC, AmountAtomic: decimal.RequireFromString("100"), Location: liquidity.LocationNative},
			{ChainCAIP2: arcCAIP2, Asset: "USDC", AmountAtomic: decimal.RequireFromString("50"), Location: liquidity.LocationNative},
		},
	}
	p, err := liquidity.PlanConsolidate(inv, nil, nil)
	require.NoError(t, err)
	require.Len(t, p.Steps, 1)
	assert.True(t, p.Steps[0].AmountAtomic.Equal(decimal.RequireFromString("150")))
}

func TestPlanConsolidate_NoopWhenNothingEligible(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			Asset: "USDC", AmountAtomic: decimal.RequireFromString("9000000"), Location: liquidity.LocationCircleGateway,
		}},
	}
	p, err := liquidity.PlanConsolidate(inv, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, liquidity.ActionNoop, p.Action)
	assert.Empty(t, p.Steps)
	assert.True(t, p.DryRun)
}

func TestPlanConsolidate_EmptyAgent_InvalidQuery(t *testing.T) {
	_, err := liquidity.PlanConsolidate(liquidity.Inventory{}, nil, nil)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
}

func TestPlanConsolidate_AllowCircleGatewayFalse(t *testing.T) {
	f := false
	orch := &liquidity.Orchestration{AllowCircleGateway: &f}
	_, err := liquidity.PlanConsolidate(liquidity.Inventory{AgentAddress: agentAddr}, orch, nil)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
}

func TestGuard_CheckAgent_Allowlist(t *testing.T) {
	g := &liquidity.Guard{AllowedAgentAddresses: []string{agentAddr}}
	require.NoError(t, g.CheckAgent(liquidity.Inventory{AgentAddress: agentAddr}))
	err := g.CheckAgent(liquidity.Inventory{AgentAddress: platformMoR})
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
}

func TestPlanConsolidate_PlanToWire_OmitsRequiredAndAmountSource(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: arcCAIP2, Asset: arcUSDC, AmountAtomic: decimal.RequireFromString("1000"), Location: liquidity.LocationNative,
		}},
	}
	p, err := liquidity.PlanConsolidate(inv, nil, nil)
	require.NoError(t, err)
	w := liquidity.PlanToWire(p)
	assert.Nil(t, w.Required)
	assert.Empty(t, w.AmountSource)
	assert.Equal(t, string(liquidity.ActionCircleGatewayConsolidate), w.Action)
	require.NotEmpty(t, w.Steps)
	require.Len(t, w.Steps[0].PrepareCalls, 2)
}

func TestGuard_DepositOnly_EmptyPayTo_OK(t *testing.T) {
	g := &liquidity.Guard{}
	p := liquidity.Plan{
		Action: liquidity.ActionCircleGatewayConsolidate,
		Steps: []liquidity.PlanStep{{
			Kind: liquidity.StepKindCircleGatewayDeposit, FromChainCAIP2: arcCAIP2,
			Recipient: agentAddr, RecipientRole: liquidity.RecipientRoleAgentSelf,
			AmountAtomic: decimal.RequireFromString("1"), Asset: arcUSDC,
		}},
	}
	p.BindAgent(agentAddr)
	require.NoError(t, g.CheckPlan(p))
}

func TestGuard_WithdrawEmptyPayTo_OK(t *testing.T) {
	g := &liquidity.Guard{}
	p := liquidity.Plan{
		Action: liquidity.ActionCircleGatewayWithdraw,
		Steps: []liquidity.PlanStep{{
			Kind: liquidity.StepKindCircleGatewayWithdraw, ToChainCAIP2: baseCAIP2,
			Recipient: agentAddr, RecipientRole: liquidity.RecipientRoleAgentSelf,
			AmountAtomic: decimal.RequireFromString("1"), Asset: baseUSDC,
		}},
	}
	p.BindAgent(agentAddr)
	require.NoError(t, g.CheckPlan(p))
}

func TestUnconfiguredExecutor_Consolidate_RailUnavailable(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: arcCAIP2, Asset: arcUSDC, AmountAtomic: decimal.RequireFromString("1000"), Location: liquidity.LocationNative,
		}},
	}
	p, err := liquidity.PlanConsolidate(inv, nil, nil)
	require.NoError(t, err)
	ex := liquidity.UnconfiguredExecutor{}
	_, err = ex.Execute(context.Background(), p)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeLiquidityRailUnavailable, liqerr.CodeOf(err))
}

func TestGatewayWalletAddress_TestnetVsMainnet(t *testing.T) {
	tw, ok := liquidity.GatewayWalletAddress(arcCAIP2)
	require.True(t, ok)
	assert.Equal(t, liquidity.GatewayWalletTestnet, tw)
	mw, ok := liquidity.GatewayWalletAddress(baseCAIP2)
	require.True(t, ok)
	assert.Equal(t, liquidity.GatewayWalletMainnet, mw)
	_, ok = liquidity.GatewayWalletAddress("eip155:999999")
	assert.False(t, ok)
}

func TestListChains_TestnetAndWallet(t *testing.T) {
	for _, c := range liquidity.ListChains() {
		if c.CAIP2 == arcCAIP2 {
			assert.True(t, c.Testnet)
		}
		if c.CAIP2 == baseCAIP2 {
			assert.False(t, c.Testnet)
		}
	}
}