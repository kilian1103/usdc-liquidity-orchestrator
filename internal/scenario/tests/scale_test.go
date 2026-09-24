package scenario_test

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/scenario"
	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
)

func TestScale_T1_Scale10_300Plus100Equals400(t *testing.T) {
	payL, err := scenario.HumanUSDCToLogicalAtomic("400")
	require.NoError(t, err)
	baseL, err := scenario.HumanUSDCToLogicalAtomic("300")
	require.NoError(t, err)
	arbL, err := scenario.HumanUSDCToLogicalAtomic("100")
	require.NoError(t, err)
	require.True(t, baseL.Add(arbL).Equal(payL))

	const scale int64 = 10
	payR, err := scenario.ScaleLogicalToReal(payL, scale)
	require.NoError(t, err)
	baseR, err := scenario.ScaleLogicalToReal(baseL, scale)
	require.NoError(t, err)
	arbR, err := scenario.ScaleLogicalToReal(arbL, scale)
	require.NoError(t, err)

	// 40 + 30 + 10 USDC real at 6 decimals
	assert.True(t, payR.Equal(decimal.RequireFromString("40000000")))
	assert.True(t, baseR.Equal(decimal.RequireFromString("30000000")))
	assert.True(t, arbR.Equal(decimal.RequireFromString("10000000")))
	assert.True(t, baseR.Add(arbR).Equal(payR))
}

func TestScale_T2_FloorDesync_Refuse(t *testing.T) {
	// human 2+2=4, scale=3 → sum floor(2e6/3) != floor(4e6/3)
	a, err := scenario.HumanUSDCToLogicalAtomic("2")
	require.NoError(t, err)
	b, err := scenario.HumanUSDCToLogicalAtomic("2")
	require.NoError(t, err)
	pay, err := scenario.HumanUSDCToLogicalAtomic("4")
	require.NoError(t, err)
	require.True(t, a.Add(b).Equal(pay))

	const scale int64 = 3
	ar, err := scenario.ScaleLogicalToReal(a, scale)
	require.NoError(t, err)
	br, err := scenario.ScaleLogicalToReal(b, scale)
	require.NoError(t, err)
	pr, err := scenario.ScaleLogicalToReal(pay, scale)
	require.NoError(t, err)
	assert.False(t, ar.Add(br).Equal(pr), "expected floor desync")
}

func TestScale_T3_ScaleLEZero_Refuse(t *testing.T) {
	logical := decimal.RequireFromString("1000000")
	for _, s := range []int64{0, -1, -10} {
		_, err := scenario.ScaleLogicalToReal(logical, s)
		require.Error(t, err, "scale %d", s)
		assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
	}
}