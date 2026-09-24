package liquidity

import (
	"encoding/hex"
	"math/big"
	"strings"

	"github.com/shopspring/decimal"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
)

// PrepareCall is an unsigned EVM call for agent-side signing.
// Server-generated advisory only — CheckPlan does not re-validate prepare_calls.
type PrepareCall struct {
	ChainCAIP2  string
	To          string
	Data        string
	Value       string
	Method      string
	Description string
}

// Fixed Keccak-256 ABI selectors (pure Go packing; no go-ethereum).
// approve(address,uint256) = 0x095ea7b3
// deposit(address,uint256) = 0x47e7ef24
var (
	selectorApprove = []byte{0x09, 0x5e, 0xa7, 0xb3}
	selectorDeposit = []byte{0x47, 0xe7, 0xef, 0x24}
)

// BuildDepositPrepareCalls re-derives unsigned approve+deposit calls for a deposit step.
// Pure: does not mutate s. Non-deposit steps return (nil, nil).
// Live execute must sign only re-derived calls, never client-supplied calldata.
func BuildDepositPrepareCalls(s PlanStep) ([]PrepareCall, error) {
	if strings.ToLower(strings.TrimSpace(s.Kind)) != StepKindCircleGatewayDeposit {
		return nil, nil
	}
	chain := strings.TrimSpace(s.FromChainCAIP2)
	if chain == "" {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: circle_gateway_deposit step missing from_chain_caip2 for prepare_calls")
	}
	usdc, ok := DefaultUSDC(chain)
	if !ok {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: no registered USDC for chain %q (prepare_calls)", chain)
	}
	wallet, ok := GatewayWalletAddress(chain)
	if !ok {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: no gateway wallet for chain %q (prepare_calls)", chain)
	}
	if !s.AmountAtomic.IsPositive() {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: deposit amount must be positive for prepare_calls")
	}

	approveData, err := packCall(selectorApprove, wallet, s.AmountAtomic)
	if err != nil {
		return nil, err
	}
	depositData, err := packCall(selectorDeposit, usdc, s.AmountAtomic)
	if err != nil {
		return nil, err
	}

	return []PrepareCall{
		{
			ChainCAIP2:  chain,
			To:          usdc,
			Data:        "0x" + hex.EncodeToString(approveData),
			Value:       "0",
			Method:      "approve",
			Description: "ERC-20 approve Gateway Wallet as USDC spender",
		},
		{
			ChainCAIP2:  chain,
			To:          wallet,
			Data:        "0x" + hex.EncodeToString(depositData),
			Value:       "0",
			Method:      "deposit",
			Description: "Circle Gateway Wallet deposit(token,amount)",
		},
	}, nil
}

// attachDepositPrepareCalls fills approve+deposit prepare_calls for a deposit step.
// Never mutates AmountAtomic or Recipient. To addresses are registry allowlisted.
func attachDepositPrepareCalls(s *PlanStep) error {
	if s == nil {
		return nil
	}
	calls, err := BuildDepositPrepareCalls(*s)
	if err != nil {
		return err
	}
	if calls != nil {
		s.PrepareCalls = calls
	}
	return nil
}

func attachDepositPrepareCallsOnPlan(p *Plan) error {
	if p == nil {
		return nil
	}
	for i := range p.Steps {
		if err := attachDepositPrepareCalls(&p.Steps[i]); err != nil {
			return err
		}
	}
	return nil
}

func packCall(selector []byte, address string, amount decimal.Decimal) ([]byte, error) {
	addrWord, err := addressWord(address)
	if err != nil {
		return nil, err
	}
	amtWord, err := uint256Word(amount)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 4+32+32)
	out = append(out, selector...)
	out = append(out, addrWord...)
	out = append(out, amtWord...)
	return out, nil
}

func addressWord(addr string) ([]byte, error) {
	a := strings.TrimSpace(addr)
	a = strings.TrimPrefix(a, "0x")
	a = strings.TrimPrefix(a, "0X")
	if len(a) != 40 {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: address must be 20-byte hex for prepare_calls")
	}
	b, err := hex.DecodeString(a)
	if err != nil {
		return nil, liqerr.Wrap(liqerr.CodeInvalidQuery, err,
			"liquidity: invalid address hex for prepare_calls")
	}
	word := make([]byte, 32)
	copy(word[12:], b)
	return word, nil
}

func uint256Word(d decimal.Decimal) ([]byte, error) {
	if d.IsNegative() {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: amount must be non-negative for prepare_calls")
	}
	if !d.Equal(d.Truncate(0)) {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: amount must be whole atomic units for prepare_calls")
	}
	bi, ok := new(big.Int).SetString(d.String(), 10)
	if !ok {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: amount not integer for prepare_calls")
	}
	if bi.BitLen() > 256 {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"liquidity: amount exceeds uint256 for prepare_calls")
	}
	raw := bi.Bytes()
	word := make([]byte, 32)
	copy(word[32-len(raw):], raw)
	return word, nil
}