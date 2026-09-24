// Package liqcli implements the usdc-liq dual-surface CLI (plan parity with HTTP).
// Never logs keys, agent addresses, balances, prepare calldata, or RPC URLs.
package liqcli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/demorun"
	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/execenv"
	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/inventory"
	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/planio"
	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/rpcenv"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/types"
)

// Version is reported by the version subcommand.
const Version = "0.1.0"

const maxBody = 1 << 20 // 1 MiB

// Main runs the CLI with the given argv (without program name), I/O streams.
// Exit codes: 0 success, 1 plan/execute failure, 2 usage.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		printUsage(stderr)
		return 2
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "plan":
		return runPlanCmd(rest, stdin, stdout, stderr)
	case "consolidate":
		return runConsolidateCmd(rest, stdin, stdout, stderr)
	case "deposit":
		return runDepositCmd(rest, stdin, stdout, stderr)
	case "move":
		return runMoveCmd(rest, stdin, stdout, stderr)
	case "payment-funding":
		return runPaymentFundingCmd(rest, stdin, stdout, stderr)
	case "chains":
		return runChainsCmd(rest, stdout, stderr)
	case "inventory":
		return runInventoryCmd(rest, stdout, stderr)
	case "demo":
		return demorun.Run(stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, Version)
		return 0
	case "help", "-h", "--help":
		printUsage(stderr)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", cmd)
		printUsage(stderr)
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `usdc-liq — non-custodial multi-chain USDC liquidity CLI

Usage:
  usdc-liq <command> [flags]

Phase A — fund circle_gateway (deposits only; no withdraw in same plan):
  deposit           fixed-N native → circle_gateway (single --source or multi --from)
  consolidate       full native balances → circle_gateway
  payment-funding   scenario multi-source fixed deposits (HTTP parity)

Phase B — land N on dest agent_self (withdraw or CCTP; never auto-deposit):
  plan              shortfall land (HTTP parity POST /v1/plan)
  move              same Phase B land (CLI-only; no fee)

Other:
  chains            registered corridors (GET /v1/chains)
  inventory         load live native + optional Gateway balances (needs agent + RPCs)
  demo              Phase A deposits + Phase B land + consolidate examples
  version           print version

Finality: after Phase A deposit execute, wait ~13–19m for Gateway finality before
Phase B withdraw can see the balance (Circle docs). Plan withdraw separately.

JSON mode (plan | consolidate | deposit | move | payment-funding):
  -f file           request JSON file (default "-" = stdin)
  --execute         request execute=true (dual-gate env when live)

Easy mode (plan | consolidate | deposit | move) — XOR with -f; incomplete → exit 2:
  --dest REF        dest domain id | name | CAIP-2 (plan/move)
  --source REF      single source chain (deposit; XOR --from)
  --from REF=USDC   multi fixed deposit per chain (repeatable; XOR --source/--amount/--amount-atomic)
  --sources REFS    comma-separated source chain refs (plan/move/allowlist)
  --amount USDC     human USDC (×10^6 atomic); XOR --amount-atomic
  --amount-atomic N atomic USDC string
  --balance REF=USDC  asserted native (repeatable; not with --live)
  --gateway-balance USDC  asserted circle_gateway unified balance
  --live            load inventory via RPCs (not with balances; testnet only)
  --agent 0x…       agent wallet (or derive from key / AGENT_ADDRESS)
  --private-key HEX never log; prefer env AGENT_PRIVATE_KEY (argv is visible)
  --rpc REF=URL     RPC overlay (repeatable); merges over env
  --mainnet         resolve domain ids as mainnet (default testnet)
  --execute         dual-gated live execute (ENABLE_TESTNET_EXECUTE=1)

Mint always lands on agent_self (never merchant pay_to). Exit: 0 success, 1 fail, 2 usage.
JSON always on stdout; notes on stderr (no secrets/balances/RPC).
`)
}

func runPlanCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file, execute := addSharedFlags(fs)
	easy := addEasyFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	mode, err := DetectPlanMode(fs)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err.Error())
		return 2
	}

	var req types.PlanRequest
	var keyHex string
	var rpcOverlay map[string]string

	switch mode {
	case ModeEasy:
		in := easyPlanFromFlags(easy, *execute)
		agent, key, idErr := ResolveAgentIdentity(
			in.Agent, in.PrivateKeyHex,
			os.Getenv("AGENT_ADDRESS"), os.Getenv("AGENT_PRIVATE_KEY"),
		)
		if idErr != nil {
			fmt.Fprintf(stderr, "%s\n", sanitizeIdentityErr(idErr))
			return 2
		}
		if vErr := ValidateEasyGates(in.EasyCommon, true); vErr != nil {
			fmt.Fprintf(stderr, "%s\n", vErr.Error())
			return 2
		}
		if vErr := ValidateEasyPlanRequired(in, agent); vErr != nil {
			fmt.Fprintf(stderr, "%s\n", vErr.Error())
			return 2
		}
		rpcOverlay, err = ParseRPCMap(in.RPCs, in.Testnet)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", sanitizeEasyErr(err))
			return 2
		}
		inv, invErr := loadEasyInventory(in.EasyCommon, agent, rpcOverlay)
		if invErr != nil {
			fmt.Fprintf(stderr, "%s\n", sanitizeEasyErr(invErr))
			return 1
		}
		req, err = BuildPlanRequestFromEasy(in, inv)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", sanitizeEasyErr(err))
			return 2
		}
		keyHex = key
	default:
		raw, rerr := readInput(*file, stdin)
		if rerr != nil {
			fmt.Fprintf(stderr, "read input: %v\n", rerr)
			return 1
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			fmt.Fprintf(stderr, "invalid JSON body\n")
			return 1
		}
		if *execute {
			req.Execute = true
		}
		// Runtime overrides apply to JSON mode execute as well.
		_, keyHex, _ = ResolveAgentIdentity(
			*easy.agent, *easy.privateKey,
			os.Getenv("AGENT_ADDRESS"), os.Getenv("AGENT_PRIVATE_KEY"),
		)
		rpcOverlay, _ = ParseRPCMap(sliceOf(easy.rpcs), !*easy.mainnet)
	}

	ex, code := resolveExecutor(req.Execute, execenv.Options{
		PrivateKeyHex: keyHex,
		RPCs:          rpcOverlay,
	}, stdout, stderr)
	if code != 0 {
		return code
	}
	resp, outcome := planio.RunPlan(context.Background(), ex, req)
	return writePlanResp(stdout, stderr, resp, outcome)
}

func runConsolidateCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("consolidate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file, execute := addSharedFlags(fs)
	easy := addEasyFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	mode, err := DetectPlanMode(fs)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err.Error())
		return 2
	}

	var req types.ConsolidateRequest
	var keyHex string
	var rpcOverlay map[string]string

	switch mode {
	case ModeEasy:
		in := easyConsolidateFromFlags(easy, *execute)
		agent, key, idErr := ResolveAgentIdentity(
			in.Agent, in.PrivateKeyHex,
			os.Getenv("AGENT_ADDRESS"), os.Getenv("AGENT_PRIVATE_KEY"),
		)
		if idErr != nil {
			fmt.Fprintf(stderr, "%s\n", sanitizeIdentityErr(idErr))
			return 2
		}
		if vErr := ValidateEasyGates(in.EasyCommon, false); vErr != nil {
			fmt.Fprintf(stderr, "%s\n", vErr.Error())
			return 2
		}
		if vErr := ValidateEasyConsolidateRequired(in, agent); vErr != nil {
			fmt.Fprintf(stderr, "%s\n", vErr.Error())
			return 2
		}
		rpcOverlay, err = ParseRPCMap(in.RPCs, in.Testnet)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", sanitizeEasyErr(err))
			return 2
		}
		inv, invErr := loadEasyInventory(in.EasyCommon, agent, rpcOverlay)
		if invErr != nil {
			fmt.Fprintf(stderr, "%s\n", sanitizeEasyErr(invErr))
			return 1
		}
		req, err = BuildConsolidateRequestFromEasy(in, inv)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", sanitizeEasyErr(err))
			return 2
		}
		keyHex = key
	default:
		raw, rerr := readInput(*file, stdin)
		if rerr != nil {
			fmt.Fprintf(stderr, "read input: %v\n", rerr)
			return 1
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			fmt.Fprintf(stderr, "invalid JSON body\n")
			return 1
		}
		if *execute {
			req.Execute = true
		}
		_, keyHex, _ = ResolveAgentIdentity(
			*easy.agent, *easy.privateKey,
			os.Getenv("AGENT_ADDRESS"), os.Getenv("AGENT_PRIVATE_KEY"),
		)
		rpcOverlay, _ = ParseRPCMap(sliceOf(easy.rpcs), !*easy.mainnet)
	}

	ex, code := resolveExecutor(req.Execute, execenv.Options{
		PrivateKeyHex: keyHex,
		RPCs:          rpcOverlay,
	}, stdout, stderr)
	if code != 0 {
		return code
	}
	resp, outcome := planio.RunConsolidate(context.Background(), ex, req)
	return writePlanResp(stdout, stderr, resp, outcome)
}

func runDepositCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("deposit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file, execute := addSharedFlags(fs)
	easy := addEasyFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	mode, err := DetectPlanMode(fs)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err.Error())
		return 2
	}

	var req types.DepositRequest
	var keyHex string
	var rpcOverlay map[string]string

	switch mode {
	case ModeEasy:
		in := easyDepositFromFlags(easy, *execute)
		agent, key, idErr := ResolveAgentIdentity(
			in.Agent, in.PrivateKeyHex,
			os.Getenv("AGENT_ADDRESS"), os.Getenv("AGENT_PRIVATE_KEY"),
		)
		if idErr != nil {
			fmt.Fprintf(stderr, "%s\n", sanitizeIdentityErr(idErr))
			return 2
		}
		if vErr := ValidateEasyGates(in.EasyCommon, false); vErr != nil {
			fmt.Fprintf(stderr, "%s\n", vErr.Error())
			return 2
		}
		if vErr := ValidateEasyDepositRequired(in, agent); vErr != nil {
			fmt.Fprintf(stderr, "%s\n", vErr.Error())
			return 2
		}
		rpcOverlay, err = ParseRPCMap(in.RPCs, in.Testnet)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", sanitizeEasyErr(err))
			return 2
		}
		inv, invErr := loadEasyInventory(in.EasyCommon, agent, rpcOverlay)
		if invErr != nil {
			fmt.Fprintf(stderr, "%s\n", sanitizeEasyErr(invErr))
			return 1
		}
		req, err = BuildDepositRequestFromEasy(in, inv)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", sanitizeEasyErr(err))
			return 2
		}
		keyHex = key
	default:
		raw, rerr := readInput(*file, stdin)
		if rerr != nil {
			fmt.Fprintf(stderr, "read input: %v\n", rerr)
			return 1
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			fmt.Fprintf(stderr, "invalid JSON body\n")
			return 1
		}
		if *execute {
			req.Execute = true
		}
		_, keyHex, _ = ResolveAgentIdentity(
			*easy.agent, *easy.privateKey,
			os.Getenv("AGENT_ADDRESS"), os.Getenv("AGENT_PRIVATE_KEY"),
		)
		rpcOverlay, _ = ParseRPCMap(sliceOf(easy.rpcs), !*easy.mainnet)
	}

	ex, code := resolveExecutor(req.Execute, execenv.Options{
		PrivateKeyHex: keyHex,
		RPCs:          rpcOverlay,
	}, stdout, stderr)
	if code != 0 {
		return code
	}
	resp, outcome := planio.RunDeposit(context.Background(), ex, req)
	return writePlanResp(stdout, stderr, resp, outcome)
}

func runMoveCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("move", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file, execute := addSharedFlags(fs)
	easy := addEasyFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	mode, err := DetectPlanMode(fs)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err.Error())
		return 2
	}

	var req types.MoveRequest
	var keyHex string
	var rpcOverlay map[string]string

	switch mode {
	case ModeEasy:
		in := easyMoveFromFlags(easy, *execute)
		agent, key, idErr := ResolveAgentIdentity(
			in.Agent, in.PrivateKeyHex,
			os.Getenv("AGENT_ADDRESS"), os.Getenv("AGENT_PRIVATE_KEY"),
		)
		if idErr != nil {
			fmt.Fprintf(stderr, "%s\n", sanitizeIdentityErr(idErr))
			return 2
		}
		if vErr := ValidateEasyGates(in.EasyCommon, false); vErr != nil {
			fmt.Fprintf(stderr, "%s\n", vErr.Error())
			return 2
		}
		if vErr := ValidateEasyMoveRequired(in, agent); vErr != nil {
			fmt.Fprintf(stderr, "%s\n", vErr.Error())
			return 2
		}
		rpcOverlay, err = ParseRPCMap(in.RPCs, in.Testnet)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", sanitizeEasyErr(err))
			return 2
		}
		inv, invErr := loadEasyInventory(in.EasyCommon, agent, rpcOverlay)
		if invErr != nil {
			fmt.Fprintf(stderr, "%s\n", sanitizeEasyErr(invErr))
			return 1
		}
		req, err = BuildMoveRequestFromEasy(in, inv)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", sanitizeEasyErr(err))
			return 2
		}
		keyHex = key
	default:
		raw, rerr := readInput(*file, stdin)
		if rerr != nil {
			fmt.Fprintf(stderr, "read input: %v\n", rerr)
			return 1
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			fmt.Fprintf(stderr, "invalid JSON body\n")
			return 1
		}
		if *execute {
			req.Execute = true
		}
		_, keyHex, _ = ResolveAgentIdentity(
			*easy.agent, *easy.privateKey,
			os.Getenv("AGENT_ADDRESS"), os.Getenv("AGENT_PRIVATE_KEY"),
		)
		rpcOverlay, _ = ParseRPCMap(sliceOf(easy.rpcs), !*easy.mainnet)
	}

	ex, code := resolveExecutor(req.Execute, execenv.Options{
		PrivateKeyHex: keyHex,
		RPCs:          rpcOverlay,
	}, stdout, stderr)
	if code != 0 {
		return code
	}
	resp, outcome := planio.RunMove(context.Background(), ex, req)
	return writePlanResp(stdout, stderr, resp, outcome)
}

func sliceOf(s *stringList) []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), (*s)...)
}

// easyFlagHolders holds pointers registered on a FlagSet for plan/consolidate/deposit/move.
type easyFlagHolders struct {
	dest, source, sources, amount, amountAtomic stringPtr
	gatewayBalance, agent, privateKey           stringPtr
	mainnet, live                               *bool
	balances, rpcs, froms                       *stringList
}

type stringPtr = *string

func addEasyFlags(fs *flag.FlagSet) easyFlagHolders {
	var bals, rpcs, froms stringList
	h := easyFlagHolders{
		dest:           fs.String("dest", "", "destination chain ref (domain|name|CAIP-2)"),
		source:         fs.String("source", "", "single source chain ref (deposit)"),
		sources:        fs.String("sources", "", "comma-separated source chain refs"),
		amount:         fs.String("amount", "", "human USDC amount"),
		amountAtomic:   fs.String("amount-atomic", "", "atomic USDC amount (XOR --amount)"),
		gatewayBalance: fs.String("gateway-balance", "", "asserted circle_gateway human USDC"),
		agent:          fs.String("agent", "", "agent wallet address"),
		privateKey:     fs.String("private-key", "", "hex ECDSA key (prefer AGENT_PRIVATE_KEY env)"),
		mainnet:        fs.Bool("mainnet", false, "resolve domain ids as mainnet (default testnet)"),
		live:           fs.Bool("live", false, "load live inventory (testnet RPCs; no asserted balances)"),
		balances:       &bals,
		rpcs:           &rpcs,
		froms:          &froms,
	}
	fs.Var(&bals, "balance", "asserted native balance ref=humanUSDC (repeatable)")
	fs.Var(&rpcs, "rpc", "RPC override ref=url (repeatable)")
	fs.Var(&froms, "from", "fixed deposit amount ref=humanUSDC (repeatable; multi deposit)")
	return h
}

func easyPlanFromFlags(h easyFlagHolders, execute bool) EasyPlanInput {
	return EasyPlanInput{
		EasyCommon:   easyCommonFromFlags(h, execute),
		Amount:       *h.amount,
		AmountAtomic: *h.amountAtomic,
	}
}

func easyConsolidateFromFlags(h easyFlagHolders, execute bool) EasyConsolidateInput {
	return EasyConsolidateInput{EasyCommon: easyCommonFromFlags(h, execute)}
}

func easyDepositFromFlags(h easyFlagHolders, execute bool) EasyDepositInput {
	return EasyDepositInput{
		EasyCommon:   easyCommonFromFlags(h, execute),
		Source:       *h.source,
		Amount:       *h.amount,
		AmountAtomic: *h.amountAtomic,
		From:         append([]string(nil), *h.froms...),
	}
}

