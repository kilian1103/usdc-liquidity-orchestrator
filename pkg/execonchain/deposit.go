package execonchain

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
)

const defaultWaitTimeout = 2 * time.Minute

// Config configures a testnet-only DepositExecutor.
type Config struct {
	PrivateKeyHex string
	// RPCs maps CAIP-2 chain id → JSON-RPC URL. Keys must be testnet executable.
	RPCs  map[string]string
	Guard *liquidity.Guard
	// WaitTimeout is max wait per receipt (default 2m).
	WaitTimeout time.Duration
	// Dial creates a ChainClient for an RPC URL (default: DefaultDial).
	Dial func(ctx context.Context, rpcURL string) (ChainClient, error)
	// GatewayAPI is Circle Gateway API base (default testnet). Used for /v1/transfer.
	GatewayAPI string
	// HTTPDo performs Gateway HTTP (default: http.DefaultClient.Do).
	HTTPDo func(*http.Request) (*http.Response, error)
	// MaxFeeAtomic overrides burn-intent maxFee (default GATEWAY_MAX_FEE_ATOMIC or 2010000).
	MaxFeeAtomic *big.Int
	// TransferRetries is attempts for POST /v1/transfer after deposits (default 5).
	// Deposits need Gateway finality; retries with delay cover short finality waits.
	TransferRetries int
	// TransferRetryDelay between transfer attempts (default 2s).
	TransferRetryDelay time.Duration
	// SaltFn optional fixed salt for tests (returns 0x-prefixed 32-byte hex).
	SaltFn func() (string, error)
}

// DepositExecutor executes testnet Gateway plans: consolidate/deposit (deposits only)
// and withdraw (burn/mint only). Never composite deposit+withdraw.
type DepositExecutor struct {
	key                *ecdsa.PrivateKey
	addr               common.Address
	rpcs               map[string]string // lower-case CAIP-2 → URL
	guard              *liquidity.Guard
	waitTimeout        time.Duration
	dial               func(ctx context.Context, rpcURL string) (ChainClient, error)
	gatewayAPI         string
	httpDo             func(*http.Request) (*http.Response, error)
	maxFeeAtomic       *big.Int
	transferRetries    int
	transferRetryDelay time.Duration
	saltFn             func() (string, error)
	mu                 sync.Mutex
}

// NewDepositExecutor parses the key and validates RPC map keys (testnet only).
func NewDepositExecutor(cfg Config) (*DepositExecutor, error) {
	hexKey := strings.TrimSpace(cfg.PrivateKeyHex)
	hexKey = strings.TrimPrefix(hexKey, "0x")
	hexKey = strings.TrimPrefix(hexKey, "0X")
	if hexKey == "" {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: private key required")
	}
	key, err := crypto.HexToECDSA(hexKey)
	if err != nil {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: invalid private key")
	}
	pub, ok := key.Public().(*ecdsa.PublicKey)
	if !ok {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: invalid private key public type")
	}
	addr := crypto.PubkeyToAddress(*pub)

	if len(cfg.RPCs) == 0 {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: RPC map required")
	}
	rpcs := make(map[string]string, len(cfg.RPCs))
	for k, v := range cfg.RPCs {
		caip := strings.TrimSpace(k)
		url := strings.TrimSpace(v)
		if caip == "" || url == "" {
			return nil, liqerr.New(liqerr.CodeInvalidQuery,
				"deposit execute: empty RPC map entry refused")
		}
		if !liquidity.IsTestnetExecutableChain(caip) {
			return nil, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
				"deposit execute: RPC chain is not testnet-executable")
		}
		rpcs[strings.ToLower(caip)] = url
	}

	dial := cfg.Dial
	if dial == nil {
		dial = DefaultDial
	}
	wait := cfg.WaitTimeout
	if wait <= 0 {
		wait = defaultWaitTimeout
	}
	// Non-nil so CheckPlan always runs with default (empty) Guard policies.
	guard := cfg.Guard
	if guard == nil {
		guard = &liquidity.Guard{}
	}
	gwAPI := strings.TrimSpace(cfg.GatewayAPI)
	if gwAPI == "" {
		gwAPI = liquidity.GatewayAPITestnetBase
	}
	gwAPI = strings.TrimRight(gwAPI, "/")
	httpDo := cfg.HTTPDo
	if httpDo == nil {
		httpDo = http.DefaultClient.Do
	}
	maxFee := cfg.MaxFeeAtomic
	if maxFee == nil {
		maxFee = maxFeeFromEnv()
	}
	retries := cfg.TransferRetries
	if retries <= 0 {
		retries = 5
	}
	retryDelay := cfg.TransferRetryDelay
	if retryDelay <= 0 {
		retryDelay = 2 * time.Second
	}

	return &DepositExecutor{
		key:                key,
		addr:               addr,
		rpcs:               rpcs,
		guard:              guard,
		waitTimeout:        wait,
		dial:               dial,
		gatewayAPI:         gwAPI,
		httpDo:             httpDo,
		maxFeeAtomic:       maxFee,
		transferRetries:    retries,
		transferRetryDelay: retryDelay,
		saltFn:             cfg.SaltFn,
	}, nil
}

