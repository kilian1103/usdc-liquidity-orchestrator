package planio_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/planio"
	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/types"
)

type stubExecutor struct {
	receipt liquidity.Receipt
	err     error
}

func (s stubExecutor) Execute(ctx context.Context, p liquidity.Plan) (liquidity.Receipt, error) {
	return s.receipt, s.err
}

const (
	arcCAIP2      = "eip155:5042002"
	arcUSDC       = "0x3600000000000000000000000000000000000000"
	merchantPayTo = "0xMerchantPayTo000000000000000000000001"
	agentAddr     = "0xAgentSelf000000000000000000000000000001"
	baseSepCAIP2  = "eip155:84532"
	baseSepUSDC   = "0x036CbD53842c5426634e7929541eC2318f3dCF7e"
)

func TestForceDryStamps(t *testing.T) {
	w := types.Plan{Action: "x", DryRun: false, Executed: true}
	got := planio.ForceDryStamps(w)
	assert.True(t, got.DryRun)
	assert.False(t, got.Executed)
	assert.True(t, got.InventoryAsserted)
	assert.True(t, got.InventoryUnverified)
}

func TestStampMatrix(t *testing.T) {
	base := types.Plan{Action: "circle_gateway_consolidate"}
	tests := []struct {
		name     string
		execute  bool
		receipt  liquidity.Receipt
		execErr  error
		wantOut  planio.StampOutcome
		wantHTTP int
		wantExit int
		dry      bool
		executed bool
		hasRcpt  bool
		hasErr   bool
	}{
		{
			name: "execute_false_force_dry", execute: false,
			receipt: liquidity.Receipt{TxHashes: []string{"0xshouldnot"}},
			wantOut: planio.StampOK, wantHTTP: 200, wantExit: 0,
			dry: true, executed: false, hasRcpt: false, hasErr: false,
		},
		{
			name: "success", execute: true,
			receipt: liquidity.Receipt{TxHashes: []string{"0xabc", "0xdef"}},
			wantOut: planio.StampOK, wantHTTP: 200, wantExit: 0,
			dry: false, executed: true, hasRcpt: true, hasErr: false,
		},
		{
			name: "partial", execute: true,
			receipt: liquidity.Receipt{TxHashes: []string{"0xpartial"}},
			execErr: liqerr.New(liqerr.CodeLiquidityRailUnavailable, "deposit execute: broadcast failed"),
			wantOut: planio.StampPartial, wantHTTP: 400, wantExit: 1,
			dry: false, executed: false, hasRcpt: true, hasErr: true,
		},
		{
			name: "fail_zero_hashes", execute: true,
			execErr: liqerr.Wrap(liqerr.CodeLiquidityRailUnavailable, assert.AnError, "deposit execute: broadcast failed"),
			wantOut: planio.StampFail, wantHTTP: 400, wantExit: 1,
			dry: true, executed: false, hasRcpt: false, hasErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, out := planio.StampPlan(base, tc.execute, tc.receipt, tc.execErr)
			assert.Equal(t, tc.wantOut, out)
			assert.Equal(t, tc.wantHTTP, planio.HTTPStatus(out))
			assert.Equal(t, tc.wantExit, planio.ExitCode(out))
			assert.Equal(t, tc.dry, resp.Plan.DryRun)
			assert.Equal(t, tc.executed, resp.Plan.Executed)
			if tc.hasRcpt {
				require.NotNil(t, resp.Receipt)
			} else {
				assert.Nil(t, resp.Receipt)
			}
			if tc.hasErr {
				require.NotNil(t, resp.Error)
				assert.NotContains(t, resp.Error.Message, "assert.AnError")
			} else {
				assert.Nil(t, resp.Error)
			}
		})
	}
}

func TestSanitizeAPIError_Plain(t *testing.T) {
	api := planio.SanitizeAPIError(assert.AnError)
	require.NotNil(t, api)
	assert.Equal(t, liqerr.CodeLiquidityRailUnavailable, api.Code)
	assert.Equal(t, "execute failed", api.Message)
}

