package liquidity

import (
	"sort"
	"strings"

	"github.com/shopspring/decimal"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
)

// FundingSource is one hard-coded deposit for scenario Phase A funding plans.
// AmountAtomic is the real on-chain amount; AmountLogicalAtomic is optional wire stamp metadata.
type FundingSource struct {
	ChainCAIP2          string
	AmountAtomic        decimal.Decimal // real
	AmountLogicalAtomic decimal.Decimal // logical (pre-scale); zero omits wire stamp
}

// PlanPaymentFunding plans Phase A multi-source fixed deposits only (not shortfall, not composite).
//
// Action is circle_gateway_deposit when sources are valid: one circle_gateway_deposit per
// positive source (real amount) with prepare_calls. No withdraw step — after deposit execute,
// agents wait Gateway finality (~13–19m) then plan Phase B withdraw separately.
//
// pay_to is optional residual only; fund recipients are always agent_self.
// Dry stamps always: dry_run=true, executed=false, inventory_asserted/unverified=true.
func PlanPaymentFunding(req Required, inv Inventory, sources []FundingSource, g *Guard) (Plan, error) {
	if err := validateRequired(req); err != nil {
		return Plan{}, err
	}
	if err := g.CheckPrepare(req, inv); err != nil {
		return Plan{}, err
	}
	agent := strings.TrimSpace(inv.AgentAddress)
	if agent == "" {
		return Plan{}, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: agent_address required to plan payment funding (agent_self recipient)")
	}
	if payTo := strings.TrimSpace(req.PayTo); payTo != "" && addrEqual(agent, payTo, req.ChainCAIP2) {
		return Plan{}, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: agent_address must not equal pay_to (anti–confused-deputy)")
	}

	gwOK, _ := corridorEligible(req.ChainCAIP2)
	if !gwOK {
		base := dryBase(req, inv)
		base.Action = ActionCorridorUnsupported
		base.Reason = "corridor_unsupported for dest chain (circle_gateway required for payment funding)"
		return base, nil
	}

	// Normalize + validate sources; sum reals must equal payment real (scenario bookkeeping).
	norm, sumReal, err := normalizeFundingSources(sources)
	if err != nil {
		return Plan{}, err
	}
	if len(norm) == 0 {
		return Plan{}, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: payment funding requires at least one positive source")
	}
	if !sumReal.Equal(req.AmountAtomic) {
		return Plan{}, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: sum(source_real) must equal payment_real for scenario full-funding")
	}

	steps := make([]PlanStep, 0, len(norm))
	for _, src := range norm {
		asset := stepAssetForChain("", src.ChainCAIP2, req.Asset)
		if u, ok := DefaultUSDC(src.ChainCAIP2); ok {
			asset = u
		}
		if _, ok := GatewayWalletAddress(src.ChainCAIP2); !ok {
			return Plan{}, liqerr.New(liqerr.CodeInvalidQuery,
				"liquidity: no gateway wallet for source chain %q", src.ChainCAIP2)
		}
		steps = append(steps, PlanStep{
			Kind:                StepKindCircleGatewayDeposit,
			FromChainCAIP2:      src.ChainCAIP2,
			Asset:               asset,
			AmountAtomic:        src.AmountAtomic,
			AmountLogicalAtomic: src.AmountLogicalAtomic,
			ScaleFactor:         req.ScaleFactor,
			Recipient:           agent,
			RecipientRole:       RecipientRoleAgentSelf,
		})
	}

	base := dryBase(req, inv)
	base.Action = ActionCircleGatewayDeposit
	base.RecipientRole = RecipientRoleAgentSelf
	base.Reason = "scenario Phase A: deposit hard-coded source reals into circle_gateway (agent_self); wait finality then Phase B withdraw"
	base.Steps = steps

	if err := attachDepositPrepareCallsOnPlan(&base); err != nil {
		return Plan{}, err
	}
	if err := g.CheckPlan(base); err != nil {
		return Plan{}, err
	}
	return base, nil
}

func normalizeFundingSources(sources []FundingSource) ([]FundingSource, decimal.Decimal, error) {
	type acc struct {
		chain   string
		real    decimal.Decimal
		logical decimal.Decimal
	}
	byKey := map[string]*acc{}
	for i, s := range sources {
		chain := strings.TrimSpace(s.ChainCAIP2)
		if chain == "" {
			return nil, decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
				"liquidity: funding sources[%d] missing chain_caip2", i)
		}
		info, ok := LookupChain(chain)
		if !ok || !info.GatewayOK {
			return nil, decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
				"liquidity: funding source chain %q is not GatewayOK in registry", chain)
		}
		if !s.AmountAtomic.IsPositive() {
			continue
		}
		if !s.AmountAtomic.Equal(s.AmountAtomic.Truncate(0)) {
			return nil, decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
				"liquidity: funding source amount must be whole atomic units")
		}
		key := strings.ToLower(info.CAIP2)
		if cur, ok := byKey[key]; ok {
			cur.real = cur.real.Add(s.AmountAtomic)
			if s.AmountLogicalAtomic.IsPositive() {
				cur.logical = cur.logical.Add(s.AmountLogicalAtomic)
			}
			continue
		}
		byKey[key] = &acc{
			chain:   info.CAIP2,
			real:    s.AmountAtomic,
			logical: s.AmountLogicalAtomic,
		}
	}
	if len(byKey) == 0 {
		return nil, decimal.Zero, nil
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]FundingSource, 0, len(keys))
	sum := decimal.Zero
	for _, k := range keys {
		a := byKey[k]
		out = append(out, FundingSource{
			ChainCAIP2:          a.chain,
			AmountAtomic:        a.real,
			AmountLogicalAtomic: a.logical,
		})
		sum = sum.Add(a.real)
	}
	return out, sum, nil
}