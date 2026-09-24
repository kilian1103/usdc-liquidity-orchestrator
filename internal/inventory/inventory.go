// Package inventory loads request-scoped agent USDC balances from chain RPCs
// and optional Circle Gateway /v1/balances. Never logs balances, keys, or RPC URLs.
package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/shopspring/decimal"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
)

// MVP Gateway domains for POST /v1/balances (Arbitrum=3, Base=6, Arc=26).
var mvpGatewayDomains = []int{3, 6, 26}

// balanceOf(address) selector.
var selectorBalanceOf = []byte{0x70, 0xa0, 0x82, 0x31}

// BalanceClient is the minimal eth_call surface (mockable).
type BalanceClient interface {
	CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
	Close()
}

// Config configures live inventory load.
type Config struct {
	AgentAddress string
	RPCs         map[string]string // CAIP-2 → URL
	GatewayAPI   string            // default liquidity.GatewayAPITestnetBase
	// Dial creates a BalanceClient for an RPC URL (default: ethclient dial).
	Dial func(ctx context.Context, rpcURL string) (BalanceClient, error)
	// HTTPDo performs Gateway API HTTP (default: http.DefaultClient.Do).
	HTTPDo func(*http.Request) (*http.Response, error)
}

// Load returns Inventory with native USDC balanceOf rows plus optional
// circle_gateway rows from Gateway POST /v1/balances.
// inventory is always client-asserted for plan stamps — callers keep inventory_unverified.
// Errors use fixed codes only (no raw RPC / HTTP bodies).
func Load(ctx context.Context, cfg Config) (liquidity.Inventory, error) {
	agent := strings.TrimSpace(cfg.AgentAddress)
	if agent == "" || !common.IsHexAddress(agent) {
		return liquidity.Inventory{}, liqerr.New(liqerr.CodeInvalidQuery,
			"inventory: agent_address required (valid EVM hex)")
	}
	if len(cfg.RPCs) == 0 {
		return liquidity.Inventory{}, liqerr.New(liqerr.CodeInvalidQuery,
			"inventory: RPC map required")
	}

	dial := cfg.Dial
	if dial == nil {
		dial = defaultDial
	}
	httpDo := cfg.HTTPDo
	if httpDo == nil {
		httpDo = http.DefaultClient.Do
	}
	gwBase := strings.TrimSpace(cfg.GatewayAPI)
	if gwBase == "" {
		gwBase = liquidity.GatewayAPITestnetBase
	}
	gwBase = strings.TrimRight(gwBase, "/")

	agentAddr := common.HexToAddress(agent)
	var bals []liquidity.Balance

	for caip, url := range cfg.RPCs {
		caip = strings.TrimSpace(caip)
		url = strings.TrimSpace(url)
		if caip == "" || url == "" {
			return liquidity.Inventory{}, liqerr.New(liqerr.CodeInvalidQuery,
				"inventory: empty RPC map entry refused")
		}
		info, ok := liquidity.LookupChain(caip)
		if !ok || info.USDC == "" {
			continue
		}
		amt, err := readUSDCBalance(ctx, dial, url, info.USDC, agentAddr)
		if err != nil {
			return liquidity.Inventory{}, err
		}
		if !amt.IsPositive() {
			continue
		}
		bals = append(bals, liquidity.Balance{
			ChainCAIP2:   info.CAIP2,
			Asset:        info.USDC,
			AmountAtomic: amt,
			Location:     liquidity.LocationNative,
		})
	}

	// Gateway rows are optional: soft-skip API failures so native inventory still returns.
	if gwBals, err := loadGatewayBalances(ctx, httpDo, gwBase, agent); err == nil {
		bals = append(bals, gwBals...)
	}

	return liquidity.Inventory{
		AgentAddress: agent,
		Balances:     bals,
	}, nil
}

func defaultDial(ctx context.Context, rpcURL string) (BalanceClient, error) {
	c, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return nil, err
	}
	return &ethBalanceClient{c: c}, nil
}

type ethBalanceClient struct {
	c *ethclient.Client
}

func (e *ethBalanceClient) CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error) {
	return e.c.CallContract(ctx, msg, blockNumber)
}

func (e *ethBalanceClient) Close() {
	e.c.Close()
}