func TestRunPlan_Dry(t *testing.T) {
	req := planRequest(false)
	resp, out := planio.RunPlan(context.Background(), nil, req)
	assert.Equal(t, planio.StampOK, out)
	assert.Nil(t, resp.Error)
	assert.True(t, resp.Plan.DryRun)
	assert.Equal(t, "circle_gateway_withdraw", resp.Plan.Action)
}

func TestRunPlan_EmptyPayTo_OK(t *testing.T) {
	req := planRequest(false)
	req.Required.PayTo = ""
	req.Required.PayToRole = ""
	resp, out := planio.RunPlan(context.Background(), nil, req)
	assert.Equal(t, planio.StampOK, out)
	assert.Nil(t, resp.Error)
	assert.Equal(t, "circle_gateway_withdraw", resp.Plan.Action)
}

func TestRunPlan_ExecuteUnconfigured(t *testing.T) {
	req := planRequest(true)
	resp, out := planio.RunPlan(context.Background(), liquidity.UnconfiguredExecutor{}, req)
	assert.Equal(t, planio.StampFail, out)
	require.NotNil(t, resp.Error)
	assert.Equal(t, liqerr.CodeLiquidityRailUnavailable, resp.Error.Code)
	assert.True(t, resp.Plan.DryRun)
}

func TestRunPlan_PrePlanSkipsExecute(t *testing.T) {
	called := false
	ex := &countingExecutor{fn: func() { called = true }}
	req := planRequest(true)
	req.Required.ChainCAIP2 = "" // invalid → pre-plan error before Execute
	resp, out := planio.RunPlan(context.Background(), ex, req)
	assert.Equal(t, planio.StampFail, out)
	assert.False(t, called, "Execute must not run on pre-plan error")
	require.NotNil(t, resp.Error)
}

func TestRunConsolidate_Dry(t *testing.T) {
	req := types.ConsolidateRequest{
		Inventory: types.Inventory{
			AgentAddress: agentAddr,
			Balances: []types.Balance{{
				ChainCAIP2: arcCAIP2, Asset: arcUSDC,
				AmountAtomic: "1000", Location: "native",
			}},
		},
	}
	resp, out := planio.RunConsolidate(context.Background(), nil, req)
	assert.Equal(t, planio.StampOK, out)
	assert.Equal(t, "circle_gateway_consolidate", resp.Plan.Action)
	assert.True(t, resp.Plan.DryRun)
}

func TestRunDeposit_Dry(t *testing.T) {
	req := types.DepositRequest{
		Inventory: types.Inventory{
			AgentAddress: agentAddr,
			Balances: []types.Balance{{
				ChainCAIP2: arcCAIP2, Asset: arcUSDC,
				AmountAtomic: "1000", Location: "native",
			}},
		},
		SourceChainCAIP2: arcCAIP2,
		AmountAtomic:     "500",
	}
	resp, out := planio.RunDeposit(context.Background(), nil, req)
	assert.Equal(t, planio.StampOK, out)
	assert.Nil(t, resp.Error)
	assert.Equal(t, "circle_gateway_deposit", resp.Plan.Action)
	assert.True(t, resp.Plan.DryRun)
	assert.Nil(t, resp.Plan.Required)
}

func TestRunDeposit_Underfund_StampFail(t *testing.T) {
	req := types.DepositRequest{
		Inventory: types.Inventory{
			AgentAddress: agentAddr,
			Balances: []types.Balance{{
				ChainCAIP2: arcCAIP2, Asset: arcUSDC,
				AmountAtomic: "100", Location: "native",
			}},
		},
		SourceChainCAIP2: arcCAIP2,
		AmountAtomic:     "1000",
	}
	resp, out := planio.RunDeposit(context.Background(), nil, req)
	assert.Equal(t, planio.StampFail, out)
	require.NotNil(t, resp.Error)
	assert.Equal(t, liqerr.CodeInsufficientLiquidity, resp.Error.Code)
}

