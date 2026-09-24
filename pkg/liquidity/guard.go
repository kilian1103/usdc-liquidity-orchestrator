package liquidity

import (
	"strings"

	"github.com/shopspring/decimal"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
)

// Guard governs client-side liquidity prepare/execute only.
// Prepare never transfers to merchant pay_to (residual claim metadata only).
type Guard struct {
	MaxAmountAtomic       decimal.Decimal
	AllowedAgentAddresses []string
}

// CheckPrepare validates required + inventory before planning.
func (g *Guard) CheckPrepare(req Required, inv Inventory) error {
	if g == nil {
		return nil
	}
	if g.MaxAmountAtomic.IsPositive() && req.AmountAtomic.GreaterThan(g.MaxAmountAtomic) {
		return liqerr.New(liqerr.CodeInsufficientLiquidity,
			"liquidity: required amount %s exceeds MaxAmountAtomic %s",
			req.AmountAtomic.String(), g.MaxAmountAtomic.String())
	}
	if len(g.AllowedAgentAddresses) == 0 {
		return nil
	}
	if strings.TrimSpace(inv.AgentAddress) == "" {
		return liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: agent_address required when AllowedAgentAddresses is set")
	}
	if !agentAddrAllowed(g.AllowedAgentAddresses, inv.AgentAddress, req.ChainCAIP2) {
		return liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: agent_address is not in AllowedAgentAddresses")
	}
	return nil
}

// CheckAgent validates inventory agent against AllowedAgentAddresses (no merchant claim).
func (g *Guard) CheckAgent(inv Inventory) error {
	if g == nil {
		return nil
	}
	if len(g.AllowedAgentAddresses) == 0 {
		return nil
	}
	if strings.TrimSpace(inv.AgentAddress) == "" {
		return liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: agent_address required when AllowedAgentAddresses is set")
	}
	// EVM-style comparison when no dest chain is known (consolidate).
	if !agentAddrAllowed(g.AllowedAgentAddresses, inv.AgentAddress, "eip155:1") {
		return liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: agent_address is not in AllowedAgentAddresses")
	}
	return nil
}

// CheckPlan refuses non-agent_self fund steps and pay_to-as-recipient.
// Nil receiver is safe (used by bare UnconfiguredExecutor{}).
//
// All fund-moving plans (deposit, withdraw, cctp) are agent_self-only.
// Empty pay_to is OK; residual non-empty pay_to is never a fund destination.
func (g *Guard) CheckPlan(p Plan) error {
	hasFund, err := planHasFundSteps(p.Steps)
	if err != nil {
		return err
	}
	agent := strings.TrimSpace(p.agentAddress)
	if err := checkFundPlanIdentity(hasFund, p.Required, agent); err != nil {
		return err
	}
	for _, s := range p.Steps {
		if err := checkPlanStep(s, p.Required, agent, g); err != nil {
			return err
		}
	}
	if err := checkPlanFee(p); err != nil {
		return err
	}
	return checkMaxAmounts(g, p)
}

func planHasFundSteps(steps []PlanStep) (bool, error) {
	hasFund := false
	for _, s := range steps {
		fund, err := classifyStep(s)
		if err != nil {
			return false, err
		}
		if fund {
			hasFund = true
		}
	}
	return hasFund, nil
}

func checkFundPlanIdentity(hasFund bool, req Required, agent string) error {
	if hasFund && agent == "" {
		return liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: agent_address required for fund-moving plan (refuse bootstrap from steps)")
	}
	if payTo := strings.TrimSpace(req.PayTo); payTo != "" && agent != "" &&
		addrEqual(agent, payTo, req.ChainCAIP2) {
		return liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: agent_address must not equal pay_to (anti–confused-deputy)")
	}
	return nil
}

func checkPlanStep(s PlanStep, req Required, agent string, g *Guard) error {
	fund, err := classifyStep(s)
	if err != nil {
		return err
	}
	if fund {
		return checkFundStep(s, req, agent, g)
	}
	return nil
}

