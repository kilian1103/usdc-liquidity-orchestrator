package liquidity

import (
	"context"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
)

// Non-secret Circle Gateway addresses (for prepare_calls + future live Executor).
// Do not treat these as credentials; no API keys belong in this package.
const (
	// GatewayAPITestnetBase is the Circle Gateway testnet API base URL.
	GatewayAPITestnetBase = "https://gateway-api-testnet.circle.com"
	// GatewayWalletTestnet is the Gateway Wallet contract on EVM testnets.
	GatewayWalletTestnet = "0x0077777d7EBA4688BDeF3E311b846F25870A19B9"
	// GatewayWalletMainnet is the Gateway Wallet contract on EVM mainnets.
	GatewayWalletMainnet = "0x77777777Dcc4d5A8B6E418Fd04D8997ef11000eE"
	// GatewayMinterTestnet is the Gateway Minter contract on EVM testnets.
	GatewayMinterTestnet = "0x0022222ABE238Cc2C7Bb1f21003F0a260052475B"
)

// Receipt is on-chain execute evidence (tx hashes only).
type Receipt struct {
	TxHashes []string
}

// Executor runs a plan (circle_gateway / cctp). This cut ships the fail-closed stub.
// Future adapters map PlanStep kinds to Gateway deposit / burn-intent / mint.
type Executor interface {
	Execute(ctx context.Context, p Plan) (Receipt, error)
}

// UnconfiguredExecutor always fails closed after validating the plan.
// No live Circle Gateway / CCTP SDK in this cut — safe for demos and CI.
type UnconfiguredExecutor struct {
	Guard *Guard
}

// Execute validates plan first, then returns a typed error. Never succeeds.
func (e UnconfiguredExecutor) Execute(ctx context.Context, p Plan) (Receipt, error) {
	_ = ctx
	g := e.Guard
	if err := g.CheckPlan(p); err != nil {
		return Receipt{}, err
	}
	switch p.Action {
	case ActionInsufficient, ActionCorridorUnsupported:
		return Receipt{}, liqerr.New(liqerr.CodeInsufficientLiquidity,
			"liquidity execute: plan action %s — %s", p.Action, p.Reason)
	case ActionNoop:
		return Receipt{}, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"liquidity execute: not configured (noop dry plan is not an on-chain success)")
	case ActionCircleGatewayConsolidate, ActionCircleGatewayDeposit,
		ActionCircleGatewayWithdraw, ActionCCTPFast:
		return Receipt{}, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"liquidity execute: circle_gateway/cctp not configured — refuse %s", p.Action)
	default:
		return Receipt{}, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"liquidity execute: circle_gateway/cctp not configured — refuse %s", p.Action)
	}
}