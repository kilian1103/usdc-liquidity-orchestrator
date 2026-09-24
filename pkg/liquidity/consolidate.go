package liquidity

import (
	"sort"
	"strings"

	"github.com/shopspring/decimal"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
)

// PlanConsolidate plans multi-chain full-balance Circle Gateway deposits (no pay_to/fee).
// Eligible: native USDC on GatewayOK chains with a known Gateway Wallet.
// Optional source allowlist; AllowCircleGateway=false → invalid_query.
func PlanConsolidate(inv Inventory, o *Orchestration, g *Guard) (Plan, error) {
	agent, err := validateConsolidate(inv, o, g)
	if err != nil {
		return Plan{}, err
	}
	base := consolidateDryBase(agent)
	sums, assets, canonical := collectConsolidateSources(inv, sourceAllowlist(o))
	steps := buildConsolidateSteps(sums, assets, canonical, agent)
	if len(steps) == 0 {
		return consolidateNoop(base), nil
	}
	base.Action = ActionCircleGatewayConsolidate
	base.Reason = "deposit full native USDC balances into circle_gateway (agent_self)"
	base.Steps = steps
	if err := attachDepositPrepareCallsOnPlan(&base); err != nil {
		return Plan{}, err
	}
	if err := g.CheckPlan(base); err != nil {
		return Plan{}, err
	}
	return base, nil
}

func validateConsolidate(inv Inventory, o *Orchestration, g *Guard) (agent string, err error) {
	if o != nil && !o.gatewayAllowed() {
		return "", liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: allow_circle_gateway=false refuses consolidate")
	}
	agent = strings.TrimSpace(inv.AgentAddress)
	if agent == "" {
		return "", liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: agent_address required to plan consolidate (agent_self recipient)")
	}
	if err := g.CheckAgent(inv); err != nil {
		return "", err
	}
	return agent, nil
}

func consolidateDryBase(agent string) Plan {
	return Plan{
		InventoryAsserted:   true,
		InventoryUnverified: true,
		Executed:            false,
		DryRun:              true,
		agentAddress:        agent,
		RecipientRole:       RecipientRoleAgentSelf,
	}
}

func consolidateNoop(base Plan) Plan {
	base.Action = ActionNoop
	base.Reason = "noop: no eligible native USDC on GatewayOK chains to consolidate"
	return base
}

// collectConsolidateSources sums native USDC on GatewayOK chains (optional allowlist).
// Keys are lower-case CAIP-2; canonical holds registry form.
func collectConsolidateSources(inv Inventory, sources []string) (
	sums map[string]decimal.Decimal,
	assets map[string]string,
	canonical map[string]string,
) {
	sums = map[string]decimal.Decimal{}
	assets = map[string]string{}
	canonical = map[string]string{}
	for _, b := range inv.Balances {
		info, ok := eligibleConsolidateChain(b, sources)
		if !ok {
			continue
		}
		key := strings.ToLower(info.CAIP2)
		canonical[key] = info.CAIP2
		sums[key] = sums[key].Add(b.AmountAtomic)
		if _, set := assets[key]; !set {
			assets[key] = stepAssetForChain(b.Asset, info.CAIP2, info.USDC)
		}
	}
	return sums, assets, canonical
}

func eligibleConsolidateChain(b Balance, sources []string) (ChainInfo, bool) {
	if normalizeLoc(b.Location) != LocationNative {
		return ChainInfo{}, false
	}
	chainIn := strings.TrimSpace(b.ChainCAIP2)
	if chainIn == "" || !b.AmountAtomic.IsPositive() {
		return ChainInfo{}, false
	}
	info, ok := LookupChain(chainIn)
	if !ok || !info.GatewayOK {
		return ChainInfo{}, false
	}
	if _, ok := GatewayWalletAddress(info.CAIP2); !ok {
		return ChainInfo{}, false
	}
	if len(sources) > 0 && !chainInList(info.CAIP2, sources) {
		return ChainInfo{}, false
	}
	if !nativeUSDCOnChain(b.Asset, info.CAIP2) {
		return ChainInfo{}, false
	}
	return info, true
}

func buildConsolidateSteps(
	sums map[string]decimal.Decimal,
	assets, canonical map[string]string,
	agent string,
) []PlanStep {
	if len(sums) == 0 {
		return nil
	}
	keys := make([]string, 0, len(sums))
	for k := range sums {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	steps := make([]PlanStep, 0, len(keys))
	for _, k := range keys {
		amt := sums[k]
		if !amt.IsPositive() {
			continue
		}
		chain := canonical[k]
		steps = append(steps, PlanStep{
			Kind:           StepKindCircleGatewayDeposit,
			FromChainCAIP2: chain,
			Asset:          consolidateStepAsset(assets[k], chain),
			AmountAtomic:   amt,
			Recipient:      agent,
			RecipientRole:  RecipientRoleAgentSelf,
		})
	}
	return steps
}

func consolidateStepAsset(asset, chain string) string {
	if u, ok := DefaultUSDC(chain); ok && (asset == "" || strings.EqualFold(asset, "USDC")) {
		return u
	}
	return asset
}

func nativeUSDCOnChain(asset, chainCAIP2 string) bool {
	usdc, ok := DefaultUSDC(chainCAIP2)
	if !ok {
		return false
	}
	return sameChainUSDCMatch(asset, usdc, chainCAIP2)
}