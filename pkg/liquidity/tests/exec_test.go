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

func TestUnconfiguredExecutor_AlwaysErrors(t *testing.T) {
	ex := liquidity.UnconfiguredExecutor{}
	p := liquidity.Plan{
		Action: liquidity.ActionCircleGatewayWithdraw,
		Required: liquidity.Required{
			PayTo: merchantPayTo, ChainCAIP2: baseCAIP2, Asset: baseUSDC,
			AmountAtomic: decimal.RequireFromString("1000000"),
		},
		Steps: []liquidity.PlanStep{{
			Kind: liquidity.StepKindCircleGatewayWithdraw, Recipient: agentAddr,
			RecipientRole: liquidity.RecipientRoleAgentSelf,
			AmountAtomic:  decimal.RequireFromString("1000000"),
		}},
	}
	p.BindAgent(agentAddr)
	_, err := ex.Execute(context.Background(), p)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeLiquidityRailUnavailable, liqerr.CodeOf(err))
}

func TestUnconfiguredExecutor_RejectsPayToAsRecipient(t *testing.T) {
	ex := liquidity.UnconfiguredExecutor{}
	p := liquidity.Plan{
		Action: liquidity.ActionCircleGatewayWithdraw,
		Required: liquidity.Required{
			PayTo: merchantPayTo, ChainCAIP2: baseCAIP2, Asset: baseUSDC,
			AmountAtomic: decimal.RequireFromString("1"),
		},
		Steps: []liquidity.PlanStep{{
			Kind: liquidity.StepKindCircleGatewayWithdraw, Recipient: merchantPayTo,
			RecipientRole: liquidity.RecipientRoleAgentSelf,
			AmountAtomic:  decimal.RequireFromString("1"),
		}},
	}
	p.BindAgent(agentAddr)
	_, err := ex.Execute(context.Background(), p)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
}

func TestUnconfiguredExecutor_NoopNeverSuccess(t *testing.T) {
	ex := liquidity.UnconfiguredExecutor{}
	p := liquidity.Plan{
		Action: liquidity.ActionNoop,
		Required: liquidity.Required{
			PayTo: merchantPayTo, ChainCAIP2: baseCAIP2, Asset: baseUSDC,
			AmountAtomic: decimal.RequireFromString("1"),
		},
	}
	_, err := ex.Execute(context.Background(), p)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeLiquidityRailUnavailable, liqerr.CodeOf(err))
}