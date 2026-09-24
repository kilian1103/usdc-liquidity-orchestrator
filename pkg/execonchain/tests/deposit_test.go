package execonchain_test

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/execonchain"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
)

const (
	baseSepCAIP2 = "eip155:84532"
	baseSepUSDC  = "0x036CbD53842c5426634e7929541eC2318f3dCF7e"
	baseMainnet  = "eip155:8453"
)

func testKey(t *testing.T) (*ecdsa.PrivateKey, string, string) {
	t.Helper()
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	hex := common.Bytes2Hex(crypto.FromECDSA(key))
	addr := crypto.PubkeyToAddress(key.PublicKey).Hex()
	return key, hex, addr
}

type mockClient struct {
	mu           sync.Mutex
	chainID      *big.Int
	nonce        uint64
	gasTipCap    *big.Int
	baseFee      *big.Int
	estimateGas  uint64
	estimateErr  error
	sendErr      error
	sendFailAt   int // 0 = never; 1-based call index
	receiptFail  int // 0 = never; 1-based receipt that reverts
	sends        int
	sent         []*types.Transaction
	chainIDCalls int
	dialURL      string
}

func newMock(chainID int64) *mockClient {
	return &mockClient{
		chainID:     big.NewInt(chainID),
		gasTipCap:   big.NewInt(1_000_000_000),
		baseFee:     big.NewInt(1_000_000_000),
		estimateGas: 100_000,
	}
}

func (m *mockClient) ChainID(ctx context.Context) (*big.Int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chainIDCalls++
	return new(big.Int).Set(m.chainID), nil
}

func (m *mockClient) PendingNonceAt(ctx context.Context, account common.Address) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nonce, nil
}

func (m *mockClient) SuggestGasTipCap(ctx context.Context) (*big.Int, error) {
	return new(big.Int).Set(m.gasTipCap), nil
}

func (m *mockClient) HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error) {
	return &types.Header{BaseFee: new(big.Int).Set(m.baseFee), Number: big.NewInt(1)}, nil
}

func (m *mockClient) EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error) {
	if m.estimateErr != nil {
		return 0, m.estimateErr
	}
	return m.estimateGas, nil
}

func (m *mockClient) SendTransaction(ctx context.Context, tx *types.Transaction) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sends++
	if m.sendFailAt > 0 && m.sends == m.sendFailAt {
		return errors.New("rpc: boom should not leak")
	}
	if m.sendErr != nil {
		return m.sendErr
	}
	m.sent = append(m.sent, tx)
	m.nonce++
	return nil
}

func (m *mockClient) TransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := types.ReceiptStatusSuccessful
	// Find send index for this hash.
	for i, tx := range m.sent {
		if tx.Hash() == txHash {
			if m.receiptFail > 0 && i+1 == m.receiptFail {
				status = types.ReceiptStatusFailed
			}
			return &types.Receipt{Status: status, TxHash: txHash}, nil
		}
	}
	return &types.Receipt{Status: status, TxHash: txHash}, nil
}

func (m *mockClient) Close() {}

func dialMock(m *mockClient) func(context.Context, string) (execonchain.ChainClient, error) {
	return func(ctx context.Context, url string) (execonchain.ChainClient, error) {
		m.dialURL = url
		return m, nil
	}
}

func consolidatePlan(t *testing.T, agent string, amount string) liquidity.Plan {
	t.Helper()
	inv := liquidity.Inventory{
		AgentAddress: agent,
		Balances: []liquidity.Balance{{
			ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC,
			AmountAtomic: decimal.RequireFromString(amount),
			Location:     liquidity.LocationNative,
		}},
	}
	p, err := liquidity.PlanConsolidate(inv, nil, nil)
	require.NoError(t, err)
	require.Equal(t, liquidity.ActionCircleGatewayConsolidate, p.Action)
	return p
}

func fixedDepositPlan(t *testing.T, agent string, amount string) liquidity.Plan {
	t.Helper()
	inv := liquidity.Inventory{
		AgentAddress: agent,
		Balances: []liquidity.Balance{{
			ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC,
			AmountAtomic: decimal.RequireFromString(amount),
			Location:     liquidity.LocationNative,
		}},
	}
	p, err := liquidity.PlanGatewayDeposit(inv, baseSepCAIP2, decimal.RequireFromString(amount), nil, nil)
	require.NoError(t, err)
	require.Equal(t, liquidity.ActionCircleGatewayDeposit, p.Action)
	return p
}

func TestNewDepositExecutor_RefusesMainnetRPC(t *testing.T) {
	_, hex, _ := testKey(t)
	_, err := execonchain.NewDepositExecutor(execonchain.Config{
		PrivateKeyHex: hex,
		RPCs:          map[string]string{baseMainnet: "https://example.invalid"},
	})
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeLiquidityRailUnavailable, liqerr.CodeOf(err))
}

