package liquidity

import (
	"strings"

	"github.com/shopspring/decimal"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
)

// PlanGatewayDeposit plans a fixed-N native USDC deposit into Circle Gateway (no pay_to/fee).
// Single explicit source chain; hard error if native on source < amount.
// Delegates to PlanGatewayDeposits.
func PlanGatewayDeposit(
	inv Inventory,
	sourceChainCAIP2 string,
	amountAtomic decimal.Decimal,
	o *Orchestration,
	g *Guard,
) (Plan, error) {
	return PlanGatewayDeposits(inv, []FundingSource{{
		ChainCAIP2:   sourceChainCAIP2,
		AmountAtomic: amountAtomic,
	}}, o, g)
}

// PlanGatewayDeposits plans fixed-amount native USDC deposits into Circle Gateway from
// one or more sources (no pay_to/fee, no payment-sum bookkeeping).
// Hard insufficient_liquidity if any source native < its amount.
// Action is circle_gateway_deposit with one deposit step per normalized source.
func PlanGatewayDeposits(
	inv Inventory,
	sources []FundingSource,
	o *Orchestration,
	g *Guard,
) (Plan, error) {
	agent, err := validateGatewayDepositAgent(inv, o, g)
	if err != nil {
		return Plan{}, err
	}
	norm, err := normalizeDepositSources(sources, o)
	if err != nil {
		return Plan{}, err
	}
	if len(norm) == 0 {
		return Plan{}, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: deposit requires at least one positive source")
	}

	steps := make([]PlanStep, 0, len(norm))
	for _, src := range norm {
		asset, ok := DefaultUSDC(src.ChainCAIP2)
		if !ok || asset == "" {
			return Plan{}, liqerr.New(liqerr.CodeInvalidQuery,
				"liquidity: no registered USDC for source chain")
		}
		req := Required{
			ChainCAIP2:   src.ChainCAIP2,
			Asset:        asset,
			AmountAtomic: src.AmountAtomic,
		}
		native := sumMatchingNative(req, inv, src.ChainCAIP2)
		if native.LessThan(src.AmountAtomic) {
			return Plan{}, liqerr.New(liqerr.CodeInsufficientLiquidity,
				"liquidity: insufficient native USDC on source for deposit amount")
		}
		steps = append(steps, PlanStep{
			Kind:           StepKindCircleGatewayDeposit,
			FromChainCAIP2: src.ChainCAIP2,
			Asset:          asset,
			AmountAtomic:   src.AmountAtomic,
			Recipient:      agent,
			RecipientRole:  RecipientRoleAgentSelf,
		})
	}

	reason := "deposit fixed amount native USDC into circle_gateway (agent_self)"
	if len(steps) > 1 {
		reason = "deposit fixed amounts native USDC into circle_gateway (multi-source; agent_self)"
	}
	base := Plan{
		Action:              ActionCircleGatewayDeposit,
		RecipientRole:       RecipientRoleAgentSelf,
		InventoryAsserted:   true,
		InventoryUnverified: true,
		Executed:            false,
		DryRun:              true,
		agentAddress:        agent,
		Reason:              reason,
		Steps:               steps,
	}
	if err := attachDepositPrepareCallsOnPlan(&base); err != nil {
		return Plan{}, err
	}
	if err := g.CheckPlan(base); err != nil {
		return Plan{}, err
	}
	return base, nil
}

func validateGatewayDepositAgent(inv Inventory, o *Orchestration, g *Guard) (agent string, err error) {
	if o != nil && !o.gatewayAllowed() {
		return "", liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: allow_circle_gateway=false refuses deposit")
	}
	agent = strings.TrimSpace(inv.AgentAddress)
	if agent == "" {
		return "", liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: agent_address required to plan deposit (agent_self recipient)")
	}
	if err := g.CheckAgent(inv); err != nil {
		return "", err
	}
	return agent, nil
}

// normalizeDepositSources merges/validates fixed deposit sources (GatewayOK, wallet, allowlist).
// Duplicate chains sum amounts. Zero/negative amounts skipped; all-zero → empty.
func normalizeDepositSources(sources []FundingSource, o *Orchestration) ([]FundingSource, error) {
	allow := sourceAllowlist(o)
	// Reuse payment-funding merge (GatewayOK registry + whole atomic units).
	norm, _, err := normalizeFundingSources(sources)
	if err != nil {
		return nil, err
	}
	out := make([]FundingSource, 0, len(norm))
	for _, src := range norm {
		if _, ok := GatewayWalletAddress(src.ChainCAIP2); !ok {
			return nil, liqerr.New(liqerr.CodeInvalidQuery,
				"liquidity: no gateway wallet for source chain")
		}
		if len(allow) > 0 && !chainInList(src.ChainCAIP2, allow) {
			return nil, liqerr.New(liqerr.CodeInvalidQuery,
				"liquidity: source_chain_caip2 not in orchestration.source_chain_caip2s allowlist")
		}
		if !src.AmountAtomic.IsPositive() || !src.AmountAtomic.Equal(src.AmountAtomic.Truncate(0)) {
			return nil, liqerr.New(liqerr.CodeInvalidQuery,
				"liquidity: deposit amount_atomic must be positive whole atomic units")
		}
		out = append(out, src)
	}
	return out, nil
}