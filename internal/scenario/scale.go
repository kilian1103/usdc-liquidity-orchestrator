package scenario

import (
	"strings"

	"github.com/shopspring/decimal"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
)

// USDCDecimals is the fixed on-chain decimal places for USDC (atomic units).
const USDCDecimals = 6

// HumanUSDCToLogicalAtomic converts human USDC (e.g. "400") to logical atomic units (* 10^6).
// Result is truncated to whole atomic units (no Round(2)).
func HumanUSDCToLogicalAtomic(human string) (decimal.Decimal, error) {
	d, err := decimal.NewFromString(strings.TrimSpace(human))
	if err != nil {
		return decimal.Zero, liqerr.Wrap(liqerr.CodeInvalidQuery, err,
			"scenario: human USDC amount %q is not a decimal", human)
	}
	if !d.IsPositive() {
		return decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
			"scenario: human USDC amount must be positive")
	}
	mul := decimal.New(1, USDCDecimals) // 10^6
	logical := d.Mul(mul)
	if !logical.Equal(logical.Truncate(0)) {
		return decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
			"scenario: human USDC amount has more than %d decimal places", USDCDecimals)
	}
	return logical.Truncate(0), nil
}

// ScaleLogicalToReal returns floor(logicalAtomic / scaleFactor) as whole atomic units.
// scaleFactor <= 0 is refused (invalid_query).
func ScaleLogicalToReal(logicalAtomic decimal.Decimal, scaleFactor int64) (decimal.Decimal, error) {
	if scaleFactor <= 0 {
		return decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
			"scenario: USDC_SCALE_FACTOR must be > 0 (got %d)", scaleFactor)
	}
	if !logicalAtomic.IsPositive() {
		return decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
			"scenario: logical atomic amount must be positive")
	}
	if !logicalAtomic.Equal(logicalAtomic.Truncate(0)) {
		return decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
			"scenario: logical atomic amount must be whole units")
	}
	// floor division via Truncate(0) on positive quotient.
	real := logicalAtomic.Div(decimal.NewFromInt(scaleFactor)).Truncate(0)
	if !real.IsPositive() {
		return decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
			"scenario: real atomic amount is zero after scale (logical=%s scale=%d)",
			logicalAtomic.String(), scaleFactor)
	}
	return real, nil
}