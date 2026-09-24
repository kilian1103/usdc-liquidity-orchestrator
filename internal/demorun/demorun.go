// Package demorun runs the worked CLI demo (scenario + shortfall + consolidate).
// Never logs keys, balances, prepare calldata, or RPC URLs.
package demorun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/inventory"
	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/rpcenv"
	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/scenario"
	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/execonchain"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
)

// Run executes the demo sequence writing JSON plans to stdout and notes to stderr.
// Returns a process exit code (0 success, 1 failure).
func Run(stdout, stderr io.Writer) int {
	ran, code := demoScenarioPlan(stdout, stderr)
	if code != 0 {
		return code
	}
	if ran {
		fmt.Fprint(stderr, "\n--- shortfall smoke ---\n\n")
	}
	if code := demoShortfallPlan(stdout, stderr); code != 0 {
		return code
	}
	fmt.Fprint(stderr, "\n--- consolidate ---\n\n")
	if code := demoConsolidate(stdout, stderr); code != 0 {
		return code
	}
	if os.Getenv("ENABLE_TESTNET_EXECUTE") == "1" {
		fmt.Fprint(stderr, "\n--- live testnet consolidate execute ---\n\n")
		demoLiveConsolidateExecute(stderr)
	}
	return 0
}

func demoScenarioPlan(stdout, stderr io.Writer) (bool, int) {
	if strings.TrimSpace(os.Getenv(scenario.EnvPaymentChain)) == "" &&
		strings.TrimSpace(os.Getenv(scenario.EnvPaymentAmountUSDC)) == "" {
		fmt.Fprintln(stderr, "# skip scenario plan: PAYMENT_CHAIN / PAYMENT_AMOUNT_USDC unset (copy .env.example → .env)")
		return false, 0
	}
	s, err := scenario.LoadFromEnv()
	if err != nil {
		fmt.Fprintf(stderr, "scenario plan error: %v\n", err)
		return true, 1
	}
	req := s.BuildRequired()
	inv, invNote, code := resolveScenarioInventory(s, stderr)
	if code != 0 {
		return true, code
	}
	if invNote != "" {
		fmt.Fprintln(stderr, invNote)
	}
	plan, err := liquidity.PlanPaymentFunding(req, inv, s.FundingSources(), nil)
	if err != nil {
		fmt.Fprintf(stderr, "payment funding plan error: %v\n", err)
		return true, 1
	}
	wire := liquidity.PlanToWire(plan)
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(wire)

	fmt.Fprintf(stderr, "\n# scenario action=%s dry_run=%v executed=%v scale=%d steps=%d\n",
		plan.Action, plan.DryRun, plan.Executed, s.ScaleFactor, len(plan.Steps))
	fmt.Fprintf(stderr, "# Phase A: deposit hard-coded sources into circle_gateway (agent_self only)\n")
	fmt.Fprintf(stderr, "# after deposit execute wait ~13-19m finality then Phase B withdraw separately\n")
	fmt.Fprintf(stderr, "# amount_atomic is REAL; amount_logical_atomic + scale_factor stamped when set\n")
	fmt.Fprintf(stderr, "# inventory_unverified=%v (live load never stamps verified)\n", plan.InventoryUnverified)
	return true, 0
}