// Address returns the EOA derived from the configured private key.
func (e *DepositExecutor) Address() common.Address {
	if e == nil {
		return common.Address{}
	}
	return e.addr
}

// Execute runs a testnet Gateway plan. Supported actions:
//   - circle_gateway_consolidate — deposit steps only (re-derived prepare_calls)
//   - circle_gateway_deposit — deposit steps only (re-derived prepare_calls)
//   - circle_gateway_withdraw — burn intents + gatewayMint only (agent_self mint)
//
// Composite deposit+withdraw is refused. Partial failures return hashes broadcast
// so far plus a coded error (caller stamps executed=false). Never logs keys, balances, or calldata.
func (e *DepositExecutor) Execute(ctx context.Context, p liquidity.Plan) (liquidity.Receipt, error) {
	if e == nil {
		return liquidity.Receipt{}, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: executor not configured")
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.guard.CheckPlan(p); err != nil {
		return liquidity.Receipt{}, err
	}
	agent, err := e.validateAgentAndAction(p)
	if err != nil {
		return liquidity.Receipt{}, err
	}

	deposits, withdraws, err := splitGatewaySteps(p.Steps, agent)
	if err != nil {
		return liquidity.Receipt{}, err
	}
	if err := validateActionSteps(p.Action, deposits, withdraws); err != nil {
		return liquidity.Receipt{}, err
	}

	var hashes []string
	clients := map[string]ChainClient{}
	verified := map[string]bool{}
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()

	for i := range deposits {
		stepHashes, err := e.executeDepositStep(ctx, deposits[i], clients, verified)
		hashes = append(hashes, stepHashes...)
		if err != nil {
			return liquidity.Receipt{TxHashes: hashes}, err
		}
	}

	if p.Action == liquidity.ActionCircleGatewayConsolidate ||
		p.Action == liquidity.ActionCircleGatewayDeposit {
		return liquidity.Receipt{TxHashes: hashes}, nil
	}

	burnParams, err := burnParamsFromPlan(p.Action, deposits, withdraws, agent)
	if err != nil {
		return liquidity.Receipt{TxHashes: hashes}, err
	}
	// Withdraw-only plans omit from_chain; resolve burn sources from live Gateway balances
	// (unified inventory may cover shortfall while USDC sits on other domains).
	burnParams, err = e.resolveBurnSources(ctx, burnParams)
	if err != nil {
		return liquidity.Receipt{TxHashes: hashes}, err
	}
	mintHashes, err := e.executeBurnAndMint(ctx, burnParams, clients, verified)
	hashes = append(hashes, mintHashes...)
	if err != nil {
		return liquidity.Receipt{TxHashes: hashes}, err
	}
	return liquidity.Receipt{TxHashes: hashes}, nil
}

func (e *DepositExecutor) validateAgentAndAction(p liquidity.Plan) (agent string, err error) {
	agent = strings.TrimSpace(p.AgentAddress())
	if agent == "" || !strings.EqualFold(agent, e.addr.Hex()) {
		return "", liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: agent_address must match configured key")
	}
	switch p.Action {
	case liquidity.ActionCircleGatewayConsolidate,
		liquidity.ActionCircleGatewayDeposit,
		liquidity.ActionCircleGatewayWithdraw:
	default:
		return "", liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: action not supported for live execute")
	}
	if len(p.Steps) == 0 {
		return "", liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: plan has no steps")
	}
	return agent, nil
}

func splitGatewaySteps(steps []liquidity.PlanStep, agent string) (deposits, withdraws []liquidity.PlanStep, err error) {
	for i := range steps {
		kind := strings.ToLower(strings.TrimSpace(steps[i].Kind))
		switch kind {
		case liquidity.StepKindCircleGatewayDeposit:
			if !liquidity.IsTestnetExecutableChain(steps[i].FromChainCAIP2) {
				return nil, nil, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
					"deposit execute: chain is not testnet-executable")
			}
			deposits = append(deposits, steps[i])
		case liquidity.StepKindCircleGatewayWithdraw:
			if !liquidity.IsTestnetExecutableChain(steps[i].ToChainCAIP2) {
				return nil, nil, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
					"deposit execute: chain is not testnet-executable")
			}
			if !strings.EqualFold(strings.TrimSpace(steps[i].Recipient), agent) {
				return nil, nil, liqerr.New(liqerr.CodeInvalidQuery,
					"deposit execute: destinationRecipient must be agent_self")
			}
			withdraws = append(withdraws, steps[i])
		default:
			return nil, nil, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
				"deposit execute: unsupported step kind for live execute")
		}
	}
	return deposits, withdraws, nil
}

