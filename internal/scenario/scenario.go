// Package scenario loads the demo payment scenario from process env (not HTTP).
// Scale maps logical atomic amounts to real on-chain atomic units.
// Never logs keys, balances, or private material.
package scenario

import (
	"crypto/ecdsa"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/shopspring/decimal"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
)

// Named source env → CAIP-2 (MVP testnets only; registry USDC, no env USDC override).
const (
	EnvScaleFactor             = "USDC_SCALE_FACTOR"
	EnvPaymentProtocol         = "PAYMENT_PROTOCOL"
	EnvPaymentChain            = "PAYMENT_CHAIN"
	EnvPaymentAmountUSDC       = "PAYMENT_AMOUNT_USDC"
	EnvSourceAmountBaseSepolia = "SOURCE_AMOUNT_BASE_SEPOLIA"
	EnvSourceAmountArbSepolia  = "SOURCE_AMOUNT_ARBITRUM_SEPOLIA"
	EnvSourceAmountArcTestnet  = "SOURCE_AMOUNT_ARC_TESTNET"
	EnvSourceMode              = "SOURCE_MODE"
	EnvAgentAddress            = "AGENT_ADDRESS"
	EnvAgentPrivateKey         = "AGENT_PRIVATE_KEY"
)

// Source is one hard-coded deposit source after scale.
type Source struct {
	ChainCAIP2    string
	LogicalAtomic decimal.Decimal
	RealAtomic    decimal.Decimal
}

// Scenario is a fully validated env Phase A payment land amount + hard-coded sources (real amounts).
// No merchant pay_to — deposits fund agent_self Gateway only.
type Scenario struct {
	Protocol             string
	PaymentChainCAIP2    string
	AgentAddress         string
	ScaleFactor          int64
	PaymentLogicalAtomic decimal.Decimal
	PaymentRealAtomic    decimal.Decimal
	Sources              []Source // positive real only
}

// LoadFromEnv reads payment scenario env vars, applies scale, and validates
// sum(source_real) == payment_real. Never logs values. PAYMENT_PAY_TO is ignored if set.
func LoadFromEnv() (Scenario, error) {
	if err := validateSourceMode(); err != nil {
		return Scenario{}, err
	}
	scale, err := parseScale(os.Getenv(EnvScaleFactor))
	if err != nil {
		return Scenario{}, err
	}
	protocol, chain, payLogical, payReal, err := loadPaymentClaim(scale)
	if err != nil {
		return Scenario{}, err
	}
	agent, err := requireAgent()
	if err != nil {
		return Scenario{}, err
	}
	sources, err := loadScaledSources(scale, payLogical, payReal)
	if err != nil {
		return Scenario{}, err
	}
	return Scenario{
		Protocol:             protocol,
		PaymentChainCAIP2:    chain,
		AgentAddress:         agent,
		ScaleFactor:          scale,
		PaymentLogicalAtomic: payLogical,
		PaymentRealAtomic:    payReal,
		Sources:              sources,
	}, nil
}

func validateSourceMode() error {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv(EnvSourceMode)))
	if mode == "" || mode == "hardcoded" || mode == "hard_coded" {
		return nil
	}
	if mode == "auto" {
		return liqerr.New(liqerr.CodeInvalidQuery,
			"scenario: SOURCE_MODE=auto is not implemented (use hard-coded SOURCE_AMOUNT_*)")
	}
	return liqerr.New(liqerr.CodeInvalidQuery,
		"scenario: SOURCE_MODE %q invalid (hardcoded only in this cut)", mode)
}