func resolveScenarioInventory(s scenario.Scenario, stderr io.Writer) (liquidity.Inventory, string, int) {
	asserted := s.BuildAssertedInventory()
	agent := strings.TrimSpace(s.AgentAddress)
	if agent == "" {
		return asserted, "", 0
	}
	rpcs, err := rpcenv.LoadEVMTestnetExecuteRPCs()
	if err != nil || len(rpcs) == 0 {
		return asserted, "", 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	gw := strings.TrimSpace(os.Getenv("GATEWAY_API_BASE"))
	live, err := inventory.Load(ctx, inventory.Config{
		AgentAddress: agent,
		RPCs:         rpcs,
		GatewayAPI:   gw,
	})
	if err != nil {
		return asserted, "# live inventory unavailable; using asserted SOURCE_AMOUNT_* inventory", 0
	}
	if err := validateSourcesAgainstNative(s.FundingSources(), live); err != nil {
		fmt.Fprintf(stderr, "payment funding plan error: %v\n", err)
		return asserted, "", 1
	}
	return live, "# live inventory loaded (native + optional gateway); funding sources remain hard-coded reals", 0
}

func validateSourcesAgainstNative(sources []liquidity.FundingSource, inv liquidity.Inventory) error {
	for _, src := range sources {
		if !src.AmountAtomic.IsPositive() {
			continue
		}
		have := sumNativeOnChain(inv, src.ChainCAIP2)
		if src.AmountAtomic.GreaterThan(have) {
			return liqerr.New(liqerr.CodeInsufficientLiquidity,
				"scenario: source real exceeds live native balance on chain")
		}
	}
	return nil
}

func sumNativeOnChain(inv liquidity.Inventory, caip2 string) decimal.Decimal {
	sum := decimal.Zero
	for _, b := range inv.Balances {
		if !strings.EqualFold(strings.TrimSpace(b.Location), liquidity.LocationNative) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(b.ChainCAIP2), strings.TrimSpace(caip2)) {
			continue
		}
		sum = sum.Add(b.AmountAtomic)
	}
	return sum
}

func demoShortfallPlan(stdout, stderr io.Writer) int {
	const (
		arc     = "eip155:5042002"
		arcUSDC = "0x3600000000000000000000000000000000000000"
		agent   = "0xAgentSelf000000000000000000000000000001"
		feeTo   = "0xKaimoFee000000000000000000000000000001"
		need    = "42000000"
	)

	req := liquidity.Required{
		ChainCAIP2:   arc,
		Asset:        arcUSDC,
		AmountAtomic: decimal.RequireFromString(need),
		AmountSource: liquidity.AmountSourceSelf,
	}
	inv := liquidity.Inventory{
		AgentAddress: agent,
		Balances: []liquidity.Balance{
			{Asset: "USDC", AmountAtomic: decimal.RequireFromString("100000000"), Location: liquidity.LocationCircleGateway},
			{ChainCAIP2: arc, Asset: arcUSDC, AmountAtomic: decimal.RequireFromString("20000000"), Location: liquidity.LocationNative},
		},
	}
	orch := &liquidity.Orchestration{
		TargetChainCAIP2: arc,
		PreferRail:       liquidity.PreferRailCircleGateway,
	}
	fee := &liquidity.FeeConfig{
		Bps:       25,
		Recipient: feeTo,
		SettleVia: liquidity.SettleViaX402,
	}

	plan, err := liquidity.PlanOrchestration(req, inv, orch, fee, nil)
	if err != nil {
		fmt.Fprintf(stderr, "plan error: %v\n", err)
		return 1
	}
	wire := liquidity.PlanToWire(plan)
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(wire)

	fmt.Fprintf(stderr, "\n# action=%s dry_run=%v executed=%v\n", plan.Action, plan.DryRun, plan.Executed)
	fmt.Fprintf(stderr, "# Phase B: target=Arc Testnet (%s) source=circle_gateway shortfall=22 USDC → agent_self\n", arc)
	fmt.Fprintf(stderr, "# recipients on fund rails are always agent_self (%s)\n", agent)
	if plan.Fee != nil {
		fmt.Fprintf(stderr, "# kaimo fee %s bps amount_atomic=%s settle_via=%s → %s\n",
			fmt.Sprint(plan.Fee.Bps), plan.Fee.AmountAtomic.String(), plan.Fee.SettleVia, plan.Fee.Recipient)
	}
	return 0
}