func validateActionSteps(action liquidity.PlanAction, deposits, withdraws []liquidity.PlanStep) error {
	switch action {
	case liquidity.ActionCircleGatewayConsolidate, liquidity.ActionCircleGatewayDeposit:
		if len(withdraws) > 0 || len(deposits) == 0 {
			return liqerr.New(liqerr.CodeInvalidQuery,
				"deposit execute: deposit action requires deposit steps only")
		}
	case liquidity.ActionCircleGatewayWithdraw:
		if len(deposits) > 0 || len(withdraws) == 0 {
			return liqerr.New(liqerr.CodeInvalidQuery,
				"deposit execute: withdraw action requires withdraw steps only")
		}
	default:
		return liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: action not supported for live execute")
	}
	return nil
}

// burnParamsFromPlan maps withdraw-only steps to burn/mint params.
// destinationRecipient is always the agent. Composite deposit+withdraw is not supported.
func burnParamsFromPlan(
	action liquidity.PlanAction,
	deposits, withdraws []liquidity.PlanStep,
	agent string,
) ([]burnMintParams, error) {
	_ = action
	if len(deposits) > 0 {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: withdraw burn/mint refuses deposit steps (Phase A then Phase B)")
	}
	if len(withdraws) == 0 {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: missing withdraw step for burn/mint")
	}
	// Single dest withdraw is the common case.
	dest := withdraws[0].ToChainCAIP2
	for _, w := range withdraws {
		if !strings.EqualFold(strings.TrimSpace(w.ToChainCAIP2), strings.TrimSpace(dest)) {
			return nil, liqerr.New(liqerr.CodeInvalidQuery,
				"deposit execute: multiple withdraw destinations not supported")
		}
		if !strings.EqualFold(strings.TrimSpace(w.Recipient), agent) {
			return nil, liqerr.New(liqerr.CodeInvalidQuery,
				"deposit execute: destinationRecipient must be agent_self")
		}
	}

	// Withdraw-only: Empty FromChainCAIP2 is resolved later via Gateway /v1/balances
	// (do not assume same-domain as dest — shortfall plans often mint to dest while
	// balance lives on other Gateway domains).
	out := make([]burnMintParams, 0, len(withdraws))
	for _, w := range withdraws {
		out = append(out, burnMintParams{
			SourceChainCAIP2: strings.TrimSpace(w.FromChainCAIP2),
			DestChainCAIP2:   w.ToChainCAIP2,
			ValueAtomic:      w.AmountAtomic,
			Recipient:        agent,
		})
	}
	return out, nil
}

