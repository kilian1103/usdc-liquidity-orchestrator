package liquidity_test

import (
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
)

func TestPlanSelfRebalance_NoopWhenDestCovers(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: baseCAIP2, Asset: baseUSDC,
			AmountAtomic: decimal.RequireFromString("2000000"), Location: liquidity.LocationNative,
		}},
	}
	p, err := liquidity.PlanSelfRebalance(baseCAIP2, decimal.RequireFromString("1000000"), inv, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, liquidity.ActionNoop, p.Action)
	assert.True(t, p.DryRun)
	assert.Nil(t, p.Fee)
	assert.Equal(t, liquidity.AmountSourceSelf, p.Required.AmountSource)
	assert.Empty(t, p.Required.PayTo)
}

func TestPlanSelfRebalance_GatewayWithdraw(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			Asset: "USDC", AmountAtomic: decimal.RequireFromString("5000000"),
			Location: liquidity.LocationCircleGateway,
		}},
	}
	p, err := liquidity.PlanSelfRebalance(baseCAIP2, decimal.RequireFromString("1000000"), inv, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, liquidity.ActionCircleGatewayWithdraw, p.Action)
	require.Len(t, p.Steps, 1)
	assert.Equal(t, agentAddr, p.Steps[0].Recipient)
	assert.Equal(t, liquidity.RecipientRoleAgentSelf, p.Steps[0].RecipientRole)
	assert.True(t, p.Steps[0].AmountAtomic.Equal(decimal.RequireFromString("1000000")))
	assert.Nil(t, p.Fee)
}

func TestPlanSelfRebalance_OtherNative_CCTP_ShortfallOnly(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{
			{ChainCAIP2: baseCAIP2, Asset: baseUSDC, AmountAtomic: decimal.RequireFromString("200000"), Location: liquidity.LocationNative},
			{ChainCAIP2: arbCAIP2, Asset: arbUSDC, AmountAtomic: decimal.RequireFromString("5000000"), Location: liquidity.LocationNative},
		},
	}
	p, err := liquidity.PlanSelfRebalance(baseCAIP2, decimal.RequireFromString("1000000"), inv, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, liquidity.ActionCCTPFast, p.Action)
	require.Len(t, p.Steps, 2)
	// shortfall = 1000000 - 200000 = 800000
	assert.True(t, p.Steps[0].AmountAtomic.Equal(decimal.RequireFromString("800000")))
	assert.True(t, p.Steps[1].AmountAtomic.Equal(decimal.RequireFromString("800000")))
	assert.Equal(t, liquidity.StepKindCCTPBurn, p.Steps[0].Kind)
	for _, s := range p.Steps {
		assert.Equal(t, agentAddr, s.Recipient)
		assert.Equal(t, liquidity.RecipientRoleAgentSelf, s.RecipientRole)
	}
}

func TestPlanSelfRebalance_PreferCCTP(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: arbCAIP2, Asset: arbUSDC,
			AmountAtomic: decimal.RequireFromString("5000000"), Location: liquidity.LocationNative,
		}},
	}
	orch := &liquidity.Orchestration{PreferRail: liquidity.PreferRailCCTPFast}
	p, err := liquidity.PlanSelfRebalance(baseCAIP2, decimal.RequireFromString("1000000"), inv, orch, nil)
	require.NoError(t, err)
	assert.Equal(t, liquidity.ActionCCTPFast, p.Action)
	require.Len(t, p.Steps, 2)
	assert.Equal(t, liquidity.StepKindCCTPBurn, p.Steps[0].Kind)
}

func TestPlanSelfRebalance_SourcesAllowlist(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{
			{ChainCAIP2: arbCAIP2, Asset: arbUSDC, AmountAtomic: decimal.RequireFromString("5000000"), Location: liquidity.LocationNative},
			{ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC, AmountAtomic: decimal.RequireFromString("5000000"), Location: liquidity.LocationNative},
		},
	}
	orch := &liquidity.Orchestration{SourceChainCAIP2s: []string{baseSepCAIP2}}
	p, err := liquidity.PlanSelfRebalance(arcCAIP2, decimal.RequireFromString("1000000"), inv, orch, nil)
	require.NoError(t, err)
	assert.Equal(t, liquidity.ActionCCTPFast, p.Action)
	assert.Equal(t, baseSepCAIP2, p.Steps[0].FromChainCAIP2)
	assert.Equal(t, liquidity.StepKindCCTPBurn, p.Steps[0].Kind)
}

func TestPlanSelfRebalance_InsufficientPlan(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: baseCAIP2, Asset: baseUSDC,
			AmountAtomic: decimal.RequireFromString("1"), Location: liquidity.LocationNative,
		}},
	}
	p, err := liquidity.PlanSelfRebalance(baseCAIP2, decimal.RequireFromString("1000000"), inv, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, liquidity.ActionInsufficient, p.Action)
	assert.Empty(t, p.Steps)
	assert.True(t, p.DryRun)
}

func TestPlanSelfRebalance_CorridorUnsupported(t *testing.T) {
	p, err := liquidity.PlanSelfRebalance("eip155:999999", decimal.RequireFromString("1000"),
		liquidity.Inventory{AgentAddress: agentAddr}, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, liquidity.ActionCorridorUnsupported, p.Action)
}

func TestPlanSelfRebalance_PlanToWire_LandRequiredNoPayTo(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			Asset: "USDC", AmountAtomic: decimal.RequireFromString("5000000"),
			Location: liquidity.LocationCircleGateway,
		}},
	}
	p, err := liquidity.PlanSelfRebalance(baseCAIP2, decimal.RequireFromString("1000000"), inv, nil, nil)
	require.NoError(t, err)
	w := liquidity.PlanToWire(p)
	require.NotNil(t, w.Required)
	assert.Equal(t, baseCAIP2, w.Required.ChainCAIP2)
	assert.Equal(t, "1000000", w.Required.AmountAtomic)
	assert.Equal(t, liquidity.AmountSourceSelf, w.Required.Source)
	assert.Equal(t, liquidity.AmountSourceSelf, w.AmountSource)
	assert.Empty(t, w.Required.PayTo)
	assert.Empty(t, w.Required.PayToRole)
	assert.Nil(t, w.Fee)

	b, err := json.Marshal(w.Required)
	require.NoError(t, err)
	assert.NotContains(t, string(b), `"pay_to"`)
	assert.NotContains(t, string(b), `"pay_to_role"`)
}

func TestGuard_SelfRebalance_WithdrawEmptyPayTo_OK(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			Asset: "USDC", AmountAtomic: decimal.RequireFromString("5000000"),
			Location: liquidity.LocationCircleGateway,
		}},
	}
	p, err := liquidity.PlanSelfRebalance(baseCAIP2, decimal.RequireFromString("1000000"), inv, nil, nil)
	require.NoError(t, err)
	require.NoError(t, (&liquidity.Guard{}).CheckPlan(p))
}

func TestGuard_WithdrawAndDepositSteps_EmptyPayTo_OK(t *testing.T) {
	// Guard allows empty pay_to for agent_self fund steps (Phase split is planner/executor).
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

func TestPlanSelfRebalance_MissingAgentAmount(t *testing.T) {
	_, err := liquidity.PlanSelfRebalance(baseCAIP2, decimal.RequireFromString("1000"),
		liquidity.Inventory{}, nil, nil)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))

	_, err = liquidity.PlanSelfRebalance(baseCAIP2, decimal.Zero,
		liquidity.Inventory{AgentAddress: agentAddr}, nil, nil)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
}