func demoConsolidate(stdout, stderr io.Writer) int {
	const (
		arc         = "eip155:5042002"
		arcUSDC     = "0x3600000000000000000000000000000000000000"
		baseSep     = "eip155:84532"
		baseSepUSDC = "0x036CbD53842c5426634e7929541eC2318f3dCF7e"
		agent       = "0xAgentSelf000000000000000000000000000001"
	)
	inv := liquidity.Inventory{
		AgentAddress: agent,
		Balances: []liquidity.Balance{
			{ChainCAIP2: baseSep, Asset: baseSepUSDC, AmountAtomic: decimal.RequireFromString("3000000"), Location: liquidity.LocationNative},
			{ChainCAIP2: arc, Asset: arcUSDC, AmountAtomic: decimal.RequireFromString("1000000"), Location: liquidity.LocationNative},
		},
	}
	plan, err := liquidity.PlanConsolidate(inv, nil, nil)
	if err != nil {
		fmt.Fprintf(stderr, "consolidate error: %v\n", err)
		return 1
	}
	wire := liquidity.PlanToWire(plan)
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(wire)

	fmt.Fprintf(stderr, "\n# action=%s steps=%d dry_run=%v\n", plan.Action, len(plan.Steps), plan.DryRun)
	fmt.Fprintf(stderr, "# full-balance circle_gateway deposits; prepare_calls are unsigned (agent signs)\n")
	if len(plan.Steps) > 0 && len(plan.Steps[0].PrepareCalls) > 0 {
		fmt.Fprintf(stderr, "# first step prepare: approve → %s, deposit → gateway wallet\n",
			plan.Steps[0].PrepareCalls[0].To)
	}
	return 0
}

func demoLiveConsolidateExecute(stderr io.Writer) {
	key := strings.TrimSpace(os.Getenv("AGENT_PRIVATE_KEY"))
	if key == "" {
		fmt.Fprintln(stderr, "# skip live execute: AGENT_PRIVATE_KEY unset")
		return
	}
	rpcs, err := rpcenv.LoadEVMTestnetExecuteRPCs()
	if err != nil || len(rpcs) == 0 {
		fmt.Fprintln(stderr, "# skip live execute: no testnet EVM RPCs (RPC_URL_BASE_SEPOLIA / ARBITRUM_SEPOLIA / ARC_TESTNET, or eip155_*/JSON)")
		return
	}
	guard := &liquidity.Guard{}
	if raw := strings.TrimSpace(os.Getenv("MAX_AMOUNT_ATOMIC")); raw != "" {
		d, err := decimal.NewFromString(raw)
		if err != nil || !d.IsPositive() {
			fmt.Fprintln(stderr, "# skip live execute: MAX_AMOUNT_ATOMIC invalid")
			return
		}
		guard.MaxAmountAtomic = d
	}
	ex, err := execonchain.NewDepositExecutor(execonchain.Config{
		PrivateKeyHex: key,
		RPCs:          rpcs,
		Guard:         guard,
		WaitTimeout:   3 * time.Minute,
	})
	if err != nil {
		fmt.Fprintf(stderr, "# live execute configure error: %v\n", err)
		return
	}
	agent := ex.Address().Hex()
	amt := strings.TrimSpace(os.Getenv("DEMO_AMOUNT_ATOMIC"))
	if amt == "" {
		amt = "1"
	}
	var chain string
	for k := range rpcs {
		chain = k
		break
	}
	usdc, ok := liquidity.DefaultUSDC(chain)
	if !ok {
		fmt.Fprintln(stderr, "# skip live execute: no USDC for chain")
		return
	}
	inv := liquidity.Inventory{
		AgentAddress: agent,
		Balances: []liquidity.Balance{{
			ChainCAIP2: chain, Asset: usdc,
			AmountAtomic: decimal.RequireFromString(amt),
			Location:     liquidity.LocationNative,
		}},
	}
	plan, err := liquidity.PlanConsolidate(inv, nil, nil)
	if err != nil {
		fmt.Fprintf(stderr, "# live plan error: %v\n", err)
		return
	}
	if plan.Action != liquidity.ActionCircleGatewayConsolidate {
		fmt.Fprintf(stderr, "# live plan action=%s (need circle_gateway_consolidate)\n", plan.Action)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rcpt, err := ex.Execute(ctx, plan)
	if len(rcpt.TxHashes) > 0 {
		fmt.Fprintf(stderr, "# tx_hashes=%v\n", rcpt.TxHashes)
	}
	if err != nil {
		fmt.Fprintf(stderr, "# live execute error: %v\n", err)
		return
	}
	fmt.Fprintln(stderr, "# live execute ok")
}