func (e *DepositExecutor) executeDepositStep(
	ctx context.Context,
	s liquidity.PlanStep,
	clients map[string]ChainClient,
	verified map[string]bool,
) ([]string, error) {
	derived, err := liquidity.BuildDepositPrepareCalls(s)
	if err != nil {
		return nil, err
	}
	if len(derived) == 0 {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: no prepare_calls for deposit step")
	}
	if len(s.PrepareCalls) > 0 {
		if !prepareCallsExactMatch(s.PrepareCalls, derived) {
			return nil, liqerr.New(liqerr.CodeInvalidQuery,
				"deposit execute: prepare_calls mismatch")
		}
	}

	chainKey := strings.ToLower(strings.TrimSpace(s.FromChainCAIP2))
	cli, err := e.clientFor(ctx, chainKey, clients)
	if err != nil {
		return nil, err
	}
	if !verified[chainKey] {
		if err := verifyChainID(ctx, cli, s.FromChainCAIP2); err != nil {
			return nil, err
		}
		verified[chainKey] = true
	}

	var hashes []string
	for _, call := range derived {
		h, err := e.broadcastCall(ctx, cli, call)
		if h != "" {
			hashes = append(hashes, h)
		}
		if err != nil {
			return hashes, err
		}
	}
	return hashes, nil
}

func (e *DepositExecutor) clientFor(
	ctx context.Context,
	chainKey string,
	clients map[string]ChainClient,
) (ChainClient, error) {
	if c, ok := clients[chainKey]; ok {
		return c, nil
	}
	url, ok := e.rpcs[chainKey]
	if !ok {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: no RPC configured for chain")
	}
	c, err := e.dial(ctx, url)
	if err != nil {
		return nil, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: RPC dial failed")
	}
	clients[chainKey] = c
	return c, nil
}

func verifyChainID(ctx context.Context, cli ChainClient, caip2 string) error {
	want, err := parseEIP155ChainID(caip2)
	if err != nil {
		return err
	}
	got, err := cli.ChainID(ctx)
	if err != nil {
		return liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: chain id query failed")
	}
	if got == nil || got.Cmp(want) != 0 {
		return liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: chain id mismatch")
	}
	return nil
}

func parseEIP155ChainID(caip2 string) (*big.Int, error) {
	s := strings.TrimSpace(caip2)
	const prefix = "eip155:"
	if !strings.HasPrefix(strings.ToLower(s), prefix) {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: chain must be eip155 CAIP-2")
	}
	// Preserve case-insensitive prefix strip.
	idx := strings.Index(strings.ToLower(s), prefix)
	num := strings.TrimSpace(s[idx+len(prefix):])
	n, ok := new(big.Int).SetString(num, 10)
	if !ok || n.Sign() <= 0 {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: invalid eip155 chain id")
	}
	return n, nil
}

func (e *DepositExecutor) broadcastCall(ctx context.Context, cli ChainClient, call liquidity.PrepareCall) (string, error) {
	to, data, err := decodePrepareCall(call)
	if err != nil {
		return "", err
	}
	chainID, err := cli.ChainID(ctx)
	if err != nil {
		return "", liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: chain id query failed")
	}
	nonce, err := cli.PendingNonceAt(ctx, e.addr)
	if err != nil {
		return "", liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: nonce query failed")
	}
	tip, feeCap, err := eip1559Fees(ctx, cli)
	if err != nil {
		return "", err
	}
	msg := ethereum.CallMsg{
		From: e.addr, To: &to, Data: data, Value: big.NewInt(0),
		GasFeeCap: feeCap, GasTipCap: tip,
	}
	gasLimit, err := cli.EstimateGas(ctx, msg)
	if err != nil {
		// Fall back to a conservative limit if estimate fails (still fixed error on send/receipt).
		gasLimit = 200_000
	}
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     nonce,
		GasTipCap: tip,
		GasFeeCap: feeCap,
		Gas:       gasLimit,
		To:        &to,
		Value:     big.NewInt(0),
		Data:      data,
	})
	signer := types.LatestSignerForChainID(chainID)
	signed, err := types.SignTx(tx, signer, e.key)
	if err != nil {
		return "", liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: sign failed")
	}
	if err := cli.SendTransaction(ctx, signed); err != nil {
		return "", liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: broadcast failed")
	}
	hash := signed.Hash().Hex()
	if err := e.waitReceiptOK(ctx, cli, signed.Hash()); err != nil {
		// Hash was broadcast; return it with the error for partial stamping.
		return hash, err
	}
	return hash, nil
}