func loadPaymentClaim(scale int64) (protocol, chain string, payLogical, payReal decimal.Decimal, err error) {
	protocol = strings.TrimSpace(os.Getenv(EnvPaymentProtocol))
	if protocol == "" {
		protocol = "x402"
	}
	chain = strings.TrimSpace(os.Getenv(EnvPaymentChain))
	if chain == "" {
		return "", "", decimal.Zero, decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
			"scenario: PAYMENT_CHAIN required (CAIP-2 dest)")
	}
	info, ok := liquidity.LookupChain(chain)
	if !ok {
		return "", "", decimal.Zero, decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
			"scenario: PAYMENT_CHAIN %q not in registry", chain)
	}
	chain = info.CAIP2

	payHuman := strings.TrimSpace(os.Getenv(EnvPaymentAmountUSDC))
	if payHuman == "" {
		return "", "", decimal.Zero, decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
			"scenario: PAYMENT_AMOUNT_USDC required")
	}
	payLogical, err = HumanUSDCToLogicalAtomic(payHuman)
	if err != nil {
		return "", "", decimal.Zero, decimal.Zero, err
	}
	payReal, err = ScaleLogicalToReal(payLogical, scale)
	if err != nil {
		return "", "", decimal.Zero, decimal.Zero, err
	}
	return protocol, chain, payLogical, payReal, nil
}

func requireAgent() (string, error) {
	agent, err := resolveAgentAddress()
	if err != nil {
		return "", err
	}
	if agent == "" {
		return "", liqerr.New(liqerr.CodeInvalidQuery,
			"scenario: agent required (set AGENT_ADDRESS or AGENT_PRIVATE_KEY)")
	}
	return agent, nil
}

func loadScaledSources(scale int64, payLogical, payReal decimal.Decimal) ([]Source, error) {
	rawSources := []struct {
		env  string
		caip string
	}{
		{EnvSourceAmountBaseSepolia, "eip155:84532"},
		{EnvSourceAmountArbSepolia, "eip155:421614"},
		{EnvSourceAmountArcTestnet, "eip155:5042002"},
	}
	var sources []Source
	sumReal := decimal.Zero
	sumLogical := decimal.Zero
	for _, rs := range rawSources {
		human := strings.TrimSpace(os.Getenv(rs.env))
		if human == "" || human == "0" {
			continue
		}
		logical, err := HumanUSDCToLogicalAtomic(human)
		if err != nil {
			return nil, liqerr.Wrap(liqerr.CodeInvalidQuery, err, "scenario: %s", rs.env)
		}
		real, err := ScaleLogicalToReal(logical, scale)
		if err != nil {
			return nil, liqerr.Wrap(liqerr.CodeInvalidQuery, err, "scenario: %s after scale", rs.env)
		}
		info, ok := liquidity.LookupChain(rs.caip)
		if !ok {
			return nil, liqerr.New(liqerr.CodeInvalidQuery,
				"scenario: source chain %q not in registry", rs.caip)
		}
		sources = append(sources, Source{
			ChainCAIP2:    info.CAIP2,
			LogicalAtomic: logical,
			RealAtomic:    real,
		})
		sumReal = sumReal.Add(real)
		sumLogical = sumLogical.Add(logical)
	}
	if len(sources) == 0 {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"scenario: at least one positive SOURCE_AMOUNT_* required")
	}
	if !sumLogical.Equal(payLogical) {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"scenario: sum(SOURCE_AMOUNT_*) logical must equal PAYMENT_AMOUNT_USDC logical")
	}
	if !sumReal.Equal(payReal) {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"scenario: sum(source_real) must equal payment_real after scale (floor desync or mismatch)")
	}
	return sources, nil
}

// BuildRequired maps the scenario land amount to liquidity.Required (real amount_atomic; no pay_to).
func (s Scenario) BuildRequired() liquidity.Required {
	asset, ok := liquidity.DefaultUSDC(s.PaymentChainCAIP2)
	if !ok {
		asset = "USDC"
	}
	return liquidity.Required{
		Protocol:            s.Protocol,
		ChainCAIP2:          s.PaymentChainCAIP2,
		Asset:               asset,
		AmountAtomic:        s.PaymentRealAtomic,
		AmountSource:        liquidity.AmountSourceProbe,
		AmountLogicalAtomic: s.PaymentLogicalAtomic,
		ScaleFactor:         s.ScaleFactor,
	}
}

