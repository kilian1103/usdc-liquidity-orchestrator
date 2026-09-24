// Package liquidity plans multi-chain USDC rebalancing for agentic commerce.
//
// Non-custodial: fund-movement steps always target the agent's own address
// (agent_self). Merchant pay_to is untrusted claim metadata for a later
// Payment-Signature — never a bridge/mint destination.
//
// Rails (naming avoids "gateway" alone to prevent MoR confusion):
//
//	Phase A: circle_gateway_deposit | circle_gateway_consolidate
//	Phase B: circle_gateway_withdraw | cctp_fast
//
// Never emit deposit+withdraw in one plan (no circle_gateway_deposit_withdraw).
package liquidity

import (
	"strings"

	"github.com/shopspring/decimal"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/types"
)

// PlanAction is the dry liquidity plan outcome.
type PlanAction string

const (
	ActionNoop                     PlanAction = "noop"
	ActionCircleGatewayWithdraw    PlanAction = "circle_gateway_withdraw"
	ActionCircleGatewayConsolidate PlanAction = "circle_gateway_consolidate"
	ActionCircleGatewayDeposit     PlanAction = "circle_gateway_deposit"
	ActionCCTPFast                 PlanAction = "cctp_fast"
	ActionInsufficient             PlanAction = "insufficient"
	ActionCorridorUnsupported      PlanAction = "corridor_unsupported"
)

const (
	LocationNative            = "native"
	LocationCircleGateway     = "circle_gateway"
	RecipientRoleAgentSelf    = "agent_self"
	RecipientRoleMerchant     = "merchant"
	RecipientRoleOrchestrator = "orchestrator"
	AmountSourceProbe         = "probe"
	AmountSourceOverride      = "override"
	AmountSourceSelf          = "self"

	StepKindCircleGatewayWithdraw = "circle_gateway_withdraw"
	StepKindCircleGatewayDeposit  = "circle_gateway_deposit"
	StepKindCCTPBurn              = "cctp_burn"
	StepKindCCTPMint              = "cctp_mint"
	StepKindNote                  = "note"
	StepKindOrchestratorFee       = "orchestrator_fee"

	PreferRailAuto          = "auto"
	PreferRailCircleGateway = "circle_gateway"
	PreferRailCCTPFast      = "cctp_fast"
	SettleViaX402           = "x402"
)

// Required is the dest-chain USDC need (+ optional amount override rules).
// AmountAtomic is always the real on-chain amount used for planning.
// AmountLogicalAtomic + ScaleFactor are optional scenario wire stamps.
type Required struct {
	Protocol            string
	ChainCAIP2          string
	Asset               string
	PayTo               string
	AmountAtomic        decimal.Decimal
	AmountSource        string
	AmountLogicalAtomic decimal.Decimal
	ScaleFactor         int64
}

// Balance is one asserted inventory row.
type Balance struct {
	ChainCAIP2   string
	Asset        string
	AmountAtomic decimal.Decimal
	Location     string
}

// Inventory is agent-supplied balances (client-side only).
type Inventory struct {
	AgentAddress string
	Balances     []Balance
}

// Orchestration is optional agent setup for target + source allowlist + rail preference.
type Orchestration struct {
	TargetChainCAIP2   string
	SourceChainCAIP2s  []string
	AllowCircleGateway *bool // nil = true
	PreferRail         string
}

// FeeConfig is optional kaimo orchestration fee (post-prepare settle; not a fund rail).
// Fee chain/asset always follow the plan Required claim.
type FeeConfig struct {
	Bps       int64
	Recipient string
	SettleVia string // empty → x402
}

// PlanFee is fee metadata attached to a fund-moving plan.
type PlanFee struct {
	Bps           int64
	AmountAtomic  decimal.Decimal
	Recipient     string
	RecipientRole string
	SettleVia     string
	ChainCAIP2    string
	Asset         string
}

// PlanStep is one planned fund movement (agent_self recipients only for fund rails).
// AmountAtomic is always real; AmountLogicalAtomic + ScaleFactor are optional scenario stamps.
type PlanStep struct {
	Kind                string
	FromChainCAIP2      string
	ToChainCAIP2        string
	Asset               string
	AmountAtomic        decimal.Decimal
	AmountLogicalAtomic decimal.Decimal
	ScaleFactor         int64
	Recipient           string
	RecipientRole       string
	PrepareCalls        []PrepareCall
}

