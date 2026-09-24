package planio

import (
	"context"
	"strings"

	"github.com/shopspring/decimal"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/types"
)

// RunPlan shortfall-plans and optionally executes. Pre-plan errors return StampFail
// without calling Execute. StampPlan uses SanitizeAPIError for execute failures.
func RunPlan(ctx context.Context, ex liquidity.Executor, req types.PlanRequest) (types.PlanResponse, StampOutcome) {
	if ex == nil {
		ex = liquidity.UnconfiguredExecutor{}
	}
	required, err := liquidity.RequiredFromWire(&req.Required, req.AmountOverride)
	if err != nil {
		return CodedPlanError(err)
	}
	inv, err := liquidity.InventoryFromWire(req.Inventory)
	if err != nil {
		return CodedPlanError(err)
	}
	orch := liquidity.OrchestrationFromWire(req.Orchestration)
	fee := liquidity.FeeConfigFromWire(req.FeeBps, req.FeeRecipient)

	plan, err := liquidity.PlanOrchestration(required, inv, orch, fee, nil)
	if err != nil {
		return CodedPlanError(err)
	}
	return stampAfterExecute(ctx, ex, plan, req.Execute)
}

// RunConsolidate plans Gateway deposits and optionally executes.
func RunConsolidate(ctx context.Context, ex liquidity.Executor, req types.ConsolidateRequest) (types.PlanResponse, StampOutcome) {
	if ex == nil {
		ex = liquidity.UnconfiguredExecutor{}
	}
	inv, err := liquidity.InventoryFromWire(req.Inventory)
	if err != nil {
		return CodedPlanError(err)
	}
	orch := liquidity.OrchestrationFromWire(req.Orchestration)

	plan, err := liquidity.PlanConsolidate(inv, orch, nil)
	if err != nil {
		return CodedPlanError(err)
	}
	return stampAfterExecute(ctx, ex, plan, req.Execute)
}

// RunDeposit plans a fixed-N Gateway deposit (single or multi-source) and optionally executes.
func RunDeposit(ctx context.Context, ex liquidity.Executor, req types.DepositRequest) (types.PlanResponse, StampOutcome) {
	if ex == nil {
		ex = liquidity.UnconfiguredExecutor{}
	}
	inv, err := liquidity.InventoryFromWire(req.Inventory)
	if err != nil {
		return CodedPlanError(err)
	}
	orch := liquidity.OrchestrationFromWire(req.Orchestration)

	singleSet := strings.TrimSpace(req.SourceChainCAIP2) != "" || strings.TrimSpace(req.AmountAtomic) != ""
	multiSet := len(req.Sources) > 0
	if singleSet && multiSet {
		return CodedPlanError(liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: deposit sources[] is mutually exclusive with source_chain_caip2/amount_atomic"))
	}
	if !singleSet && !multiSet {
		return CodedPlanError(liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: deposit requires source_chain_caip2+amount_atomic or sources[]"))
	}

	var plan liquidity.Plan
	if multiSet {
		sources, err := liquidity.FundingSourcesFromWire(req.Sources)
		if err != nil {
			return CodedPlanError(err)
		}
		plan, err = liquidity.PlanGatewayDeposits(inv, sources, orch, nil)
		if err != nil {
			return CodedPlanError(err)
		}
	} else {
		amt, err := parseAtomicWire(req.AmountAtomic)
		if err != nil {
			return CodedPlanError(err)
		}
		plan, err = liquidity.PlanGatewayDeposit(inv, req.SourceChainCAIP2, amt, orch, nil)
		if err != nil {
			return CodedPlanError(err)
		}
	}
	return stampAfterExecute(ctx, ex, plan, req.Execute)
}

// RunMove plans self-land shortfall rebalance and optionally executes.
func RunMove(ctx context.Context, ex liquidity.Executor, req types.MoveRequest) (types.PlanResponse, StampOutcome) {
	if ex == nil {
		ex = liquidity.UnconfiguredExecutor{}
	}
	inv, err := liquidity.InventoryFromWire(req.Inventory)
	if err != nil {
		return CodedPlanError(err)
	}
	amt, err := parseAtomicWire(req.AmountAtomic)
	if err != nil {
		return CodedPlanError(err)
	}
	orch := liquidity.OrchestrationFromWire(req.Orchestration)

	plan, err := liquidity.PlanSelfRebalance(req.DestChainCAIP2, amt, inv, orch, nil)
	if err != nil {
		return CodedPlanError(err)
	}
	return stampAfterExecute(ctx, ex, plan, req.Execute)
}

func parseAtomicWire(s string) (decimal.Decimal, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery, "liquidity: amount_atomic required")
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery, "liquidity: amount_atomic must be a decimal")
	}
	if !d.IsPositive() || !d.Equal(d.Truncate(0)) {
		return decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: amount_atomic must be positive whole atomic units")
	}
	return d.Truncate(0), nil
}

// RunPaymentFunding plans scenario full-funding and optionally executes.
func RunPaymentFunding(ctx context.Context, ex liquidity.Executor, req types.PaymentFundingRequest) (types.PlanResponse, StampOutcome) {
	if ex == nil {
		ex = liquidity.UnconfiguredExecutor{}
	}
	required, err := liquidity.RequiredFromWire(&req.Required, "")
	if err != nil {
		return CodedPlanError(err)
	}
	if log := strings.TrimSpace(req.Required.AmountLogicalAtomic); log != "" {
		if d, err := decimal.NewFromString(log); err == nil && d.IsPositive() && d.Equal(d.Truncate(0)) {
			required.AmountLogicalAtomic = d.Truncate(0)
		}
	}
	if req.Required.ScaleFactor > 0 {
		required.ScaleFactor = req.Required.ScaleFactor
	}

	inv, err := liquidity.InventoryFromWire(req.Inventory)
	if err != nil {
		return CodedPlanError(err)
	}
	sources, err := liquidity.FundingSourcesFromWire(req.Sources)
	if err != nil {
		return CodedPlanError(err)
	}

	plan, err := liquidity.PlanPaymentFunding(required, inv, sources, nil)
	if err != nil {
		return CodedPlanError(err)
	}
	return stampAfterExecute(ctx, ex, plan, req.Execute)
}

// ListChains returns the registered corridor discovery payload.
func ListChains() types.ChainsResponse {
	reg := liquidity.ListChains()
	out := make([]types.ChainInfo, 0, len(reg))
	for _, c := range reg {
		wallet, _ := liquidity.GatewayWalletAddress(c.CAIP2)
		out = append(out, types.ChainInfo{
			CAIP2: c.CAIP2, Name: c.Name, GatewayDomain: c.GatewayDomain,
			USDC: c.USDC, GatewayOK: c.GatewayOK, CCTPOK: c.CCTPOK,
			Testnet: c.Testnet, GatewayWallet: wallet,
		})
	}
	return types.ChainsResponse{Chains: out}
}

func stampAfterExecute(ctx context.Context, ex liquidity.Executor, plan liquidity.Plan, execute bool) (types.PlanResponse, StampOutcome) {
	wire := liquidity.PlanToWire(plan)
	var receipt liquidity.Receipt
	var execErr error
	if execute {
		receipt, execErr = ex.Execute(ctx, plan)
	}
	return StampPlan(wire, execute, receipt, execErr)
}