// BuildAssertedInventory builds client-asserted native balances from hard-coded source reals.
// Does not read chain state (PR2). Dest-chain sources appear as native rows like any other source.
func (s Scenario) BuildAssertedInventory() liquidity.Inventory {
	bals := make([]liquidity.Balance, 0, len(s.Sources))
	for _, src := range s.Sources {
		asset, ok := liquidity.DefaultUSDC(src.ChainCAIP2)
		if !ok {
			asset = "USDC"
		}
		bals = append(bals, liquidity.Balance{
			ChainCAIP2:   src.ChainCAIP2,
			Asset:        asset,
			AmountAtomic: src.RealAtomic,
			Location:     liquidity.LocationNative,
		})
	}
	return liquidity.Inventory{
		AgentAddress: s.AgentAddress,
		Balances:     bals,
	}
}

// FundingSources maps scenario sources to liquidity.FundingSource for PlanPaymentFunding.
func (s Scenario) FundingSources() []liquidity.FundingSource {
	out := make([]liquidity.FundingSource, 0, len(s.Sources))
	for _, src := range s.Sources {
		out = append(out, liquidity.FundingSource{
			ChainCAIP2:          src.ChainCAIP2,
			AmountAtomic:        src.RealAtomic,
			AmountLogicalAtomic: src.LogicalAtomic,
		})
	}
	return out
}

func parseScale(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 1, nil
	}
	d, err := decimal.NewFromString(raw)
	if err != nil {
		return 0, liqerr.Wrap(liqerr.CodeInvalidQuery, err,
			"scenario: USDC_SCALE_FACTOR %q is not an integer", raw)
	}
	if !d.Equal(d.Truncate(0)) {
		return 0, liqerr.New(liqerr.CodeInvalidQuery,
			"scenario: USDC_SCALE_FACTOR must be a whole integer")
	}
	if !d.IsPositive() {
		return 0, liqerr.New(liqerr.CodeInvalidQuery,
			"scenario: USDC_SCALE_FACTOR must be > 0 (got %s)", raw)
	}
	return d.IntPart(), nil
}

func resolveAgentAddress() (string, error) {
	addrEnv := strings.TrimSpace(os.Getenv(EnvAgentAddress))
	keyEnv := strings.TrimSpace(os.Getenv(EnvAgentPrivateKey))
	var derived string
	if keyEnv != "" {
		a, err := addressFromPrivateKey(keyEnv)
		if err != nil {
			return "", err
		}
		derived = a
	}
	switch {
	case addrEnv != "" && derived != "":
		if !addrEqualEVM(addrEnv, derived) {
			return "", liqerr.New(liqerr.CodeInvalidQuery,
				"scenario: AGENT_ADDRESS does not match address derived from AGENT_PRIVATE_KEY")
		}
		return addrEnv, nil
	case addrEnv != "":
		return addrEnv, nil
	case derived != "":
		return derived, nil
	default:
		return "", nil
	}
}

func addressFromPrivateKey(hexKey string) (string, error) {
	hexKey = strings.TrimSpace(hexKey)
	hexKey = strings.TrimPrefix(hexKey, "0x")
	hexKey = strings.TrimPrefix(hexKey, "0X")
	if hexKey == "" {
		return "", liqerr.New(liqerr.CodeInvalidQuery,
			"scenario: AGENT_PRIVATE_KEY empty after trim")
	}
	key, err := crypto.HexToECDSA(hexKey)
	if err != nil {
		return "", liqerr.New(liqerr.CodeInvalidQuery,
			"scenario: AGENT_PRIVATE_KEY is not a valid ECDSA key")
	}
	pub, ok := key.Public().(*ecdsa.PublicKey)
	if !ok {
		return "", liqerr.New(liqerr.CodeInvalidQuery,
			"scenario: AGENT_PRIVATE_KEY public type invalid")
	}
	return crypto.PubkeyToAddress(*pub).Hex(), nil
}

func addrEqualEVM(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(a, b)
}