// Plan is the pure planner output (Executed always false from PlanLiquidity).
type Plan struct {
	Action              PlanAction
	Required            Required
	Steps               []PlanStep
	Reason              string
	RecipientRole       string
	InventoryAsserted   bool
	InventoryUnverified bool
	Executed            bool
	DryRun              bool
	Fee                 *PlanFee
	agentAddress        string // agent_self binding; never bootstrap from steps in CheckPlan
}

// BindAgent sets the agent_self identity for CheckPlan / execute.
// Fund-moving plans require a non-empty agent; CheckPlan refuses step-recipient bootstrap.
func (p *Plan) BindAgent(addr string) {
	if p == nil {
		return
	}
	p.agentAddress = strings.TrimSpace(addr)
}

// AgentAddress returns the bound agent_self address (empty if unset).
func (p Plan) AgentAddress() string {
	return p.agentAddress
}

// RequiredFromWire builds Required from land/claim wire.
// pay_to is optional residual metadata only (never a fund destination).
// amountOverride is accepted only when probe amount is empty.
func RequiredFromWire(lr *types.Required, amountOverride string) (Required, error) {
	if lr == nil {
		return Required{}, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: missing required (chain_caip2, asset, amount_atomic)")
	}
	chain := strings.TrimSpace(lr.ChainCAIP2)
	asset := strings.TrimSpace(lr.Asset)
	amount, source, err := resolveAmount(strings.TrimSpace(lr.AmountAtomic), strings.TrimSpace(amountOverride))
	if err != nil {
		return Required{}, err
	}
	if chain == "" || asset == "" {
		return Required{}, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: chain_caip2 and asset required (cannot invent; override only fills missing amount)")
	}
	if !amount.IsPositive() {
		return Required{}, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: amount_atomic must be positive")
	}
	return Required{
		Protocol:     strings.TrimSpace(lr.Protocol),
		ChainCAIP2:   chain,
		Asset:        asset,
		PayTo:        strings.TrimSpace(lr.PayTo),
		AmountAtomic: amount,
		AmountSource: source,
	}, nil
}

func resolveAmount(probeAmt, override string) (decimal.Decimal, string, error) {
	switch {
	case override != "" && probeAmt != "":
		o, err := parseAtomic(override)
		if err != nil {
			return decimal.Zero, "", err
		}
		p, err := parseAtomic(probeAmt)
		if err != nil {
			return decimal.Zero, "", err
		}
		if !o.Equal(p) {
			return decimal.Zero, "", liqerr.New(liqerr.CodeInvalidQuery,
				"liquidity: amount_override refused — probe already has amount %s (cannot change)", probeAmt)
		}
		return p, AmountSourceProbe, nil
	case override != "" && probeAmt == "":
		o, err := parseAtomic(override)
		if err != nil {
			return decimal.Zero, "", err
		}
		return o, AmountSourceOverride, nil
	case probeAmt != "":
		p, err := parseAtomic(probeAmt)
		if err != nil {
			return decimal.Zero, "", err
		}
		return p, AmountSourceProbe, nil
	default:
		return decimal.Zero, "", liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: amount missing on probe and no amount_override — cannot plan")
	}
}

func parseAtomic(s string) (decimal.Decimal, error) {
	d, err := decimal.NewFromString(strings.TrimSpace(s))
	if err != nil {
		return decimal.Zero, liqerr.Wrap(liqerr.CodeInvalidQuery, err,
			"liquidity: amount_atomic %q is not a decimal", s)
	}
	if !d.Equal(d.Truncate(0)) {
		return decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: amount_atomic %q must be whole atomic units (no fractional)", s)
	}
	return d.Truncate(0), nil
}

// PlanLiquidity returns a pure dry plan. Never moves funds.
// Fund steps always target Inventory.AgentAddress with recipient_role=agent_self.
func PlanLiquidity(req Required, inv Inventory, g *Guard) (Plan, error) {
	return PlanOrchestration(req, inv, nil, nil, g)
}