func TestRunDeposit_MultiDry(t *testing.T) {
	const arbSepCAIP2 = "eip155:421614"
	const arbSepUSDC = "0x75faf114eafb1BDbe2F0316DF893fd58CE46AA4d"
	req := types.DepositRequest{
		Inventory: types.Inventory{
			AgentAddress: agentAddr,
			Balances: []types.Balance{
				{ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC, AmountAtomic: "3000000", Location: "native"},
				{ChainCAIP2: arbSepCAIP2, Asset: arbSepUSDC, AmountAtomic: "2000000", Location: "native"},
			},
		},
		Sources: []types.FundingSource{
			{ChainCAIP2: baseSepCAIP2, AmountAtomic: "3000000"},
			{ChainCAIP2: arbSepCAIP2, AmountAtomic: "2000000"},
		},
	}
	resp, out := planio.RunDeposit(context.Background(), nil, req)
	assert.Equal(t, planio.StampOK, out)
	assert.Nil(t, resp.Error)
	assert.Equal(t, "circle_gateway_deposit", resp.Plan.Action)
	assert.True(t, resp.Plan.DryRun)
	require.Len(t, resp.Plan.Steps, 2)
}

func TestRunDeposit_ExclusiveSingleAndMulti(t *testing.T) {
	req := types.DepositRequest{
		Inventory: types.Inventory{
			AgentAddress: agentAddr,
			Balances: []types.Balance{{
				ChainCAIP2: arcCAIP2, Asset: arcUSDC,
				AmountAtomic: "1000", Location: "native",
			}},
		},
		SourceChainCAIP2: arcCAIP2,
		AmountAtomic:     "500",
		Sources: []types.FundingSource{
			{ChainCAIP2: arcCAIP2, AmountAtomic: "500"},
		},
	}
	resp, out := planio.RunDeposit(context.Background(), nil, req)
	assert.Equal(t, planio.StampFail, out)
	require.NotNil(t, resp.Error)
	assert.Equal(t, liqerr.CodeInvalidQuery, resp.Error.Code)
}

func TestRunDeposit_ExclusiveSourcesPlusAmountAtomicOnly(t *testing.T) {
	// sources[] + amount_atomic (no source_chain_caip2) is still exclusive → invalid_query.
	req := types.DepositRequest{
		Inventory: types.Inventory{
			AgentAddress: agentAddr,
			Balances: []types.Balance{{
				ChainCAIP2: arcCAIP2, Asset: arcUSDC,
				AmountAtomic: "1000", Location: "native",
			}},
		},
		AmountAtomic: "500",
		Sources: []types.FundingSource{
			{ChainCAIP2: arcCAIP2, AmountAtomic: "500"},
		},
	}
	resp, out := planio.RunDeposit(context.Background(), nil, req)
	assert.Equal(t, planio.StampFail, out)
	require.NotNil(t, resp.Error)
	assert.Equal(t, liqerr.CodeInvalidQuery, resp.Error.Code)
}

func TestRunDeposit_NeitherMode(t *testing.T) {
	// Neither single-source fields nor sources[] → clear mode error (not amount_atomic required).
	req := types.DepositRequest{
		Inventory: types.Inventory{
			AgentAddress: agentAddr,
			Balances: []types.Balance{{
				ChainCAIP2: arcCAIP2, Asset: arcUSDC,
				AmountAtomic: "1000", Location: "native",
			}},
		},
	}
	resp, out := planio.RunDeposit(context.Background(), nil, req)
	assert.Equal(t, planio.StampFail, out)
	require.NotNil(t, resp.Error)
	assert.Equal(t, liqerr.CodeInvalidQuery, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "source_chain_caip2+amount_atomic or sources[]")
}

func TestRunMove_DryWithdraw(t *testing.T) {
	req := types.MoveRequest{
		DestChainCAIP2: arcCAIP2,
		AmountAtomic:   "1000",
		Inventory: types.Inventory{
			AgentAddress: agentAddr,
			Balances: []types.Balance{{
				Asset: "USDC", AmountAtomic: "5000", Location: "circle_gateway",
			}},
		},
	}
	resp, out := planio.RunMove(context.Background(), nil, req)
	assert.Equal(t, planio.StampOK, out)
	assert.Nil(t, resp.Error)
	assert.Equal(t, "circle_gateway_withdraw", resp.Plan.Action)
	assert.True(t, resp.Plan.DryRun)
	require.NotNil(t, resp.Plan.Required)
	assert.Equal(t, "self", resp.Plan.AmountSource)
	assert.Empty(t, resp.Plan.Required.PayTo)
}

