// Easy-mode flag helpers for usdc-liq plan/consolidate/deposit/move (no network in pure builders).
// Never log private keys, agent addresses in error strings, balances, or RPC URLs.
package liqcli

import (
	"crypto/ecdsa"
	"flag"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/shopspring/decimal"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/types"
)

// HumanUSDCScale is USDC atomic units per human unit (6 decimals). Fixed; ignore USDC_SCALE_FACTOR.
const HumanUSDCScale int64 = 1_000_000

// ModeKind is plan/consolidate/deposit/move input mode.
type ModeKind int

const (
	// ModeJSON reads -f / stdin wire body.
	ModeJSON ModeKind = iota
	// ModeEasy builds the wire request from flags (no body read).
	ModeEasy
)

// bodyEasyFlagNames trigger easy mode when Visited (plan/consolidate/deposit/move).
var bodyEasyFlagNames = map[string]struct{}{
	"dest":            {},
	"source":          {},
	"sources":         {},
	"from":            {},
	"amount":          {},
	"amount-atomic":   {},
	"balance":         {},
	"gateway-balance": {},
	"live":            {},
}

// stringList is a repeatable flag.Value.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// EasyCommon is shared easy-mode fields for plan and consolidate.
type EasyCommon struct {
	Agent          string
	PrivateKeyHex  string // flag; empty → env AGENT_PRIVATE_KEY at identity resolve
	Testnet        bool   // default true; --mainnet ⇒ false
	Sources        string // comma-separated refs
	Dest           string
	Balances       []string
	GatewayBalance string
	Live           bool
	RPCs           []string
	Execute        bool
}

// EasyPlanInput is easy-mode Phase B plan flags (land N on dest agent_self; no pay_to).
type EasyPlanInput struct {
	EasyCommon
	Amount       string
	AmountAtomic string
}

// EasyConsolidateInput is easy-mode consolidate flags (no pay_to / amount).
type EasyConsolidateInput struct {
	EasyCommon
}

// EasyDepositInput is easy-mode fixed-N deposit flags (no pay_to).
// Single: Source + Amount/AmountAtomic. Multi: From (ref=humanUSDC), exclusive with single.
type EasyDepositInput struct {
	EasyCommon
	Source       string
	Amount       string
	AmountAtomic string
	From         []string // repeatable --from REF=USDC (multi fixed)
}

// EasyMoveInput is easy-mode self-land flags (no pay_to).
type EasyMoveInput struct {
	EasyCommon
	Amount       string
	AmountAtomic string
}

// DetectPlanMode classifies JSON vs easy after flag.Parse.
// Body easy flags + explicit -f → exclusive error. Body easy only → ModeEasy.
// No body easy flags → ModeJSON (default -f "-" / file).
func DetectPlanMode(fs *flag.FlagSet) (ModeKind, error) {
	if fs == nil {
		return ModeJSON, nil
	}
	easy := false
	fVisited := false
	fs.Visit(func(f *flag.Flag) {
		if f == nil {
			return
		}
		if f.Name == "f" {
			fVisited = true
		}
		if _, ok := bodyEasyFlagNames[f.Name]; ok {
			easy = true
		}
	})
	if easy && fVisited {
		return ModeJSON, fmt.Errorf("easy flags and -f are mutually exclusive")
	}
	if easy {
		return ModeEasy, nil
	}
	return ModeJSON, nil
}

// HumanUSDCToAtomic converts human USDC (e.g. "42", "42.5") to whole atomic units (× 10^6).
// Refuses non-positive, non-decimal, or more than 6 fractional digits (no truncate).
func HumanUSDCToAtomic(human string) (decimal.Decimal, error) {
	s := strings.TrimSpace(human)
	if s == "" {
		return decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery, "amount: human USDC required")
	}
	if err := checkFracDigits(s, 6); err != nil {
		return decimal.Zero, err
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery, "amount: human USDC must be a decimal")
	}
	if !d.IsPositive() {
		return decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery, "amount: human USDC must be positive")
	}
	mul := decimal.NewFromInt(HumanUSDCScale)
	atomic := d.Mul(mul)
	if !atomic.Equal(atomic.Truncate(0)) {
		return decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
			"amount: human USDC has more than 6 decimal places")
	}
	return atomic.Truncate(0), nil
}