func TestNewDepositExecutor_RefusesEmptyKey(t *testing.T) {
	_, err := execonchain.NewDepositExecutor(execonchain.Config{
		PrivateKeyHex: "",
		RPCs:          map[string]string{baseSepCAIP2: "https://example.invalid"},
	})
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
}

func TestDepositExecutor_HappyPath(t *testing.T) {
	_, hex, agent := testKey(t)
	mock := newMock(84532)
	ex, err := execonchain.NewDepositExecutor(execonchain.Config{
		PrivateKeyHex: hex,
		RPCs:          map[string]string{baseSepCAIP2: "http://mock.local"},
		Dial:          dialMock(mock),
		WaitTimeout:   time.Second,
	})
	require.NoError(t, err)
	assert.True(t, strings.EqualFold(ex.Address().Hex(), agent))

	plan := consolidatePlan(t, agent, "1000")
	rcpt, err := ex.Execute(context.Background(), plan)
	require.NoError(t, err)
	require.Len(t, rcpt.TxHashes, 2) // approve + deposit
	assert.Equal(t, 2, mock.sends)
	assert.GreaterOrEqual(t, mock.chainIDCalls, 1)
	require.NotEmpty(t, mock.sent)
	// EIP-1559 dynamic fee txs (type 2), not legacy gasPrice.
	assert.Equal(t, uint8(types.DynamicFeeTxType), mock.sent[0].Type())
	assert.NotNil(t, mock.sent[0].GasTipCap())
	assert.NotNil(t, mock.sent[0].GasFeeCap())
}

func TestDepositExecutor_FixedDepositAction_DepositOnly(t *testing.T) {
	_, hex, agent := testKey(t)
	mock := newMock(84532)
	ex, err := execonchain.NewDepositExecutor(execonchain.Config{
		PrivateKeyHex: hex,
		RPCs:          map[string]string{baseSepCAIP2: "http://mock.local"},
		Dial:          dialMock(mock),
		WaitTimeout:   time.Second,
	})
	require.NoError(t, err)

	plan := fixedDepositPlan(t, agent, "1000")
	rcpt, err := ex.Execute(context.Background(), plan)
	require.NoError(t, err)
	require.Len(t, rcpt.TxHashes, 2)
	assert.Equal(t, 2, mock.sends)
}

func TestDepositExecutor_MaxAmountAtomic(t *testing.T) {
	_, hex, agent := testKey(t)
	mock := newMock(84532)
	ex, err := execonchain.NewDepositExecutor(execonchain.Config{
		PrivateKeyHex: hex,
		RPCs:          map[string]string{baseSepCAIP2: "http://mock.local"},
		Dial:          dialMock(mock),
		Guard:         &liquidity.Guard{MaxAmountAtomic: decimal.RequireFromString("100")},
	})
	require.NoError(t, err)
	plan := consolidatePlan(t, agent, "1000")
	_, err = ex.Execute(context.Background(), plan)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MaxAmountAtomic")
	assert.Zero(t, mock.sends)
}

func TestDepositExecutor_KeyMismatch(t *testing.T) {
	_, hex, _ := testKey(t)
	mock := newMock(84532)
	ex, err := execonchain.NewDepositExecutor(execonchain.Config{
		PrivateKeyHex: hex,
		RPCs:          map[string]string{baseSepCAIP2: "http://mock.local"},
		Dial:          dialMock(mock),
	})
	require.NoError(t, err)

	// Plan bound to a different agent address.
	plan := consolidatePlan(t, "0x1111111111111111111111111111111111111111", "1000")
	_, err = ex.Execute(context.Background(), plan)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
	assert.Contains(t, err.Error(), "agent_address must match")
	assert.Zero(t, mock.sends)
}

func TestDepositExecutor_ActionAllowlist_CCTPRefused(t *testing.T) {
	_, hex, agent := testKey(t)
	mock := newMock(84532)
	ex, err := execonchain.NewDepositExecutor(execonchain.Config{
		PrivateKeyHex: hex,
		RPCs:          map[string]string{baseSepCAIP2: "http://mock.local"},
		Dial:          dialMock(mock),
	})
	require.NoError(t, err)

	p := liquidity.Plan{
		Action: liquidity.ActionCCTPFast,
		Required: liquidity.Required{
			PayTo:      "0xMerchant000000000000000000000000000001",
			ChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC,
			AmountAtomic: decimal.RequireFromString("1"),
		},
		Steps: []liquidity.PlanStep{{
			Kind:           liquidity.StepKindCCTPBurn,
			FromChainCAIP2: baseSepCAIP2, ToChainCAIP2: baseSepCAIP2, Asset: baseSepUSDC,
			AmountAtomic:  decimal.RequireFromString("1"),
			Recipient:     agent,
			RecipientRole: liquidity.RecipientRoleAgentSelf,
		}},
	}
	p.BindAgent(agent)
	_, err = ex.Execute(context.Background(), p)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeLiquidityRailUnavailable, liqerr.CodeOf(err))
	assert.Contains(t, err.Error(), "action not supported")
}