func TestRunMove_Insufficient_StampOK(t *testing.T) {
	req := types.MoveRequest{
		DestChainCAIP2: arcCAIP2,
		AmountAtomic:   "1000000",
		Inventory: types.Inventory{
			AgentAddress: agentAddr,
			Balances: []types.Balance{{
				ChainCAIP2: arcCAIP2, Asset: arcUSDC,
				AmountAtomic: "1", Location: "native",
			}},
		},
	}
	resp, out := planio.RunMove(context.Background(), nil, req)
	assert.Equal(t, planio.StampOK, out)
	assert.Nil(t, resp.Error)
	assert.Equal(t, "insufficient", resp.Plan.Action)
	assert.True(t, resp.Plan.DryRun)
}

func TestRunConsolidate_ExecuteSuccess(t *testing.T) {
	ex := stubExecutor{receipt: liquidity.Receipt{TxHashes: []string{"0xabc"}}}
	req := types.ConsolidateRequest{
		Inventory: types.Inventory{
			AgentAddress: agentAddr,
			Balances: []types.Balance{{
				ChainCAIP2: arcCAIP2, Asset: arcUSDC,
				AmountAtomic: "1000", Location: "native",
			}},
		},
		Execute: true,
	}
	resp, out := planio.RunConsolidate(context.Background(), ex, req)
	assert.Equal(t, planio.StampOK, out)
	assert.True(t, resp.Plan.Executed)
	require.NotNil(t, resp.Receipt)
}

func TestRunPaymentFunding_Dry(t *testing.T) {
	req := types.PaymentFundingRequest{
		Required: types.Required{
			Protocol: "x402", ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC,
			AmountAtomic: "40000000", AmountLogicalAtomic: "400000000", ScaleFactor: 10,
			PayTo: merchantPayTo, PayToRole: "merchant",
		},
		Inventory: types.Inventory{
			AgentAddress: agentAddr,
			Balances: []types.Balance{
				{Location: "native", ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC, AmountAtomic: "30000000"},
				{Location: "native", ChainCAIP2: "eip155:421614", Asset: "0x75faf114eafb1BDbe2F0316DF893fd58CE46AA4d", AmountAtomic: "10000000"},
			},
		},
		Sources: []types.FundingSource{
			{ChainCAIP2: baseSepCAIP2, AmountAtomic: "30000000", AmountLogicalAtomic: "300000000"},
			{ChainCAIP2: "eip155:421614", AmountAtomic: "10000000", AmountLogicalAtomic: "100000000"},
		},
	}
	resp, out := planio.RunPaymentFunding(context.Background(), nil, req)
	assert.Equal(t, planio.StampOK, out)
	assert.Equal(t, "circle_gateway_deposit", resp.Plan.Action)
	assert.Equal(t, "400000000", resp.Plan.Required.AmountLogicalAtomic)
	assert.Equal(t, int64(10), resp.Plan.Required.ScaleFactor)
}

func TestListChains_IncludesArc(t *testing.T) {
	body := planio.ListChains()
	var found bool
	for _, c := range body.Chains {
		if c.CAIP2 == arcCAIP2 {
			found = true
			assert.Equal(t, 26, c.GatewayDomain)
			assert.True(t, c.GatewayOK)
		}
	}
	assert.True(t, found)
}

type countingExecutor struct {
	fn func()
}

func (c *countingExecutor) Execute(ctx context.Context, p liquidity.Plan) (liquidity.Receipt, error) {
	c.fn()
	return liquidity.Receipt{}, nil
}

func planRequest(execute bool) types.PlanRequest {
	return types.PlanRequest{
		Required: types.Required{
			Protocol: "x402", ChainCAIP2: arcCAIP2, Asset: arcUSDC,
			AmountAtomic: "42000000", PayTo: merchantPayTo, PayToRole: "merchant",
		},
		Inventory: types.Inventory{
			AgentAddress: agentAddr,
			Balances: []types.Balance{
				{Asset: "USDC", AmountAtomic: "100000000", Location: "circle_gateway"},
				{ChainCAIP2: arcCAIP2, Asset: arcUSDC, AmountAtomic: "20000000", Location: "native"},
			},
		},
		Orchestration: &types.Orchestration{
			TargetChainCAIP2: arcCAIP2,
			PreferRail:       "circle_gateway",
		},
		Execute: execute,
	}
}