func checkFracDigits(s string, max int) error {
	// Strip leading + if present for digit check only.
	s = strings.TrimPrefix(s, "+")
	if i := strings.IndexByte(s, '.'); i >= 0 {
		frac := s[i+1:]
		// allow trailing zeros count as digits present
		if len(frac) > max {
			return liqerr.New(liqerr.CodeInvalidQuery,
				"amount: human USDC has more than %d decimal places", max)
		}
	}
	return nil
}

// ParseAmountExclusive requires exactly one of human / atomic amount strings.
// Atomic must be a positive whole decimal integer.
func ParseAmountExclusive(amountHuman, amountAtomic string) (decimal.Decimal, error) {
	h := strings.TrimSpace(amountHuman)
	a := strings.TrimSpace(amountAtomic)
	switch {
	case h != "" && a != "":
		return decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
			"amount: --amount and --amount-atomic are mutually exclusive")
	case h != "":
		return HumanUSDCToAtomic(h)
	case a != "":
		d, err := decimal.NewFromString(a)
		if err != nil {
			return decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
				"amount: --amount-atomic must be a positive whole integer")
		}
		if !d.IsPositive() || !d.Equal(d.Truncate(0)) {
			return decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
				"amount: --amount-atomic must be a positive whole integer")
		}
		return d.Truncate(0), nil
	default:
		return decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
			"amount: require --amount or --amount-atomic")
	}
}

// ParseChainAmountKV parses "ref=humanUSDC" for --balance.
func ParseChainAmountKV(kv string, testnet bool) (caip2 string, amountAtomic decimal.Decimal, err error) {
	return parseChainAmountKVLabeled(kv, testnet, "balance")
}

// ParseFromAmountKV parses "ref=humanUSDC" for deposit --from.
func ParseFromAmountKV(kv string, testnet bool) (caip2 string, amountAtomic decimal.Decimal, err error) {
	return parseChainAmountKVLabeled(kv, testnet, "from")
}

func parseChainAmountKVLabeled(kv string, testnet bool, label string) (caip2 string, amountAtomic decimal.Decimal, err error) {
	ref, human, err := splitKV(kv)
	if err != nil {
		return "", decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery, "%s: expected ref=humanUSDC", label)
	}
	info, err := liquidity.ResolveChainRef(ref, testnet)
	if err != nil {
		return "", decimal.Zero, err
	}
	amt, err := HumanUSDCToAtomic(human)
	if err != nil {
		return "", decimal.Zero, err
	}
	return info.CAIP2, amt, nil
}

// ParseRPCOverride parses "ref=url" for --rpc. Empty URL refused. Never log URL.
func ParseRPCOverride(kv string, testnet bool) (caip2, url string, err error) {
	ref, u, err := splitKV(kv)
	if err != nil {
		return "", "", liqerr.New(liqerr.CodeInvalidQuery, "rpc: expected ref=url")
	}
	if strings.TrimSpace(u) == "" {
		return "", "", liqerr.New(liqerr.CodeInvalidQuery, "rpc: empty url refused")
	}
	info, err := liquidity.ResolveChainRef(ref, testnet)
	if err != nil {
		return "", "", err
	}
	return info.CAIP2, strings.TrimSpace(u), nil
}

func splitKV(kv string) (key, val string, err error) {
	s := strings.TrimSpace(kv)
	i := strings.IndexByte(s, '=')
	if i <= 0 || i == len(s)-1 {
		return "", "", fmt.Errorf("bad kv")
	}
	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:]), nil
}

// ResolveAgentIdentity picks agent address and key hex.
// key = flagKey else envKey; agent = flagAgent else envAgent else derive(key).
// When both agent and key present, derived address must match agent (EqualFold).
// Errors use fixed messages only (never embed key material).
func ResolveAgentIdentity(flagAgent, flagKey, envAgent, envKey string) (agent string, keyHex string, err error) {
	keyHex = strings.TrimSpace(flagKey)
	if keyHex == "" {
		keyHex = strings.TrimSpace(envKey)
	}
	agent = strings.TrimSpace(flagAgent)
	if agent == "" {
		agent = strings.TrimSpace(envAgent)
	}

	var derived string
	if keyHex != "" {
		a, derr := addressFromPrivateKey(keyHex)
		if derr != nil {
			return "", "", liqerr.New(liqerr.CodeInvalidQuery, "agent: private key invalid")
		}
		derived = a
	}

	switch {
	case agent != "" && derived != "":
		if !strings.EqualFold(agent, derived) {
			return "", "", liqerr.New(liqerr.CodeInvalidQuery,
				"agent: address does not match private key")
		}
		return normalizeAgentHex(agent), keyHex, nil
	case agent != "":
		na, aerr := normalizeAgentHexStrict(agent)
		if aerr != nil {
			return "", "", aerr
		}
		return na, keyHex, nil
	case derived != "":
		return derived, keyHex, nil
	default:
		return "", "", liqerr.New(liqerr.CodeInvalidQuery,
			"agent: require --agent or --private-key (or AGENT_ADDRESS / AGENT_PRIVATE_KEY)")
	}
}