func TestDepositExecutor_ChainIDMismatch(t *testing.T) {
	_, hex, agent := testKey(t)
	mock := newMock(1) // eth mainnet id while chain is base sepolia
	ex, err := execonchain.NewDepositExecutor(execonchain.Config{
		PrivateKeyHex: hex,
		RPCs:          map[string]string{baseSepCAIP2: "http://mock.local"},
		Dial:          dialMock(mock),
	})
	require.NoError(t, err)
	plan := consolidatePlan(t, agent, "1000")
	_, err = ex.Execute(context.Background(), plan)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeLiquidityRailUnavailable, liqerr.CodeOf(err))
	assert.Contains(t, err.Error(), "chain id mismatch")
	assert.Zero(t, mock.sends)
}

func TestDepositExecutor_PrepareCallsMutationRefused(t *testing.T) {
	_, hex, agent := testKey(t)
	mock := newMock(84532)
	ex, err := execonchain.NewDepositExecutor(execonchain.Config{
		PrivateKeyHex: hex,
		RPCs:          map[string]string{baseSepCAIP2: "http://mock.local"},
		Dial:          dialMock(mock),
	})
	require.NoError(t, err)
	plan := consolidatePlan(t, agent, "1000")
	require.NotEmpty(t, plan.Steps[0].PrepareCalls)
	// Mutate client-supplied calldata.
	plan.Steps[0].PrepareCalls[0].Data = "0xdeadbeef"

	_, err = ex.Execute(context.Background(), plan)
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
	assert.Contains(t, err.Error(), "prepare_calls mismatch")
	assert.Zero(t, mock.sends)
}

func TestDepositExecutor_PartialFailReturnsHashes(t *testing.T) {
	_, hex, agent := testKey(t)
	mock := newMock(84532)
	mock.sendFailAt = 2 // approve ok, deposit broadcast fails
	ex, err := execonchain.NewDepositExecutor(execonchain.Config{
		PrivateKeyHex: hex,
		RPCs:          map[string]string{baseSepCAIP2: "http://mock.local"},
		Dial:          dialMock(mock),
		WaitTimeout:   time.Second,
	})
	require.NoError(t, err)
	plan := consolidatePlan(t, agent, "1000")
	rcpt, err := ex.Execute(context.Background(), plan)
	require.Error(t, err)
	require.Len(t, rcpt.TxHashes, 1, "approve hash should be returned on partial fail")
	assert.Equal(t, liqerr.CodeLiquidityRailUnavailable, liqerr.CodeOf(err))
	// Fixed message — raw RPC string must not appear.
	assert.NotContains(t, err.Error(), "boom should not leak")
	assert.Contains(t, err.Error(), "broadcast failed")
}

func TestDepositExecutor_ReceiptRevertPartial(t *testing.T) {
	_, hex, agent := testKey(t)
	mock := newMock(84532)
	mock.receiptFail = 1 // first tx reverts
	ex, err := execonchain.NewDepositExecutor(execonchain.Config{
		PrivateKeyHex: hex,
		RPCs:          map[string]string{baseSepCAIP2: "http://mock.local"},
		Dial:          dialMock(mock),
		WaitTimeout:   time.Second,
	})
	require.NoError(t, err)
	plan := consolidatePlan(t, agent, "1000")
	rcpt, err := ex.Execute(context.Background(), plan)
	require.Error(t, err)
	require.Len(t, rcpt.TxHashes, 1)
	assert.Contains(t, err.Error(), "transaction failed")
}

func TestIsTestnetExecutableChain(t *testing.T) {
	assert.True(t, liquidity.IsTestnetExecutableChain(baseSepCAIP2))
	assert.True(t, liquidity.IsTestnetExecutableChain("eip155:5042002"))
	assert.False(t, liquidity.IsTestnetExecutableChain(baseMainnet))
	assert.False(t, liquidity.IsTestnetExecutableChain("eip155:1"))
	assert.False(t, liquidity.IsTestnetExecutableChain(""))
}

func TestBuildDepositPrepareCalls_MatchesAttached(t *testing.T) {
	step := liquidity.PlanStep{
		Kind:           liquidity.StepKindCircleGatewayDeposit,
		FromChainCAIP2: baseSepCAIP2,
		Asset:          baseSepUSDC,
		AmountAtomic:   decimal.RequireFromString("42"),
		Recipient:      "0xAgent",
		RecipientRole:  liquidity.RecipientRoleAgentSelf,
	}
	calls, err := liquidity.BuildDepositPrepareCalls(step)
	require.NoError(t, err)
	require.Len(t, calls, 2)
	assert.Equal(t, "approve", calls[0].Method)
	assert.Equal(t, "deposit", calls[1].Method)

	// Non-deposit → nil
	none, err := liquidity.BuildDepositPrepareCalls(liquidity.PlanStep{Kind: liquidity.StepKindNote})
	require.NoError(t, err)
	assert.Nil(t, none)
}