func easyMoveFromFlags(h easyFlagHolders, execute bool) EasyMoveInput {
	return EasyMoveInput{
		EasyCommon:   easyCommonFromFlags(h, execute),
		Amount:       *h.amount,
		AmountAtomic: *h.amountAtomic,
	}
}

func easyCommonFromFlags(h easyFlagHolders, execute bool) EasyCommon {
	return EasyCommon{
		Agent:          *h.agent,
		PrivateKeyHex:  *h.privateKey,
		Testnet:        !*h.mainnet,
		Sources:        *h.sources,
		Dest:           *h.dest,
		Balances:       append([]string(nil), *h.balances...),
		GatewayBalance: *h.gatewayBalance,
		Live:           *h.live,
		RPCs:           append([]string(nil), *h.rpcs...),
		Execute:        execute,
	}
}

func loadEasyInventory(common EasyCommon, agent string, rpcOverlay map[string]string) (types.Inventory, error) {
	if common.Live {
		return loadLiveInventory(agent, rpcOverlay)
	}
	return BuildAssertedInventory(agent, common.Balances, common.GatewayBalance, common.Testnet)
}

func loadLiveInventory(agent string, rpcOverlay map[string]string) (types.Inventory, error) {
	base, err := rpcenv.LoadFromEnv()
	if err != nil {
		return types.Inventory{}, fmt.Errorf("inventory: RPC env invalid")
	}
	if base == nil {
		base = map[string]string{}
	}
	for k, v := range rpcOverlay {
		base[k] = v
	}
	// Prefer testnet executable corridors for live MVP inventory.
	rpcs := make(map[string]string, len(base))
	for caip, url := range base {
		if liquidity.IsTestnetExecutableChain(caip) {
			rpcs[caip] = url
		}
	}
	if len(rpcs) == 0 {
		return types.Inventory{}, fmt.Errorf("inventory: RPC map required for --live")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	gw := strings.TrimSpace(os.Getenv("GATEWAY_API_BASE"))
	inv, err := inventory.Load(ctx, inventory.Config{
		AgentAddress: agent,
		RPCs:         rpcs,
		GatewayAPI:   gw,
	})
	if err != nil {
		// Sanitized fixed message — no RPC / balance / address details.
		return types.Inventory{}, fmt.Errorf("inventory load failed")
	}
	return liquidity.InventoryToWire(inv), nil
}

func sanitizeIdentityErr(err error) string {
	if err == nil {
		return "identity error"
	}
	// liqerr messages are already fixed; never pass raw crypto errors through.
	msg := err.Error()
	if strings.Contains(strings.ToLower(msg), "private key") && strings.Contains(msg, "0x") {
		return "agent: private key invalid"
	}
	return msg
}

func sanitizeEasyErr(err error) string {
	if err == nil {
		return "invalid easy flags"
	}
	return err.Error()
}

func runPaymentFundingCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("payment-funding", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file, execute := addSharedFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	raw, err := readInput(*file, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "read input: %v\n", err)
		return 1
	}
	var req types.PaymentFundingRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		fmt.Fprintf(stderr, "invalid JSON body\n")
		return 1
	}
	if *execute {
		req.Execute = true
	}
	ex, code := resolveExecutor(req.Execute, execenv.Options{}, stdout, stderr)
	if code != 0 {
		return code
	}
	resp, outcome := planio.RunPaymentFunding(context.Background(), ex, req)
	return writePlanResp(stdout, stderr, resp, outcome)
}

func runChainsCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("chains", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return writeJSON(stdout, planio.ListChains())
}

func runInventoryCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("inventory", flag.ContinueOnError)
	fs.SetOutput(stderr)
	agent := fs.String("agent", "", "agent wallet address (or AGENT_ADDRESS env)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := ValidateInventoryArgs(*agent, os.Getenv("AGENT_ADDRESS"))
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err.Error())
		return 1
	}
	rpcs, err := rpcenv.LoadEVMTestnetExecuteRPCs()
	if err != nil {
		fmt.Fprintf(stderr, "inventory: RPC env invalid\n")
		return 1
	}
	if err := ValidateInventoryRPCs(rpcs); err != nil {
		fmt.Fprintf(stderr, "%s\n", err.Error())
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	gw := strings.TrimSpace(os.Getenv("GATEWAY_API_BASE"))
	inv, err := inventory.Load(ctx, inventory.Config{
		AgentAddress: cfg.AgentAddress,
		RPCs:         rpcs,
		GatewayAPI:   gw,
	})
	if err != nil {
		// Sanitized fixed message only — no RPC / balance details.
		fmt.Fprintf(stderr, "inventory load failed\n")
		return 1
	}
	wire := liquidity.InventoryToWire(inv)
	return writeJSON(stdout, wire)
}