func addressFromPrivateKey(hexKey string) (string, error) {
	hexKey = strings.TrimSpace(hexKey)
	hexKey = strings.TrimPrefix(hexKey, "0x")
	hexKey = strings.TrimPrefix(hexKey, "0X")
	if hexKey == "" {
		return "", fmt.Errorf("empty")
	}
	key, err := crypto.HexToECDSA(hexKey)
	if err != nil {
		return "", err
	}
	pub, ok := key.Public().(*ecdsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("pubkey")
	}
	return crypto.PubkeyToAddress(*pub).Hex(), nil
}

func normalizeAgentHex(agent string) string {
	a := strings.TrimSpace(agent)
	if !strings.HasPrefix(a, "0x") && !strings.HasPrefix(a, "0X") {
		a = "0x" + a
	}
	return a
}

func normalizeAgentHexStrict(agent string) (string, error) {
	a := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(agent), "0x"), "0X")
	if len(a) != 40 {
		return "", liqerr.New(liqerr.CodeInvalidQuery, "agent: address required (valid EVM hex)")
	}
	for _, c := range a {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return "", liqerr.New(liqerr.CodeInvalidQuery, "agent: address required (valid EVM hex)")
		}
	}
	return "0x" + a, nil
}

// ValidateEasyGates checks live vs asserted and mainnet+live refusals.
func ValidateEasyGates(common EasyCommon, _ bool) error {
	if common.Live {
		if len(common.Balances) > 0 || strings.TrimSpace(common.GatewayBalance) != "" {
			return fmt.Errorf("--live is mutually exclusive with --balance / --gateway-balance")
		}
		if !common.Testnet {
			return fmt.Errorf("--mainnet and --live are mutually exclusive (MVP testnet inventory only)")
		}
	}
	return nil
}

// ValidateEasyPlanRequired checks dest, amount XOR, and agent (Phase B; no pay_to).
func ValidateEasyPlanRequired(in EasyPlanInput, agent string) error {
	if strings.TrimSpace(agent) == "" {
		return fmt.Errorf("easy plan: --agent or --private-key required")
	}
	if strings.TrimSpace(in.Dest) == "" {
		return fmt.Errorf("easy plan: --dest required")
	}
	h := strings.TrimSpace(in.Amount)
	a := strings.TrimSpace(in.AmountAtomic)
	if h == "" && a == "" {
		return fmt.Errorf("easy plan: --amount or --amount-atomic required")
	}
	if h != "" && a != "" {
		return fmt.Errorf("easy plan: --amount and --amount-atomic are mutually exclusive")
	}
	return nil
}

// ValidateEasyConsolidateRequired checks agent and inventory source for consolidate easy.
func ValidateEasyConsolidateRequired(in EasyConsolidateInput, agent string) error {
	if strings.TrimSpace(agent) == "" {
		return fmt.Errorf("easy consolidate: --agent or --private-key required")
	}
	if !in.Live && len(in.Balances) == 0 && strings.TrimSpace(in.GatewayBalance) == "" {
		return fmt.Errorf("easy consolidate: require --balance / --gateway-balance or --live")
	}
	return nil
}

// ValidateEasyDepositRequired checks agent, single XOR multi, amount XOR, inventory for deposit easy.
func ValidateEasyDepositRequired(in EasyDepositInput, agent string) error {
	if strings.TrimSpace(agent) == "" {
		return fmt.Errorf("deposit: --agent or --private-key required")
	}
	multi := len(in.From) > 0
	singleSource := strings.TrimSpace(in.Source) != ""
	h := strings.TrimSpace(in.Amount)
	a := strings.TrimSpace(in.AmountAtomic)
	singleAmt := h != "" || a != ""
	if multi && (singleSource || singleAmt) {
		return fmt.Errorf("deposit: --from is mutually exclusive with --source/--amount/--amount-atomic")
	}
	if multi {
		// --from list non-empty is enough; inventory still required below.
	} else {
		if !singleSource {
			return fmt.Errorf("deposit: --source or --from required")
		}
		if h == "" && a == "" {
			return fmt.Errorf("deposit: --amount or --amount-atomic required")
		}
		if h != "" && a != "" {
			return fmt.Errorf("deposit: --amount and --amount-atomic are mutually exclusive")
		}
	}
	if !in.Live && len(in.Balances) == 0 {
		return fmt.Errorf("deposit: require --balance or --live")
	}
	return nil
}

