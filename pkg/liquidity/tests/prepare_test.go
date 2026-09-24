package liquidity_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
)

func TestPrepareCalls_DepositGolden(t *testing.T) {
	// Phase A fixed-N deposit on Base Sepolia with known amount → fixed calldata.
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC,
			AmountAtomic: decimal.RequireFromString("5000000"), Location: liquidity.LocationNative,
		}},
	}
	p, err := liquidity.PlanGatewayDeposit(inv, baseSepCAIP2, decimal.RequireFromString("1000000"), nil, nil)
	require.NoError(t, err)
	assert.Equal(t, liquidity.ActionCircleGatewayDeposit, p.Action)
	require.Len(t, p.Steps, 1)
	dep := p.Steps[0]
	assert.Equal(t, liquidity.StepKindCircleGatewayDeposit, dep.Kind)
	require.Len(t, dep.PrepareCalls, 2)

	approve := dep.PrepareCalls[0]
	deposit := dep.PrepareCalls[1]

	assert.Equal(t, baseSepCAIP2, approve.ChainCAIP2)
	assert.Equal(t, baseSepUSDC, approve.To)
	assert.Equal(t, "0", approve.Value)
	assert.Equal(t, "approve", approve.Method)
	assert.True(t, strings.HasPrefix(strings.ToLower(approve.Data), "0x095ea7b3"),
		"approve selector, got %s", approve.Data)

	wallet, ok := liquidity.GatewayWalletAddress(baseSepCAIP2)
	require.True(t, ok)
	assert.Equal(t, liquidity.GatewayWalletTestnet, wallet)

	approveRaw, err := hex.DecodeString(strings.TrimPrefix(approve.Data, "0x"))
	require.NoError(t, err)
	require.Len(t, approveRaw, 4+32+32)
	spenderWord := approveRaw[4 : 4+32]
	assert.Equal(t, strings.ToLower(strings.TrimPrefix(wallet, "0x")),
		hex.EncodeToString(spenderWord[12:]))

	amtWord := approveRaw[4+32:]
	assert.Equal(t, "00000000000000000000000000000000000000000000000000000000000f4240",
		hex.EncodeToString(amtWord))

	assert.Equal(t, baseSepCAIP2, deposit.ChainCAIP2)
	assert.Equal(t, wallet, deposit.To)
	assert.Equal(t, "0", deposit.Value)
	assert.Equal(t, "deposit", deposit.Method)
	assert.True(t, strings.HasPrefix(strings.ToLower(deposit.Data), "0x47e7ef24"),
		"deposit(address,uint256) selector, got %s", deposit.Data)
	depositRaw, err := hex.DecodeString(strings.TrimPrefix(deposit.Data, "0x"))
	require.NoError(t, err)
	require.Len(t, depositRaw, 4+32+32)
	tokenWord := depositRaw[4 : 4+32]
	assert.Equal(t, strings.ToLower(strings.TrimPrefix(baseSepUSDC, "0x")),
		hex.EncodeToString(tokenWord[12:]))
	assert.Equal(t, "00000000000000000000000000000000000000000000000000000000000f4240",
		hex.EncodeToString(depositRaw[4+32:]))

	assert.True(t, dep.AmountAtomic.Equal(decimal.RequireFromString("1000000")))
	assert.Equal(t, agentAddr, dep.Recipient)
}

func TestPrepareCalls_DoesNotMutateStepIdentity(t *testing.T) {
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{{
			ChainCAIP2: arcCAIP2, Asset: arcUSDC, AmountAtomic: decimal.RequireFromString("42"), Location: liquidity.LocationNative,
		}},
	}
	p, err := liquidity.PlanConsolidate(inv, nil, nil)
	require.NoError(t, err)
	require.Len(t, p.Steps, 1)
	assert.True(t, p.Steps[0].AmountAtomic.Equal(decimal.RequireFromString("42")))
	assert.Equal(t, agentAddr, p.Steps[0].Recipient)
	require.Len(t, p.Steps[0].PrepareCalls, 2)
}

func TestPlan_PhaseB_OtherNative_NoDepositPrepareCalls(t *testing.T) {
	// Fragmented: need 42 on Base, 20 Base + 30 Arb → Phase B CCTP shortfall 22; no deposit steps.
	req := baseRequired()
	req.AmountAtomic = decimal.RequireFromString("42000000")
	inv := liquidity.Inventory{
		AgentAddress: agentAddr,
		Balances: []liquidity.Balance{
			{ChainCAIP2: arbCAIP2, Asset: arbUSDC, AmountAtomic: decimal.RequireFromString("30000000"), Location: liquidity.LocationNative},
			{ChainCAIP2: baseCAIP2, Asset: baseUSDC, AmountAtomic: decimal.RequireFromString("20000000"), Location: liquidity.LocationNative},
		},
	}
	p, err := liquidity.PlanLiquidity(req, inv, nil)
	require.NoError(t, err)
	assert.Equal(t, liquidity.ActionCCTPFast, p.Action)
	require.Len(t, p.Steps, 2)
	assert.True(t, p.Steps[0].AmountAtomic.Equal(decimal.RequireFromString("22000000")))
	assert.Equal(t, liquidity.StepKindCCTPBurn, p.Steps[0].Kind)
	assert.Empty(t, p.Steps[0].PrepareCalls)
	assert.Empty(t, p.Steps[1].PrepareCalls)
}