func checkMaxAmounts(g *Guard, p Plan) error {
	if g == nil || !g.MaxAmountAtomic.IsPositive() {
		return nil
	}
	if p.Required.AmountAtomic.IsPositive() && p.Required.AmountAtomic.GreaterThan(g.MaxAmountAtomic) {
		return liqerr.New(liqerr.CodeInsufficientLiquidity,
			"liquidity: plan amount %s exceeds MaxAmountAtomic %s",
			p.Required.AmountAtomic.String(), g.MaxAmountAtomic.String())
	}
	for _, s := range p.Steps {
		fund, err := classifyStep(s)
		if err != nil {
			return err
		}
		if !fund {
			continue
		}
		if !s.AmountAtomic.IsPositive() {
			return liqerr.New(liqerr.CodeInvalidQuery,
				"liquidity: fund-moving step amount must be positive")
		}
		if s.AmountAtomic.GreaterThan(g.MaxAmountAtomic) {
			return liqerr.New(liqerr.CodeInsufficientLiquidity,
				"liquidity: step amount %s exceeds MaxAmountAtomic %s",
				s.AmountAtomic.String(), g.MaxAmountAtomic.String())
		}
	}
	return nil
}

// classifyStep returns whether the step is fund-moving, or an error for unknown kinds.
// orchestrator_fee is intentionally not a valid step kind (fee is plan.fee only).
func classifyStep(s PlanStep) (fundMoving bool, err error) {
	k := strings.ToLower(strings.TrimSpace(s.Kind))
	switch k {
	case StepKindCircleGatewayWithdraw, StepKindCircleGatewayDeposit, StepKindCCTPBurn, StepKindCCTPMint:
		return true, nil
	case StepKindNote:
		return false, nil
	case "":
		if strings.TrimSpace(s.Recipient) != "" {
			return false, liqerr.New(liqerr.CodeInvalidQuery,
				"liquidity: step missing kind but has recipient — refuse")
		}
		return false, nil
	default:
		return false, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: unknown step kind %q — refuse (allowlist only)", s.Kind)
	}
}

func checkFundStep(s PlanStep, req Required, agent string, g *Guard) error {
	if s.RecipientRole != RecipientRoleAgentSelf {
		return liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: fund-moving step kind %q must use recipient_role=agent_self, got %q",
			s.Kind, s.RecipientRole)
	}
	rec := strings.TrimSpace(s.Recipient)
	if rec == "" {
		return liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: fund-moving step missing recipient (agent_self required)")
	}
	// Always refuse residual merchant pay_to as fund dest when present.
	chain := req.ChainCAIP2
	if chain == "" {
		chain = s.FromChainCAIP2
	}
	if chain == "" {
		chain = s.ToChainCAIP2
	}
	if payTo := strings.TrimSpace(req.PayTo); payTo != "" && addrEqual(rec, payTo, chain) {
		return liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: refuse transfer to merchant pay_to — prepare funds agent_self only")
	}
	if agent == "" || !addrEqual(rec, agent, chain) {
		return liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: step recipient must equal agent_address")
	}
	if g != nil && len(g.AllowedAgentAddresses) > 0 && !agentAddrAllowed(g.AllowedAgentAddresses, rec, chain) {
		return liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: step recipient not in AllowedAgentAddresses")
	}
	return nil
}

func checkPlanFee(p Plan) error {
	if p.Fee == nil {
		return nil
	}
	rec := strings.TrimSpace(p.Fee.Recipient)
	if rec == "" {
		return liqerr.New(liqerr.CodeInvalidQuery, "liquidity: plan fee missing recipient")
	}
	if payTo := strings.TrimSpace(p.Required.PayTo); payTo != "" && addrEqual(rec, payTo, p.Required.ChainCAIP2) {
		return liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: fee recipient must not equal merchant pay_to")
	}
	return nil
}

func agentAddrAllowed(allowed []string, addr, chainCAIP2 string) bool {
	caseSensitive := strings.HasPrefix(chainCAIP2, "solana:")
	return addrInSet(allowed, addr, caseSensitive)
}

func addrEqual(a, b, chainCAIP2 string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if strings.HasPrefix(chainCAIP2, "solana:") {
		return a == b
	}
	if strings.HasPrefix(a, "0x") || strings.HasPrefix(b, "0x") {
		return strings.EqualFold(a, b)
	}
	return strings.EqualFold(a, b)
}

func addrInSet(allowed []string, addr string, caseSensitive bool) bool {
	addr = strings.TrimSpace(addr)
	for _, a := range allowed {
		a = strings.TrimSpace(a)
		if caseSensitive {
			if a == addr {
				return true
			}
			continue
		}
		if strings.EqualFold(a, addr) {
			return true
		}
	}
	return false
}