// InventoryConfig is validated inventory CLI input (no live fields).
type InventoryConfig struct {
	AgentAddress string
}

// ValidateInventoryArgs checks agent address from flag or env (unit-testable, no network).
func ValidateInventoryArgs(flagAgent, envAgent string) (InventoryConfig, error) {
	agent := strings.TrimSpace(flagAgent)
	if agent == "" {
		agent = strings.TrimSpace(envAgent)
	}
	if agent == "" {
		return InventoryConfig{}, fmt.Errorf("inventory: agent address required (-agent or AGENT_ADDRESS)")
	}
	// Hex address shape check without importing eth packages in callers' tests.
	a := strings.TrimPrefix(strings.TrimPrefix(agent, "0x"), "0X")
	if len(a) != 40 {
		return InventoryConfig{}, fmt.Errorf("inventory: agent_address required (valid EVM hex)")
	}
	for _, c := range a {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return InventoryConfig{}, fmt.Errorf("inventory: agent_address required (valid EVM hex)")
		}
	}
	return InventoryConfig{AgentAddress: "0x" + a}, nil
}

// ValidateInventoryRPCs checks that at least one RPC is configured (no network).
func ValidateInventoryRPCs(rpcs map[string]string) error {
	if len(rpcs) == 0 {
		return fmt.Errorf("inventory: RPC map required (set RPC_URL_BASE_SEPOLIA / ARBITRUM_SEPOLIA / ARC_TESTNET or RPC_URLS_JSON)")
	}
	return nil
}

func addSharedFlags(fs *flag.FlagSet) (file *string, execute *bool) {
	file = fs.String("f", "-", "request JSON file (\"-\" = stdin)")
	execute = fs.Bool("execute", false, "execute plan when dual-gate env is configured")
	return file, execute
}

// resolveExecutor: dry always UnconfiguredExecutor; execute uses BuildExecutor (no loopback).
// On configure failure returns nil executor and exit 1 after writing sanitized JSON to stdout.
// opts.PrivateKeyHex / opts.RPCs overlay env for both JSON and easy modes.
func resolveExecutor(execute bool, opts execenv.Options, stdout, stderr io.Writer) (liquidity.Executor, int) {
	if !execute {
		return liquidity.UnconfiguredExecutor{}, 0
	}
	ex, err := execenv.BuildExecutor(opts)
	if err != nil {
		api := planio.SanitizeAPIError(err)
		if api == nil {
			api = &types.APIError{Code: "liquidity_rail_unavailable", Message: "execute failed"}
		}
		fmt.Fprintln(stderr, "# execute configure failed")
		_ = writeJSON(stdout, types.PlanResponse{Error: api})
		return nil, 1
	}
	return ex, 0
}

func writePlanResp(stdout, stderr io.Writer, resp types.PlanResponse, outcome planio.StampOutcome) int {
	if err := writeJSON(stdout, resp); err != 0 {
		return err
	}
	if outcome != planio.StampOK {
		if resp.Error != nil && resp.Error.Message != "" {
			fmt.Fprintf(stderr, "# %s: %s\n", resp.Error.Code, resp.Error.Message)
		} else {
			fmt.Fprintln(stderr, "# plan failed")
		}
	}
	return planio.ExitCode(outcome)
}

func writeJSON(w io.Writer, v any) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return 1
	}
	return 0
}

func readInput(path string, stdin io.Reader) ([]byte, error) {
	var r io.Reader
	var closer io.Closer
	if path == "" || path == "-" {
		r = stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		closer = f
		r = f
	}
	if closer != nil {
		defer closer.Close()
	}
	limited := io.LimitReader(r, maxBody+1)
	b, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(b) > maxBody {
		return nil, fmt.Errorf("body exceeds 1 MiB limit")
	}
	return b, nil
}