// ValidateEasyMoveRequired checks agent, dest, amount XOR, inventory for move easy.
func ValidateEasyMoveRequired(in EasyMoveInput, agent string) error {
	if strings.TrimSpace(agent) == "" {
		return fmt.Errorf("move: --agent or --private-key required")
	}
	if strings.TrimSpace(in.Dest) == "" {
		return fmt.Errorf("move: dest required")
	}
	h := strings.TrimSpace(in.Amount)
	a := strings.TrimSpace(in.AmountAtomic)
	if h == "" && a == "" {
		return fmt.Errorf("move: --amount or --amount-atomic required")
	}
	if h != "" && a != "" {
		return fmt.Errorf("move: --amount and --amount-atomic are mutually exclusive")
	}
	if !in.Live && len(in.Balances) == 0 && strings.TrimSpace(in.GatewayBalance) == "" {
		return fmt.Errorf("move: require --balance / --gateway-balance or --live")
	}
	return nil
}

// BuildAssertedInventory builds wire inventory from --balance and optional --gateway-balance.
func BuildAssertedInventory(agent string, balanceKVs []string, gatewayHuman string, testnet bool) (types.Inventory, error) {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return types.Inventory{}, liqerr.New(liqerr.CodeInvalidQuery, "inventory: agent required")
	}
	out := types.Inventory{AgentAddress: agent}
	for _, kv := range balanceKVs {
		caip, amt, err := ParseChainAmountKV(kv, testnet)
		if err != nil {
			return types.Inventory{}, err
		}
		usdc, ok := liquidity.DefaultUSDC(caip)
		if !ok || usdc == "" {
			return types.Inventory{}, liqerr.New(liqerr.CodeInvalidQuery,
				"inventory: no registered USDC for chain")
		}
		out.Balances = append(out.Balances, types.Balance{
			ChainCAIP2:   caip,
			Asset:        usdc,
			AmountAtomic: amt.String(),
			Location:     liquidity.LocationNative,
		})
	}
	if gw := strings.TrimSpace(gatewayHuman); gw != "" {
		amt, err := HumanUSDCToAtomic(gw)
		if err != nil {
			return types.Inventory{}, err
		}
		out.Balances = append(out.Balances, types.Balance{
			Asset:        "USDC",
			AmountAtomic: amt.String(),
			Location:     liquidity.LocationCircleGateway,
		})
	}
	return out, nil
}

// BuildPlanRequestFromEasy maps easy flags + inventory to a Phase B land PlanRequest.
// Execute comes only from in.Execute (never from Live). No pay_to — mint always agent_self.
func BuildPlanRequestFromEasy(in EasyPlanInput, inv types.Inventory) (types.PlanRequest, error) {
	dest, err := liquidity.ResolveChainRef(in.Dest, in.Testnet)
	if err != nil {
		return types.PlanRequest{}, err
	}
	if dest.USDC == "" {
		return types.PlanRequest{}, liqerr.New(liqerr.CodeInvalidQuery, "plan: dest has no USDC")
	}
	amt, err := ParseAmountExclusive(in.Amount, in.AmountAtomic)
	if err != nil {
		return types.PlanRequest{}, err
	}
	sources, err := resolveSourceCAIP2s(in.Sources, in.Testnet)
	if err != nil {
		return types.PlanRequest{}, err
	}
	orch := &types.Orchestration{
		TargetChainCAIP2: dest.CAIP2,
	}
	if len(sources) > 0 {
		orch.SourceChainCAIP2s = sources
	}
	return types.PlanRequest{
		Required: types.Required{
			ChainCAIP2:   dest.CAIP2,
			Asset:        dest.USDC,
			AmountAtomic: amt.String(),
			Source:       liquidity.AmountSourceSelf,
		},
		Inventory:     inv,
		Orchestration: orch,
		Execute:       in.Execute,
	}, nil
}