// eip1559Fees returns tip + feeCap (2*baseFee + tip) for DynamicFeeTx.
// Base/Arb Sepolia and other post-London nets reject legacy gasPrice txs.
func eip1559Fees(ctx context.Context, cli ChainClient) (tip, feeCap *big.Int, err error) {
	tip, err = cli.SuggestGasTipCap(ctx)
	if err != nil {
		return nil, nil, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: gas tip query failed")
	}
	if tip == nil || tip.Sign() < 0 {
		return nil, nil, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: gas tip query failed")
	}
	header, err := cli.HeaderByNumber(ctx, nil)
	if err != nil || header == nil || header.BaseFee == nil {
		return nil, nil, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: base fee query failed")
	}
	// feeCap = 2*baseFee + tip — headroom for one base-fee doubling before inclusion.
	feeCap = new(big.Int).Mul(header.BaseFee, big.NewInt(2))
	feeCap.Add(feeCap, tip)
	return tip, feeCap, nil
}

func (e *DepositExecutor) waitReceiptOK(ctx context.Context, cli ChainClient, hash common.Hash) error {
	deadline := time.Now().Add(e.waitTimeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return liqerr.New(liqerr.CodeLiquidityRailUnavailable,
				"deposit execute: wait cancelled")
		}
		rcpt, err := cli.TransactionReceipt(ctx, hash)
		if err == nil && rcpt != nil {
			if rcpt.Status != types.ReceiptStatusSuccessful {
				return liqerr.New(liqerr.CodeLiquidityRailUnavailable,
					"deposit execute: transaction failed")
			}
			return nil
		}
		if time.Now().After(deadline) {
			return liqerr.New(liqerr.CodeLiquidityRailUnavailable,
				"deposit execute: receipt wait timeout")
		}
		select {
		case <-ctx.Done():
			return liqerr.New(liqerr.CodeLiquidityRailUnavailable,
				"deposit execute: wait cancelled")
		case <-ticker.C:
		}
	}
}

func decodePrepareCall(call liquidity.PrepareCall) (common.Address, []byte, error) {
	toStr := strings.TrimSpace(call.To)
	if !common.IsHexAddress(toStr) {
		return common.Address{}, nil, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: invalid prepare call to address")
	}
	to := common.HexToAddress(toStr)
	dataHex := strings.TrimSpace(call.Data)
	dataHex = strings.TrimPrefix(dataHex, "0x")
	dataHex = strings.TrimPrefix(dataHex, "0X")
	if dataHex == "" {
		return common.Address{}, nil, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: empty prepare call data")
	}
	data, err := hex.DecodeString(dataHex)
	if err != nil {
		return common.Address{}, nil, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: invalid prepare call data")
	}
	return to, data, nil
}

func prepareCallsExactMatch(client, derived []liquidity.PrepareCall) bool {
	if len(client) != len(derived) {
		return false
	}
	for i := range client {
		c, d := client[i], derived[i]
		if !strings.EqualFold(strings.TrimSpace(c.ChainCAIP2), strings.TrimSpace(d.ChainCAIP2)) {
			return false
		}
		if !strings.EqualFold(strings.TrimSpace(c.To), strings.TrimSpace(d.To)) {
			return false
		}
		if !strings.EqualFold(strings.TrimSpace(c.Data), strings.TrimSpace(d.Data)) {
			return false
		}
		if strings.TrimSpace(c.Value) != strings.TrimSpace(d.Value) {
			return false
		}
		if strings.TrimSpace(c.Method) != strings.TrimSpace(d.Method) {
			return false
		}
	}
	return true
}