// PlanOrchestration plans Phase B shortfall land on dest agent_self (optional fee).
// pay_to is residual metadata only; fund rails always mint/withdraw to agent_self.
// Never emits deposit steps — deposit first via PlanGatewayDeposit / PlanConsolidate.
func PlanOrchestration(req Required, inv Inventory, o *Orchestration, fee *FeeConfig, g *Guard) (Plan, error) {
	if err := validateRequired(req); err != nil {
		return Plan{}, err
	}
	if err := validateOrchestration(req, o); err != nil {
		return Plan{}, err
	}
	if err := validateFeeConfig(fee); err != nil {
		return Plan{}, err
	}
	if err := g.CheckPrepare(req, inv); err != nil {
		return Plan{}, err
	}
	agent, err := requireAgentSelf(req, inv)
	if err != nil {
		return Plan{}, err
	}

	base := dryBase(req, inv)
	base.RecipientRole = RecipientRoleAgentSelf
	if hasNativeCover(req, inv) {
		base.Action = ActionNoop
		base.Reason = "dest native balance covers required amount"
		return base, nil
	}
	if early, ok := unsupportedOrEarly(req, base); ok {
		return early, nil
	}

	// Only move the shortfall (required − dest native). Full required amount would
	// reject valid fragmented inventory (e.g. 20 Base + 30 Arb for 42 on Base).
	shortfall := req.AmountAtomic.Sub(sumMatchingNative(req, inv, req.ChainCAIP2))
	if !shortfall.IsPositive() {
		base.Action = ActionNoop
		base.Reason = "dest native balance covers required amount"
		return base, nil
	}

	gwOK, cctpOK, prefer, sources := phaseBOptions(req, o)
	if p, ok, err := tryBridgePlans(req, inv, agent, shortfall, gwOK, cctpOK, prefer, sources, fee, g, base); ok || err != nil {
		return p, err
	}
	return phaseBUncovered(base, req, inv, shortfall, sources, gwOK, cctpOK), nil
}

func requireAgentSelf(req Required, inv Inventory) (string, error) {
	agent := strings.TrimSpace(inv.AgentAddress)
	if agent == "" {
		return "", liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: agent_address required to plan fund movement (agent_self recipient)")
	}
	if payTo := strings.TrimSpace(req.PayTo); payTo != "" && addrEqual(agent, payTo, req.ChainCAIP2) {
		return "", liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: agent_address must not equal pay_to (anti–confused-deputy)")
	}
	return agent, nil
}

func phaseBOptions(req Required, o *Orchestration) (gwOK, cctpOK bool, prefer string, sources []string) {
	gwOK, cctpOK = corridorEligible(req.ChainCAIP2)
	if o != nil && !o.gatewayAllowed() {
		gwOK = false
	}
	prefer = PreferRailAuto
	if o != nil && strings.TrimSpace(o.PreferRail) != "" {
		prefer = strings.ToLower(strings.TrimSpace(o.PreferRail))
	}
	return gwOK, cctpOK, prefer, sourceAllowlist(o)
}

func phaseBUncovered(base Plan, req Required, inv Inventory, shortfall decimal.Decimal, sources []string, gwOK, cctpOK bool) Plan {
	if !gwOK && !cctpOK {
		base.Action = ActionCorridorUnsupported
		base.Reason = "no circle_gateway/cctp corridor for dest and dest native insufficient"
		return base
	}
	base.Action = ActionInsufficient
	if _, hasOther := findOtherNativeSource(req, inv, shortfall, sources); hasOther {
		base.Reason = insufficientDepositFirstReason
	} else {
		base.Reason = "insufficient_liquidity: no dest native, circle_gateway, or cross-chain source covers shortfall"
	}
	return base
}

func (o *Orchestration) gatewayAllowed() bool {
	if o == nil || o.AllowCircleGateway == nil {
		return true
	}
	return *o.AllowCircleGateway
}