// BuildConsolidateRequestFromEasy maps easy flags + inventory to ConsolidateRequest.
func BuildConsolidateRequestFromEasy(in EasyConsolidateInput, inv types.Inventory) (types.ConsolidateRequest, error) {
	var orch *types.Orchestration
	if strings.TrimSpace(in.Dest) != "" || strings.TrimSpace(in.Sources) != "" {
		orch = &types.Orchestration{}
		if strings.TrimSpace(in.Dest) != "" {
			dest, err := liquidity.ResolveChainRef(in.Dest, in.Testnet)
			if err != nil {
				return types.ConsolidateRequest{}, err
			}
			orch.TargetChainCAIP2 = dest.CAIP2
		}
		sources, err := resolveSourceCAIP2s(in.Sources, in.Testnet)
		if err != nil {
			return types.ConsolidateRequest{}, err
		}
		if len(sources) > 0 {
			orch.SourceChainCAIP2s = sources
		}
	}
	return types.ConsolidateRequest{
		Inventory:     inv,
		Orchestration: orch,
		Execute:       in.Execute,
	}, nil
}

// BuildDepositRequestFromEasy maps easy flags + inventory to DepositRequest.
// Multi --from builds sources[]; single uses source_chain_caip2 + amount_atomic.
func BuildDepositRequestFromEasy(in EasyDepositInput, inv types.Inventory) (types.DepositRequest, error) {
	var orch *types.Orchestration
	if strings.TrimSpace(in.Sources) != "" {
		sources, err := resolveSourceCAIP2s(in.Sources, in.Testnet)
		if err != nil {
			return types.DepositRequest{}, err
		}
		if len(sources) > 0 {
			orch = &types.Orchestration{SourceChainCAIP2s: sources}
		}
	}
	if len(in.From) > 0 {
		wireSources := make([]types.FundingSource, 0, len(in.From))
		for _, kv := range in.From {
			caip, amt, err := ParseFromAmountKV(kv, in.Testnet)
			if err != nil {
				return types.DepositRequest{}, err
			}
			wireSources = append(wireSources, types.FundingSource{
				ChainCAIP2:   caip,
				AmountAtomic: amt.String(),
			})
		}
		return types.DepositRequest{
			Inventory:     inv,
			Sources:       wireSources,
			Orchestration: orch,
			Execute:       in.Execute,
		}, nil
	}
	src, err := liquidity.ResolveChainRef(in.Source, in.Testnet)
	if err != nil {
		return types.DepositRequest{}, err
	}
	amt, err := ParseAmountExclusive(in.Amount, in.AmountAtomic)
	if err != nil {
		return types.DepositRequest{}, err
	}
	return types.DepositRequest{
		Inventory:        inv,
		SourceChainCAIP2: src.CAIP2,
		AmountAtomic:     amt.String(),
		Orchestration:    orch,
		Execute:          in.Execute,
	}, nil
}

// BuildMoveRequestFromEasy maps easy flags + inventory to MoveRequest.
func BuildMoveRequestFromEasy(in EasyMoveInput, inv types.Inventory) (types.MoveRequest, error) {
	dest, err := liquidity.ResolveChainRef(in.Dest, in.Testnet)
	if err != nil {
		return types.MoveRequest{}, err
	}
	amt, err := ParseAmountExclusive(in.Amount, in.AmountAtomic)
	if err != nil {
		return types.MoveRequest{}, err
	}
	sources, err := resolveSourceCAIP2s(in.Sources, in.Testnet)
	if err != nil {
		return types.MoveRequest{}, err
	}
	orch := &types.Orchestration{TargetChainCAIP2: dest.CAIP2}
	if len(sources) > 0 {
		orch.SourceChainCAIP2s = sources
	}
	return types.MoveRequest{
		DestChainCAIP2: dest.CAIP2,
		AmountAtomic:   amt.String(),
		Inventory:      inv,
		Orchestration:  orch,
		Execute:        in.Execute,
	}, nil
}

func resolveSourceCAIP2s(csv string, testnet bool) ([]string, error) {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil, nil
	}
	var out []string
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		info, err := liquidity.ResolveChainRef(part, testnet)
		if err != nil {
			return nil, err
		}
		out = append(out, info.CAIP2)
	}
	return out, nil
}

// ParseRPCMap builds CAIP-2 → URL from repeatable --rpc ref=url entries.
func ParseRPCMap(kvs []string, testnet bool) (map[string]string, error) {
	if len(kvs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		caip, url, err := ParseRPCOverride(kv, testnet)
		if err != nil {
			return nil, err
		}
		out[caip] = url
	}
	return out, nil
}