func readUSDCBalance(
	ctx context.Context,
	dial func(context.Context, string) (BalanceClient, error),
	rpcURL, usdc string,
	owner common.Address,
) (decimal.Decimal, error) {
	cli, err := dial(ctx, rpcURL)
	if err != nil {
		return decimal.Zero, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"inventory: RPC dial failed")
	}
	defer cli.Close()

	data := make([]byte, 4+32)
	copy(data[:4], selectorBalanceOf)
	copy(data[4+12:], owner.Bytes())

	to := common.HexToAddress(usdc)
	out, err := cli.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
	if err != nil {
		return decimal.Zero, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"inventory: balanceOf call failed")
	}
	if len(out) < 32 {
		return decimal.Zero, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"inventory: balanceOf response invalid")
	}
	bi := new(big.Int).SetBytes(out[len(out)-32:])
	if bi.Sign() < 0 {
		return decimal.Zero, liqerr.New(liqerr.CodeInvalidQuery,
			"inventory: negative balance refused")
	}
	return decimal.NewFromBigInt(bi, 0), nil
}

type gatewayBalancesReq struct {
	Token   string              `json:"token"`
	Sources []gatewayBalanceSrc `json:"sources"`
}

type gatewayBalanceSrc struct {
	Domain    int    `json:"domain"`
	Depositor string `json:"depositor"`
}

type gatewayBalancesResp struct {
	Balances []struct {
		Domain  int    `json:"domain"`
		Balance string `json:"balance"`
	} `json:"balances"`
}

func loadGatewayBalances(
	ctx context.Context,
	httpDo func(*http.Request) (*http.Response, error),
	base, depositor string,
) ([]liquidity.Balance, error) {
	sources := make([]gatewayBalanceSrc, 0, len(mvpGatewayDomains))
	for _, d := range mvpGatewayDomains {
		sources = append(sources, gatewayBalanceSrc{Domain: d, Depositor: depositor})
	}
	body, err := json.Marshal(gatewayBalancesReq{Token: "USDC", Sources: sources})
	if err != nil {
		return nil, liqerr.New(liqerr.CodeInvalidQuery, "inventory: gateway balances encode failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/balances", bytes.NewReader(body))
	if err != nil {
		return nil, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"inventory: gateway balances request failed")
	}
	req.Header.Set("Content-Type", "application/json")

	// Bound hung HTTP without requiring callers to set timeout on DefaultClient.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		req = req.WithContext(ctx)
	}

	resp, err := httpDo(req)
	if err != nil {
		return nil, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"inventory: gateway balances request failed")
	}
	defer resp.Body.Close()
	// Drain limited body; never surface raw payload.
	limited := io.LimitReader(resp.Body, 1<<20)
	raw, _ := io.ReadAll(limited)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"inventory: gateway balances HTTP %d", resp.StatusCode)
	}
	var parsed gatewayBalancesResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"inventory: gateway balances response invalid")
	}
	var out []liquidity.Balance
	for _, row := range parsed.Balances {
		amt, err := humanUSDCToAtomic(row.Balance)
		if err != nil || !amt.IsPositive() {
			continue
		}
		// Unified gateway balance: Asset "USDC"; optional chain stamp from testnet registry.
		b := liquidity.Balance{
			Asset:        "USDC",
			AmountAtomic: amt,
			Location:     liquidity.LocationCircleGateway,
		}
		if caip, ok := testnetCAIP2ForDomain(row.Domain); ok {
			b.ChainCAIP2 = caip
		}
		out = append(out, b)
	}
	return out, nil
}

func humanUSDCToAtomic(s string) (decimal.Decimal, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return decimal.Zero, fmt.Errorf("empty")
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, err
	}
	if d.IsNegative() {
		return decimal.Zero, fmt.Errorf("negative")
	}
	// USDC 6 decimals; truncate fractional dust below atomic.
	return d.Mul(decimal.NewFromInt(1_000_000)).Truncate(0), nil
}

func testnetCAIP2ForDomain(domain int) (string, bool) {
	for _, c := range liquidity.ListChains() {
		if c.GatewayDomain == domain && c.Testnet && c.GatewayOK {
			return c.CAIP2, true
		}
	}
	return "", false
}