func sourceAllowlist(o *Orchestration) []string {
	if o == nil || len(o.SourceChainCAIP2s) == 0 {
		return nil
	}
	out := make([]string, 0, len(o.SourceChainCAIP2s))
	for _, s := range o.SourceChainCAIP2s {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func validateOrchestration(req Required, o *Orchestration) error {
	if o == nil {
		return nil
	}
	if t := strings.TrimSpace(o.TargetChainCAIP2); t != "" {
		if !strings.EqualFold(t, req.ChainCAIP2) {
			return liqerr.New(liqerr.CodeInvalidQuery,
				"liquidity: orchestration.target_chain_caip2 %q must equal required.chain_caip2 %q",
				t, req.ChainCAIP2)
		}
	}
	prefer := strings.ToLower(strings.TrimSpace(o.PreferRail))
	switch prefer {
	case "", PreferRailAuto, PreferRailCircleGateway, PreferRailCCTPFast:
	default:
		return liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: prefer_rail %q invalid (auto|circle_gateway|cctp_fast)", o.PreferRail)
	}
	return nil
}

func validateFeeConfig(fee *FeeConfig) error {
	if fee == nil || fee.Bps == 0 {
		return nil
	}
	if fee.Bps < 0 {
		return liqerr.New(liqerr.CodeInvalidQuery, "liquidity: fee_bps must be non-negative")
	}
	// Cap at 100% (10000 bps) — plan metadata only, but bounds agent-steering risk.
	if fee.Bps > 10000 {
		return liqerr.New(liqerr.CodeInvalidQuery, "liquidity: fee_bps must be <= 10000")
	}
	if strings.TrimSpace(fee.Recipient) == "" {
		return liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: fee_recipient required when fee_bps > 0")
	}
	return nil
}

func validateRequired(req Required) error {
	if strings.TrimSpace(req.ChainCAIP2) == "" || strings.TrimSpace(req.Asset) == "" {
		return liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: chain_caip2 and asset required")
	}
	if !req.AmountAtomic.IsPositive() {
		return liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: amount_atomic must be positive")
	}
	return nil
}

func dryBase(req Required, inv Inventory) Plan {
	return Plan{
		Required:            req,
		InventoryAsserted:   true,
		InventoryUnverified: true,
		Executed:            false,
		DryRun:              true,
		agentAddress:        strings.TrimSpace(inv.AgentAddress),
	}
}

func unsupportedOrEarly(req Required, base Plan) (Plan, bool) {
	gwOK, cctpOK := corridorEligible(req.ChainCAIP2)
	if chainIsSolana(req.ChainCAIP2) || (!chainIsEVM(req.ChainCAIP2) && !gwOK && !cctpOK) {
		base.Action = ActionCorridorUnsupported
		base.Reason = "corridor_unsupported for dest chain (EVM-first; Solana/unknown not planned this cut)"
		return base, true
	}
	if chainIsEVM(req.ChainCAIP2) && !gwOK && !cctpOK {
		// Unknown EVM dest (not in registry).
		base.Action = ActionCorridorUnsupported
		base.Reason = "corridor_unsupported for dest chain (not in registry)"
		return base, true
	}
	return base, false
}

// insufficientDepositFirstReason guides agents when other-chain native exists but Phase B
// cannot complete without a prior Phase A deposit + Gateway finality wait.
const insufficientDepositFirstReason = "insufficient_liquidity: no circle_gateway balance or cctp path covers shortfall; deposit native into circle_gateway (Phase A), wait finality (~13-19m), then plan withdraw (Phase B)"

func tryBridgePlans(
	req Required, inv Inventory, agent string, shortfall decimal.Decimal,
	gwOK, cctpOK bool, prefer string, sources []string, fee *FeeConfig, g *Guard, base Plan,
) (Plan, bool, error) {
	tryGW := func() (Plan, bool, error) {
		if !gwOK {
			return base, false, nil
		}
		// Phase B: withdraw only when gateway balance already covers shortfall.
		// Never auto-insert deposit steps (no deposit_withdraw composite).
		if locationSumGateway(req, inv).GreaterThanOrEqual(shortfall) {
			base.Action = ActionCircleGatewayWithdraw
			base.RecipientRole = RecipientRoleAgentSelf
			base.Reason = "circle_gateway balance covers shortfall; withdraw to agent_self on dest"
			base.Steps = []PlanStep{{
				Kind: StepKindCircleGatewayWithdraw, ToChainCAIP2: req.ChainCAIP2, Asset: req.Asset,
				AmountAtomic: shortfall, Recipient: agent, RecipientRole: RecipientRoleAgentSelf,
			}}
			return finalizeFundPlan(base, shortfall, fee, g)
		}
		return base, false, nil
	}

	tryCCTP := func() (Plan, bool, error) {
		if !cctpOK {
			return base, false, nil
		}
		if src, ok := findOtherNativeSource(req, inv, shortfall, sources); ok {
			srcAsset := stepAssetForChain(src.Asset, src.ChainCAIP2, req.Asset)
			base.Action = ActionCCTPFast
			base.RecipientRole = RecipientRoleAgentSelf
			base.Reason = "cctp_fast burn shortfall on source, mint to agent_self on dest"
			base.Steps = []PlanStep{
				{Kind: StepKindCCTPBurn, FromChainCAIP2: src.ChainCAIP2, ToChainCAIP2: req.ChainCAIP2, Asset: srcAsset,
					AmountAtomic: shortfall, Recipient: agent, RecipientRole: RecipientRoleAgentSelf},
				{Kind: StepKindCCTPMint, ToChainCAIP2: req.ChainCAIP2, Asset: req.Asset,
					AmountAtomic: shortfall, Recipient: agent, RecipientRole: RecipientRoleAgentSelf},
			}
			return finalizeFundPlan(base, shortfall, fee, g)
		}
		return base, false, nil
	}

	order := []func() (Plan, bool, error){tryGW, tryCCTP}
	if prefer == PreferRailCCTPFast {
		order = []func() (Plan, bool, error){tryCCTP, tryGW}
	}
	for _, fn := range order {
		if p, ok, err := fn(); ok || err != nil {
			return p, ok, err
		}
	}
	return base, false, nil
}

func finalizeFundPlan(p Plan, shortfall decimal.Decimal, fee *FeeConfig, g *Guard) (Plan, bool, error) {
	attachFee(&p, shortfall, fee)
	if err := attachDepositPrepareCallsOnPlan(&p); err != nil {
		return Plan{}, true, err
	}
	if err := g.CheckPlan(p); err != nil {
		return Plan{}, true, err
	}
	return p, true, nil
}

func attachFee(p *Plan, shortfall decimal.Decimal, fee *FeeConfig) {
	if fee == nil || fee.Bps <= 0 || strings.TrimSpace(fee.Recipient) == "" {
		return
	}
	if !isFundMovingAction(p.Action) {
		return
	}
	amt := computeFeeAtomic(shortfall, fee.Bps)
	if !amt.IsPositive() {
		return
	}
	settle := strings.TrimSpace(fee.SettleVia)
	if settle == "" {
		settle = SettleViaX402
	}
	// Fee lives only on plan.fee (envelope metadata for post-prepare x402 settle).
	// Never append orchestrator_fee to steps[] — naive agents must not auto-execute it as a transfer.
	p.Fee = &PlanFee{
		Bps:           fee.Bps,
		AmountAtomic:  amt,
		Recipient:     strings.TrimSpace(fee.Recipient),
		RecipientRole: RecipientRoleOrchestrator,
		SettleVia:     settle,
		ChainCAIP2:    p.Required.ChainCAIP2,
		Asset:         p.Required.Asset,
	}
}

func isFundMovingAction(a PlanAction) bool {
	switch a {
	case ActionCircleGatewayWithdraw, ActionCircleGatewayDeposit, ActionCircleGatewayConsolidate, ActionCCTPFast:
		return true
	default:
		return false
	}
}

// computeFeeAtomic returns ceil(shortfall * bps / 10000) in whole atomic units.
func computeFeeAtomic(shortfall decimal.Decimal, bps int64) decimal.Decimal {
	if bps <= 0 || !shortfall.IsPositive() {
		return decimal.Zero
	}
	// Integer ceil: (shortfall*bps + 9999) / 10000
	num := shortfall.Mul(decimal.NewFromInt(bps)).Add(decimal.NewFromInt(9999))
	return num.Div(decimal.NewFromInt(10000)).Truncate(0)
}

func hasNativeCover(req Required, inv Inventory) bool {
	return sumMatchingNative(req, inv, req.ChainCAIP2).GreaterThanOrEqual(req.AmountAtomic)
}

// locationSumGateway sums circle_gateway USDC (unified balance; not chain-scoped).
func locationSumGateway(req Required, inv Inventory) decimal.Decimal {
	sum := decimal.Zero
	for _, b := range inv.Balances {
		if normalizeLoc(b.Location) != LocationCircleGateway {
			continue
		}
		if !gatewayAssetMatch(b.Asset, req.Asset) {
			continue
		}
		sum = sum.Add(b.AmountAtomic)
	}
	return sum
}

func gatewayAssetMatch(bAsset, reqAsset string) bool {
	bAsset = strings.TrimSpace(bAsset)
	// Empty asset is incomplete inventory — do not treat as USDC.
	if bAsset == "" {
		return false
	}
	if strings.EqualFold(bAsset, "USDC") {
		return true
	}
	if IsKnownUSDCAsset(bAsset) {
		return true
	}
	if IsKnownUSDCAsset(reqAsset) && assetEqual(bAsset, reqAsset, "eip155:1") {
		return true
	}
	return assetEqual(bAsset, reqAsset, "eip155:1")
}

func findOtherNativeSource(req Required, inv Inventory, need decimal.Decimal, sources []string) (Balance, bool) {
	for _, b := range inv.Balances {
		if normalizeLoc(b.Location) != LocationNative {
			continue
		}
		if strings.TrimSpace(b.ChainCAIP2) == "" || strings.EqualFold(b.ChainCAIP2, req.ChainCAIP2) {
			continue
		}
		if chainIsSolana(b.ChainCAIP2) {
			continue
		}
		if len(sources) > 0 && !chainInList(b.ChainCAIP2, sources) {
			continue
		}
		if !crossChainUSDCMatch(b.Asset, b.ChainCAIP2, req.Asset, req.ChainCAIP2) {
			continue
		}
		if b.AmountAtomic.GreaterThanOrEqual(need) {
			return b, true
		}
	}
	return Balance{}, false
}

func chainInList(chain string, list []string) bool {
	for _, c := range list {
		if strings.EqualFold(strings.TrimSpace(c), strings.TrimSpace(chain)) {
			return true
		}
	}
	return false
}

func crossChainUSDCMatch(srcAsset, srcChain, destAsset, destChain string) bool {
	if assetEqual(srcAsset, destAsset, destChain) {
		return true
	}
	// Per-chain registry USDC (real multi-chain inventories).
	if srcU, ok := DefaultUSDC(srcChain); ok && assetEqual(srcAsset, srcU, srcChain) {
		if destU, ok := DefaultUSDC(destChain); ok && assetEqual(destAsset, destU, destChain) {
			return true
		}
		if IsKnownUSDCAsset(destAsset) {
			return true
		}
	}
	if IsKnownUSDCAsset(srcAsset) && IsKnownUSDCAsset(destAsset) {
		return true
	}
	return false
}

func stepAssetForChain(rowAsset, chainCAIP2, fallback string) string {
	if strings.TrimSpace(rowAsset) != "" {
		return strings.TrimSpace(rowAsset)
	}
	if u, ok := DefaultUSDC(chainCAIP2); ok {
		return u
	}
	return fallback
}

func sumMatchingNative(req Required, inv Inventory, chain string) decimal.Decimal {
	sum := decimal.Zero
	for _, b := range inv.Balances {
		if normalizeLoc(b.Location) != LocationNative {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(b.ChainCAIP2), strings.TrimSpace(chain)) {
			continue
		}
		// Same-chain USDC equivalence (symbol "USDC" ↔ registry contract), aligned with
		// gateway/cross-chain matching so shortfall-only is not inflated.
		if !sameChainUSDCMatch(b.Asset, req.Asset, chain) {
			continue
		}
		sum = sum.Add(b.AmountAtomic)
	}
	return sum
}

// sameChainUSDCMatch treats registry USDC contract and symbol "USDC" as equivalent
// on a single chain (client-asserted inventory may use either form).
func sameChainUSDCMatch(rowAsset, reqAsset, chain string) bool {
	if assetEqual(rowAsset, reqAsset, chain) {
		return true
	}
	// Reuse cross-chain logic with both sides on the same CAIP-2.
	return crossChainUSDCMatch(rowAsset, chain, reqAsset, chain)
}

func normalizeLoc(loc string) string {
	loc = strings.TrimSpace(strings.ToLower(loc))
	if loc == "" {
		return LocationNative
	}
	// Bare "gateway" is forbidden (collides with HTTP gateway / MoR mental models).
	if loc == "gateway" {
		return "invalid_gateway_label"
	}
	return loc
}

func assetEqual(a, b, chainCAIP2 string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if strings.HasPrefix(chainCAIP2, "solana:") {
		return a == b
	}
	return strings.EqualFold(a, b)
}

// PlanToWire maps a Plan to the agent-facing types.Plan.
func PlanToWire(p Plan) types.Plan {
	steps := make([]types.PlanStep, 0, len(p.Steps))
	for _, s := range p.Steps {
		var calls []types.PrepareCall
		if len(s.PrepareCalls) > 0 {
			calls = make([]types.PrepareCall, 0, len(s.PrepareCalls))
			for _, c := range s.PrepareCalls {
				calls = append(calls, types.PrepareCall{
					ChainCAIP2:  c.ChainCAIP2,
					To:          c.To,
					Data:        c.Data,
					Value:       c.Value,
					Method:      c.Method,
					Description: c.Description,
				})
			}
		}
		stepWire := types.PlanStep{
			Kind:           s.Kind,
			FromChainCAIP2: s.FromChainCAIP2,
			ToChainCAIP2:   s.ToChainCAIP2,
			Asset:          s.Asset,
			AmountAtomic:   s.AmountAtomic.String(),
			Recipient:      s.Recipient,
			RecipientRole:  s.RecipientRole,
			PrepareCalls:   calls,
		}
		if s.AmountLogicalAtomic.IsPositive() {
			stepWire.AmountLogicalAtomic = s.AmountLogicalAtomic.String()
		}
		if s.ScaleFactor > 0 {
			stepWire.ScaleFactor = s.ScaleFactor
		}
		steps = append(steps, stepWire)
	}
	var reqWire *types.Required
	if p.Required.ChainCAIP2 != "" || p.Required.AmountAtomic.IsPositive() {
		src := p.Required.AmountSource
		if src == "" {
			if p.Required.PayTo != "" {
				src = AmountSourceProbe
			} else {
				src = AmountSourceSelf
			}
		}
		reqWire = &types.Required{
			Protocol:     p.Required.Protocol,
			ChainCAIP2:   p.Required.ChainCAIP2,
			Asset:        p.Required.Asset,
			AmountAtomic: p.Required.AmountAtomic.String(),
			Source:       src,
		}
		// Residual pay_to is claim metadata only (never fund dest); omit when empty.
		if payTo := strings.TrimSpace(p.Required.PayTo); payTo != "" {
			reqWire.PayTo = payTo
			reqWire.PayToRole = RecipientRoleMerchant
		}
		if p.Required.AmountLogicalAtomic.IsPositive() {
			reqWire.AmountLogicalAtomic = p.Required.AmountLogicalAtomic.String()
		}
		if p.Required.ScaleFactor > 0 {
			reqWire.ScaleFactor = p.Required.ScaleFactor
		}
	}
	var feeWire *types.Fee
	if p.Fee != nil {
		feeWire = &types.Fee{
			Bps:           p.Fee.Bps,
			AmountAtomic:  p.Fee.AmountAtomic.String(),
			Recipient:     p.Fee.Recipient,
			RecipientRole: p.Fee.RecipientRole,
			SettleVia:     p.Fee.SettleVia,
			ChainCAIP2:    p.Fee.ChainCAIP2,
			Asset:         p.Fee.Asset,
		}
	}
	// Land/plan amount_source; omit for consolidate/deposit with empty Required.
	amountSrc := ""
	if reqWire != nil {
		amountSrc = reqWire.Source
	}
	return types.Plan{
		Action:              string(p.Action),
		Required:            reqWire,
		Steps:               steps,
		Reason:              p.Reason,
		RecipientRole:       p.RecipientRole,
		InventoryAsserted:   p.InventoryAsserted,
		InventoryUnverified: p.InventoryUnverified,
		Executed:            p.Executed,
		DryRun:              p.DryRun,
		AmountSource:        amountSrc,
		Fee:                 feeWire,
	}
}

// InventoryToWire maps planner Inventory to agent-facing types.Inventory.
func InventoryToWire(inv Inventory) types.Inventory {
	out := types.Inventory{AgentAddress: inv.AgentAddress}
	for _, b := range inv.Balances {
		out.Balances = append(out.Balances, types.Balance{
			ChainCAIP2:   b.ChainCAIP2,
			Asset:        b.Asset,
			AmountAtomic: b.AmountAtomic.String(),
			Location:     b.Location,
		})
	}
	return out
}

// InventoryFromWire parses wire inventory into planner Inventory.
// Non-positive balance amounts (zero or negative) are rejected as invalid_query.
func InventoryFromWire(inv types.Inventory) (Inventory, error) {
	out := Inventory{AgentAddress: strings.TrimSpace(inv.AgentAddress)}
	for i, b := range inv.Balances {
		amt, err := parseAtomic(b.AmountAtomic)
		if err != nil {
			return Inventory{}, liqerr.Wrap(liqerr.CodeInvalidQuery, err,
				"liquidity: balances[%d].amount_atomic", i)
		}
		if !amt.IsPositive() {
			return Inventory{}, liqerr.New(liqerr.CodeInvalidQuery,
				"liquidity: balances[%d].amount_atomic must be positive (got %s)", i, amt.String())
		}
		out.Balances = append(out.Balances, Balance{
			ChainCAIP2:   strings.TrimSpace(b.ChainCAIP2),
			Asset:        strings.TrimSpace(b.Asset),
			AmountAtomic: amt,
			Location:     b.Location,
		})
	}
	return out, nil
}

// OrchestrationFromWire maps wire orchestration options.
func OrchestrationFromWire(o *types.Orchestration) *Orchestration {
	if o == nil {
		return nil
	}
	return &Orchestration{
		TargetChainCAIP2:   strings.TrimSpace(o.TargetChainCAIP2),
		SourceChainCAIP2s:  append([]string(nil), o.SourceChainCAIP2s...),
		AllowCircleGateway: o.AllowCircleGateway,
		PreferRail:         strings.TrimSpace(o.PreferRail),
	}
}

// FeeConfigFromWire builds FeeConfig from request fee fields.
func FeeConfigFromWire(bps int64, recipient string) *FeeConfig {
	if bps == 0 && strings.TrimSpace(recipient) == "" {
		return nil
	}
	return &FeeConfig{
		Bps:       bps,
		Recipient: strings.TrimSpace(recipient),
		SettleVia: SettleViaX402,
	}
}

// FundingSourcesFromWire parses hard-coded payment-funding sources (real amounts).
// Optional amount_logical_atomic is stamp metadata only.
func FundingSourcesFromWire(src []types.FundingSource) ([]FundingSource, error) {
	if len(src) == 0 {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: payment funding requires sources[]")
	}
	out := make([]FundingSource, 0, len(src))
	for i, s := range src {
		chain := strings.TrimSpace(s.ChainCAIP2)
		if chain == "" {
			return nil, liqerr.New(liqerr.CodeInvalidQuery,
				"liquidity: sources[%d].chain_caip2 required", i)
		}
		real, err := parseAtomic(s.AmountAtomic)
		if err != nil {
			return nil, liqerr.Wrap(liqerr.CodeInvalidQuery, err,
				"liquidity: sources[%d].amount_atomic", i)
		}
		if !real.IsPositive() {
			return nil, liqerr.New(liqerr.CodeInvalidQuery,
				"liquidity: sources[%d].amount_atomic must be positive", i)
		}
		fs := FundingSource{ChainCAIP2: chain, AmountAtomic: real}
		if log := strings.TrimSpace(s.AmountLogicalAtomic); log != "" {
			logical, err := parseAtomic(log)
			if err != nil {
				return nil, liqerr.Wrap(liqerr.CodeInvalidQuery, err,
					"liquidity: sources[%d].amount_logical_atomic", i)
			}
			if logical.IsPositive() {
				fs.AmountLogicalAtomic = logical
			}
		}
		out = append(out, fs)
	}
	return out, nil
}