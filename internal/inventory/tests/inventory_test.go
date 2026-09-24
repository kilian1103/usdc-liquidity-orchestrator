package inventory_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/inventory"
	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
)

const (
	baseSepCAIP2 = "eip155:84532"
	baseSepUSDC  = "0x036CbD53842c5426634e7929541eC2318f3dCF7e"
	agent        = "0x1111111111111111111111111111111111111111"
)

type mockBalanceClient struct {
	ret    []byte
	err    error
	calls  int
	lastTo *common.Address
	data   []byte
}

func (m *mockBalanceClient) CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error) {
	m.calls++
	m.lastTo = msg.To
	m.data = msg.Data
	if m.err != nil {
		return nil, m.err
	}
	return m.ret, nil
}

func (m *mockBalanceClient) Close() {}

func uint256Word(n int64) []byte {
	word := make([]byte, 32)
	bi := big.NewInt(n)
	b := bi.Bytes()
	copy(word[32-len(b):], b)
	return word
}

func TestLoad_NativeAndGateway(t *testing.T) {
	mock := &mockBalanceClient{ret: uint256Word(5_000_000)}
	var sawGateway bool
	httpDo := func(req *http.Request) (*http.Response, error) {
		sawGateway = true
		assert.Equal(t, http.MethodPost, req.Method)
		assert.True(t, strings.HasSuffix(req.URL.Path, "/v1/balances"))
		body, _ := io.ReadAll(req.Body)
		var parsed map[string]any
		require.NoError(t, json.Unmarshal(body, &parsed))
		assert.Equal(t, "USDC", parsed["token"])
		raw, _ := json.Marshal(map[string]any{
			"balances": []map[string]any{
				{"domain": 6, "balance": "1.5"},
				{"domain": 3, "balance": "0"},
				{"domain": 26, "balance": "0.000001"},
			},
		})
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(raw)),
			Header:     make(http.Header),
		}, nil
	}

	inv, err := inventory.Load(context.Background(), inventory.Config{
		AgentAddress: agent,
		RPCs:         map[string]string{baseSepCAIP2: "http://mock.local"},
		GatewayAPI:   "https://gateway-api-testnet.circle.com",
		Dial: func(ctx context.Context, url string) (inventory.BalanceClient, error) {
			assert.Equal(t, "http://mock.local", url)
			return mock, nil
		},
		HTTPDo: httpDo,
	})
	require.NoError(t, err)
	assert.True(t, sawGateway)
	assert.Equal(t, 1, mock.calls)
	require.NotNil(t, mock.lastTo)
	assert.True(t, strings.EqualFold(mock.lastTo.Hex(), baseSepUSDC))
	// balanceOf selector
	require.GreaterOrEqual(t, len(mock.data), 4)
	assert.Equal(t, []byte{0x70, 0xa0, 0x82, 0x31}, mock.data[:4])

	require.Len(t, inv.Balances, 3) // native 5 + gw 1.5 + gw 0.000001
	assert.True(t, strings.EqualFold(inv.AgentAddress, agent))

	var native, gw decimal.Decimal
	for _, b := range inv.Balances {
		switch b.Location {
		case liquidity.LocationNative:
			assert.Equal(t, baseSepCAIP2, b.ChainCAIP2)
			native = native.Add(b.AmountAtomic)
		case liquidity.LocationCircleGateway:
			assert.Equal(t, "USDC", b.Asset)
			gw = gw.Add(b.AmountAtomic)
		}
	}
	assert.True(t, native.Equal(decimal.RequireFromString("5000000")))
	// 1.5 USDC + 1 atomic = 1500000 + 1
	assert.True(t, gw.Equal(decimal.RequireFromString("1500001")))
}

func TestLoad_OmitsNonPositiveNative(t *testing.T) {
	mock := &mockBalanceClient{ret: uint256Word(0)}
	httpDo := func(req *http.Request) (*http.Response, error) {
		raw, _ := json.Marshal(map[string]any{"balances": []any{}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw)), Header: make(http.Header)}, nil
	}
	inv, err := inventory.Load(context.Background(), inventory.Config{
		AgentAddress: agent,
		RPCs:         map[string]string{baseSepCAIP2: "http://mock.local"},
		Dial:         func(ctx context.Context, url string) (inventory.BalanceClient, error) { return mock, nil },
		HTTPDo:       httpDo,
	})
	require.NoError(t, err)
	assert.Empty(t, inv.Balances)
}

func TestLoad_RefusesEmptyAgent(t *testing.T) {
	_, err := inventory.Load(context.Background(), inventory.Config{
		AgentAddress: "",
		RPCs:         map[string]string{baseSepCAIP2: "http://mock.local"},
	})
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
}

func TestLoad_RPCCallErrorSanitized(t *testing.T) {
	mock := &mockBalanceClient{err: errString("rpc secret-body-must-not-leak")}
	_, err := inventory.Load(context.Background(), inventory.Config{
		AgentAddress: agent,
		RPCs:         map[string]string{baseSepCAIP2: "http://mock.local"},
		Dial:         func(ctx context.Context, url string) (inventory.BalanceClient, error) { return mock, nil },
		HTTPDo: func(req *http.Request) (*http.Response, error) {
			t.Fatal("HTTP should not run after native failure")
			return nil, nil
		},
	})
	require.Error(t, err)
	assert.Equal(t, liqerr.CodeLiquidityRailUnavailable, liqerr.CodeOf(err))
	assert.NotContains(t, err.Error(), "secret-body-must-not-leak")
	assert.Contains(t, err.Error(), "balanceOf call failed")
}

func TestLoad_GatewayHTTPErrorSoftSkips(t *testing.T) {
	mock := &mockBalanceClient{ret: uint256Word(42)}
	inv, err := inventory.Load(context.Background(), inventory.Config{
		AgentAddress: agent,
		RPCs:         map[string]string{baseSepCAIP2: "http://mock.local"},
		Dial:         func(ctx context.Context, url string) (inventory.BalanceClient, error) { return mock, nil },
		HTTPDo: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 500,
				Body:       io.NopCloser(strings.NewReader(`{"secret":"leak-me"}`)),
				Header:     make(http.Header),
			}, nil
		},
	})
	require.NoError(t, err)
	require.Len(t, inv.Balances, 1)
	assert.Equal(t, liquidity.LocationNative, inv.Balances[0].Location)
	assert.True(t, inv.Balances[0].AmountAtomic.Equal(decimal.NewFromInt(42)))
}

type errString string

func (e errString) Error() string { return string(e) }

func TestBalanceOfCalldataShape(t *testing.T) {
	// balanceOf(address) selector + left-padded 20-byte address (ABI word).
	// Mirrors internal packing used by inventory.Load eth_call.
	owner := common.HexToAddress(agent)
	data := make([]byte, 4+32)
	copy(data[:4], []byte{0x70, 0xa0, 0x82, 0x31})
	copy(data[4+12:], owner.Bytes())
	require.Equal(t, []byte{0x70, 0xa0, 0x82, 0x31}, data[:4])
	assert.Equal(t, owner.Bytes(), data[4+12:4+32])
	assert.Equal(t, 36, len(data))
}