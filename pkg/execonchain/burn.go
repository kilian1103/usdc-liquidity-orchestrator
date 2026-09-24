package execonchain

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
	"github.com/shopspring/decimal"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
)

// Default max fee for burn intents (2.01 USDC atomic). Override via GATEWAY_MAX_FEE_ATOMIC.
const defaultMaxFeeAtomic int64 = 2_010_000

var (
	// maxUint256 for burn intent maxBlockHeight.
	maxUint256 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	// gatewayMint(bytes,bytes) selector.
	selectorGatewayMint = crypto.Keccak256([]byte("gatewayMint(bytes,bytes)"))[:4]
)

// burnIntentMessage is the EIP-712 BurnIntent message (also posted to /v1/transfer).
type burnIntentMessage struct {
	MaxBlockHeight string              `json:"maxBlockHeight"`
	MaxFee         string              `json:"maxFee"`
	Spec           transferSpecMessage `json:"spec"`
}

type transferSpecMessage struct {
	Version              interface{} `json:"version"`
	SourceDomain         interface{} `json:"sourceDomain"`
	DestinationDomain    interface{} `json:"destinationDomain"`
	SourceContract       string      `json:"sourceContract"`
	DestinationContract  string      `json:"destinationContract"`
	SourceToken          string      `json:"sourceToken"`
	DestinationToken     string      `json:"destinationToken"`
	SourceDepositor      string      `json:"sourceDepositor"`
	DestinationRecipient string      `json:"destinationRecipient"`
	SourceSigner         string      `json:"sourceSigner"`
	DestinationCaller    string      `json:"destinationCaller"`
	Value                string      `json:"value"`
	Salt                 string      `json:"salt"`
	HookData             string      `json:"hookData"`
}

type transferRequestItem struct {
	BurnIntent burnIntentMessage `json:"burnIntent"`
	Signature  string            `json:"signature"`
}

type transferResponse struct {
	Attestation string `json:"attestation"`
	Signature   string `json:"signature"`
}

// burnMintParams describes one deposit-sourced burn + mint on dest.
type burnMintParams struct {
	SourceChainCAIP2 string
	DestChainCAIP2   string
	ValueAtomic      decimal.Decimal
	Recipient        string // must be agent
}

func (e *DepositExecutor) executeBurnAndMint(
	ctx context.Context,
	params []burnMintParams,
	clients map[string]ChainClient,
	verified map[string]bool,
) ([]string, error) {
	if len(params) == 0 {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: no burn/mint params")
	}
	agentHex := e.addr.Hex()
	var hashes []string
	for _, p := range params {
		if !strings.EqualFold(strings.TrimSpace(p.Recipient), agentHex) {
			return hashes, liqerr.New(liqerr.CodeInvalidQuery,
				"deposit execute: destinationRecipient must be agent_self")
		}
		if !liquidity.IsTestnetExecutableChain(p.SourceChainCAIP2) ||
			!liquidity.IsTestnetExecutableChain(p.DestChainCAIP2) {
			return hashes, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
				"deposit execute: burn/mint chain is not testnet-executable")
		}
		attestation, opSig, err := e.signAndTransferWithRetry(ctx, p)
		if err != nil {
			return hashes, err
		}
		h, err := e.gatewayMint(ctx, p.DestChainCAIP2, attestation, opSig, clients, verified)
		if h != "" {
			hashes = append(hashes, h)
		}
		if err != nil {
			return hashes, err
		}
	}
	return hashes, nil
}

func (e *DepositExecutor) signAndTransferWithRetry(ctx context.Context, p burnMintParams) (attestation, opSig string, err error) {
	retries := e.transferRetries
	if retries <= 0 {
		retries = 5
	}
	delay := e.transferRetryDelay
	if delay <= 0 {
		delay = 2 * time.Second
	}
	var last error
	for attempt := 0; attempt < retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", "", liqerr.New(liqerr.CodeLiquidityRailUnavailable,
				"deposit execute: transfer cancelled")
		}
		attestation, opSig, last = e.signAndTransferOnce(ctx, p)
		if last == nil {
			return attestation, opSig, nil
		}
		if attempt+1 < retries {
			select {
			case <-ctx.Done():
				return "", "", liqerr.New(liqerr.CodeLiquidityRailUnavailable,
					"deposit execute: transfer cancelled")
			case <-time.After(delay):
			}
		}
	}
	return "", "", last
}

func (e *DepositExecutor) signAndTransferOnce(ctx context.Context, p burnMintParams) (string, string, error) {
	msg, err := e.buildBurnIntentMessage(p)
	if err != nil {
		return "", "", err
	}
	sig, err := e.signBurnIntent(msg)
	if err != nil {
		return "", "", err
	}
	return e.postTransfer(ctx, msg, sig)
}

func (e *DepositExecutor) buildBurnIntentMessage(p burnMintParams) (burnIntentMessage, error) {
	srcInfo, ok := liquidity.LookupChain(p.SourceChainCAIP2)
	if !ok || !srcInfo.GatewayOK {
		return burnIntentMessage{}, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: unknown source chain for burn intent")
	}
	destInfo, ok := liquidity.LookupChain(p.DestChainCAIP2)
	if !ok || !destInfo.GatewayOK {
		return burnIntentMessage{}, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: unknown dest chain for burn intent")
	}
	srcUSDC, ok := liquidity.DefaultUSDC(p.SourceChainCAIP2)
	if !ok {
		return burnIntentMessage{}, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: no USDC for source chain")
	}
	destUSDC, ok := liquidity.DefaultUSDC(p.DestChainCAIP2)
	if !ok {
		return burnIntentMessage{}, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: no USDC for dest chain")
	}
	if !p.ValueAtomic.IsPositive() || !p.ValueAtomic.Equal(p.ValueAtomic.Truncate(0)) {
		return burnIntentMessage{}, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: burn value must be positive whole atomic units")
	}

	wallet := liquidity.GatewayWalletTestnet
	minter := liquidity.GatewayMinterTestnet
	agent := e.addr.Hex()
	salt, err := e.newSalt()
	if err != nil {
		return burnIntentMessage{}, err
	}
	maxFee := e.maxFeeAtomic
	if maxFee == nil || maxFee.Sign() <= 0 {
		maxFee = big.NewInt(defaultMaxFeeAtomic)
	}
	// Circle rejects burns when maxFee >= value (fee cannot consume the full burn).
	maxFee = capMaxFee(maxFee, p.ValueAtomic)

	return burnIntentMessage{
		MaxBlockHeight: maxUint256.String(),
		MaxFee:         maxFee.String(),
		Spec: transferSpecMessage{
			Version:              1,
			SourceDomain:         srcInfo.GatewayDomain,
			DestinationDomain:    destInfo.GatewayDomain,
			SourceContract:       addressToBytes32(wallet),
			DestinationContract:  addressToBytes32(minter),
			SourceToken:          addressToBytes32(srcUSDC),
			DestinationToken:     addressToBytes32(destUSDC),
			SourceDepositor:      addressToBytes32(agent),
			DestinationRecipient: addressToBytes32(p.Recipient),
			SourceSigner:         addressToBytes32(agent),
			DestinationCaller:    addressToBytes32(common.Address{}.Hex()),
			Value:                p.ValueAtomic.String(),
			Salt:                 salt,
			HookData:             "0x",
		},
	}, nil
}

func (e *DepositExecutor) newSalt() (string, error) {
	if e.saltFn != nil {
		return e.saltFn()
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: salt generation failed")
	}
	return "0x" + hex.EncodeToString(b[:]), nil
}

func (e *DepositExecutor) signBurnIntent(msg burnIntentMessage) (string, error) {
	typed := apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": {
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
			},
			"TransferSpec": {
				{Name: "version", Type: "uint32"},
				{Name: "sourceDomain", Type: "uint32"},
				{Name: "destinationDomain", Type: "uint32"},
				{Name: "sourceContract", Type: "bytes32"},
				{Name: "destinationContract", Type: "bytes32"},
				{Name: "sourceToken", Type: "bytes32"},
				{Name: "destinationToken", Type: "bytes32"},
				{Name: "sourceDepositor", Type: "bytes32"},
				{Name: "destinationRecipient", Type: "bytes32"},
				{Name: "sourceSigner", Type: "bytes32"},
				{Name: "destinationCaller", Type: "bytes32"},
				{Name: "value", Type: "uint256"},
				{Name: "salt", Type: "bytes32"},
				{Name: "hookData", Type: "bytes"},
			},
			"BurnIntent": {
				{Name: "maxBlockHeight", Type: "uint256"},
				{Name: "maxFee", Type: "uint256"},
				{Name: "spec", Type: "TransferSpec"},
			},
		},
		PrimaryType: "BurnIntent",
		Domain: apitypes.TypedDataDomain{
			Name:    "GatewayWallet",
			Version: "1",
		},
		Message: apitypes.TypedDataMessage{
			"maxBlockHeight": msg.MaxBlockHeight,
			"maxFee":         msg.MaxFee,
			"spec": map[string]interface{}{
				"version":              fmt.Sprint(msg.Spec.Version),
				"sourceDomain":         fmt.Sprint(msg.Spec.SourceDomain),
				"destinationDomain":    fmt.Sprint(msg.Spec.DestinationDomain),
				"sourceContract":       msg.Spec.SourceContract,
				"destinationContract":  msg.Spec.DestinationContract,
				"sourceToken":          msg.Spec.SourceToken,
				"destinationToken":     msg.Spec.DestinationToken,
				"sourceDepositor":      msg.Spec.SourceDepositor,
				"destinationRecipient": msg.Spec.DestinationRecipient,
				"sourceSigner":         msg.Spec.SourceSigner,
				"destinationCaller":    msg.Spec.DestinationCaller,
				"value":                msg.Spec.Value,
				"salt":                 msg.Spec.Salt,
				"hookData":             msg.Spec.HookData,
			},
		},
	}
	hash, _, err := apitypes.TypedDataAndHash(typed)
	if err != nil {
		return "", liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: burn intent hash failed")
	}
	sig, err := crypto.Sign(hash, e.key)
	if err != nil {
		return "", liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: burn intent sign failed")
	}
	// crypto.Sign returns v as 0/1; Ethereum typed-data expects 27/28.
	if sig[64] < 27 {
		sig[64] += 27
	}
	return "0x" + hex.EncodeToString(sig), nil
}

func (e *DepositExecutor) postTransfer(ctx context.Context, msg burnIntentMessage, signature string) (string, string, error) {
	payload := []transferRequestItem{{BurnIntent: msg, Signature: signature}}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", "", liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: transfer encode failed")
	}
	url := e.gatewayAPI + "/v1/transfer"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", "", liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: transfer request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.httpDo(req)
	if err != nil {
		return "", "", liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: transfer request failed")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: transfer HTTP %d%s", resp.StatusCode, circleAPIErrorSuffix(raw))
	}
	var parsed transferResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		// Circle may return success:false with 200 in some paths; still refuse empty attestation.
		if suf := circleAPIErrorSuffix(raw); suf != "" {
			return "", "", liqerr.New(liqerr.CodeLiquidityRailUnavailable,
				"deposit execute: transfer failed%s", suf)
		}
		return "", "", liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: transfer response invalid")
	}
	att := strings.TrimSpace(parsed.Attestation)
	sig := strings.TrimSpace(parsed.Signature)
	if att == "" || sig == "" {
		return "", "", liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: transfer missing attestation")
	}
	return att, sig, nil
}

func (e *DepositExecutor) gatewayMint(
	ctx context.Context,
	destCAIP2 string,
	attestation, opSig string,
	clients map[string]ChainClient,
	verified map[string]bool,
) (string, error) {
	data, err := packGatewayMint(attestation, opSig)
	if err != nil {
		return "", err
	}
	chainKey := strings.ToLower(strings.TrimSpace(destCAIP2))
	cli, err := e.clientFor(ctx, chainKey, clients)
	if err != nil {
		return "", err
	}
	if !verified[chainKey] {
		if err := verifyChainID(ctx, cli, destCAIP2); err != nil {
			return "", err
		}
		verified[chainKey] = true
	}
	call := liquidity.PrepareCall{
		ChainCAIP2: destCAIP2,
		To:         liquidity.GatewayMinterTestnet,
		Data:       "0x" + hex.EncodeToString(data),
		Value:      "0",
		Method:     "gatewayMint",
	}
	return e.broadcastCall(ctx, cli, call)
}

func packGatewayMint(attestationHex, signatureHex string) ([]byte, error) {
	att, err := decodeHexBytes(attestationHex)
	if err != nil {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: invalid attestation hex")
	}
	sig, err := decodeHexBytes(signatureHex)
	if err != nil {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: invalid operator signature hex")
	}
	// ABI: gatewayMint(bytes attestation, bytes signature)
	// head: offset0=0x40, offset1=0x40+32+pad(len(att))
	pad := func(b []byte) []byte {
		n := ((len(b) + 31) / 32) * 32
		if n == len(b) {
			return b
		}
		out := make([]byte, n)
		copy(out, b)
		return out
	}
	encDyn := func(b []byte) []byte {
		word := make([]byte, 32)
		new(big.Int).SetInt64(int64(len(b))).FillBytes(word)
		return append(word, pad(b)...)
	}
	off0 := 64
	enc0 := encDyn(att)
	off1 := off0 + len(enc0)
	enc1 := encDyn(sig)

	out := make([]byte, 0, 4+64+len(enc0)+len(enc1))
	out = append(out, selectorGatewayMint...)
	w0 := make([]byte, 32)
	new(big.Int).SetInt64(int64(off0)).FillBytes(w0)
	w1 := make([]byte, 32)
	new(big.Int).SetInt64(int64(off1)).FillBytes(w1)
	out = append(out, w0...)
	out = append(out, w1...)
	out = append(out, enc0...)
	out = append(out, enc1...)
	return out, nil
}

func decodeHexBytes(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")
	if s == "" {
		return []byte{}, nil
	}
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("odd hex length")
	}
	return hex.DecodeString(s)
}

func addressToBytes32(addr string) string {
	a := strings.TrimSpace(addr)
	a = strings.TrimPrefix(a, "0x")
	a = strings.TrimPrefix(a, "0X")
	a = strings.ToLower(a)
	if len(a) > 64 {
		a = a[len(a)-64:]
	}
	return "0x" + strings.Repeat("0", 64-len(a)) + a
}

func maxFeeFromEnv() *big.Int {
	raw := strings.TrimSpace(os.Getenv("GATEWAY_MAX_FEE_ATOMIC"))
	if raw == "" {
		return big.NewInt(defaultMaxFeeAtomic)
	}
	d, err := decimal.NewFromString(raw)
	if err != nil || !d.IsPositive() || !d.Equal(d.Truncate(0)) {
		return big.NewInt(defaultMaxFeeAtomic)
	}
	bi, ok := new(big.Int).SetString(d.String(), 10)
	if !ok {
		return big.NewInt(defaultMaxFeeAtomic)
	}
	return bi
}

// capMaxFee ensures maxFee < burn value (atomic USDC). Default 2.01 USDC fee would
// reject a 1 USDC source burn (common multi-domain withdraw split).
func capMaxFee(maxFee *big.Int, valueAtomic decimal.Decimal) *big.Int {
	if maxFee == nil || maxFee.Sign() <= 0 {
		maxFee = big.NewInt(defaultMaxFeeAtomic)
	}
	val, ok := new(big.Int).SetString(valueAtomic.Truncate(0).String(), 10)
	if !ok || val.Sign() <= 0 {
		return maxFee
	}
	// Leave at least 1 atomic unit of burn value after max fee.
	if maxFee.Cmp(val) < 0 {
		return new(big.Int).Set(maxFee)
	}
	if val.Cmp(big.NewInt(1)) <= 0 {
		return big.NewInt(0)
	}
	return new(big.Int).Sub(val, big.NewInt(1))
}

// resolveBurnSources fills empty SourceChainCAIP2 by allocating from live Gateway balances.
// Explicit sources (from_chain set) pass through unchanged.
func (e *DepositExecutor) resolveBurnSources(ctx context.Context, params []burnMintParams) ([]burnMintParams, error) {
	if len(params) == 0 {
		return params, nil
	}
	var out []burnMintParams
	for _, p := range params {
		if strings.TrimSpace(p.SourceChainCAIP2) != "" {
			out = append(out, p)
			continue
		}
		expanded, err := e.allocateBurnsFromGatewayBalances(ctx, p)
		if err != nil {
			return nil, err
		}
		out = append(out, expanded...)
	}
	return out, nil
}

type gatewayBalanceRow struct {
	Domain  int    `json:"domain"`
	Balance string `json:"balance"`
}

type gatewayBalancesAPIResp struct {
	Balances []gatewayBalanceRow `json:"balances"`
}

type burnSourceCand struct {
	caip string
	amt  decimal.Decimal
}

// allocateBurnsFromGatewayBalances splits p.ValueAtomic across domains with positive balance.
// Domains with balance too small to cover Gateway maxFee are skipped (Circle base fee is
// often ~2 USDC — a 1 USDC domain balance cannot be burned alone).
func (e *DepositExecutor) allocateBurnsFromGatewayBalances(ctx context.Context, p burnMintParams) ([]burnMintParams, error) {
	need := p.ValueAtomic.Truncate(0)
	if !need.IsPositive() {
		return nil, liqerr.New(liqerr.CodeInvalidQuery,
			"deposit execute: withdraw amount must be positive")
	}
	rows, err := e.fetchGatewayBalances(ctx)
	if err != nil {
		return nil, err
	}
	feeFloor := e.configuredMaxFeeAtomic()
	ordered := collectFeeViableBurnSources(rows, p.DestChainCAIP2, feeFloor)
	out, left := pickBurnsFromSources(ordered, need, p, feeFloor)
	if left.IsPositive() {
		return nil, liqerr.New(liqerr.CodeInsufficientLiquidity,
			"deposit execute: gateway balance insufficient for withdraw (need %s atomic; domains below fee floor ~%s atomic skipped)",
			need.String(), feeFloor.String())
	}
	if len(out) == 0 {
		return nil, liqerr.New(liqerr.CodeInsufficientLiquidity,
			"deposit execute: no gateway domain has balance above fee floor (~%s atomic USDC)",
			feeFloor.String())
	}
	return out, nil
}

func collectFeeViableBurnSources(rows []gatewayBalanceRow, destCAIP2 string, feeFloor *big.Int) []burnSourceCand {
	destInfo, destOK := liquidity.LookupChain(destCAIP2)
	floorDec := decimal.NewFromBigInt(feeFloor, 0)
	var preferred, sameDomain []burnSourceCand
	for _, row := range rows {
		amt, err := humanUSDCToAtomic(row.Balance)
		if err != nil || !amt.IsPositive() || !amt.GreaterThan(floorDec) {
			continue
		}
		caip, ok := testnetCAIP2ForGatewayDomain(row.Domain)
		if !ok || !liquidity.IsTestnetExecutableChain(caip) {
			continue
		}
		c := burnSourceCand{caip: caip, amt: amt}
		if destOK && row.Domain == destInfo.GatewayDomain {
			sameDomain = append(sameDomain, c)
		} else {
			preferred = append(preferred, c)
		}
	}
	sortBurnCandsByAmtDesc(preferred)
	sortBurnCandsByAmtDesc(sameDomain)
	return append(preferred, sameDomain...)
}

func sortBurnCandsByAmtDesc(cs []burnSourceCand) {
	for i := 0; i < len(cs); i++ {
		for j := i + 1; j < len(cs); j++ {
			if cs[j].amt.GreaterThan(cs[i].amt) {
				cs[i], cs[j] = cs[j], cs[i]
			}
		}
	}
}

func pickBurnsFromSources(ordered []burnSourceCand, need decimal.Decimal, p burnMintParams, feeFloor *big.Int) ([]burnMintParams, decimal.Decimal) {
	floorDec := decimal.NewFromBigInt(feeFloor, 0)
	var out []burnMintParams
	left := need
	for _, c := range ordered {
		if !left.IsPositive() {
			break
		}
		take := c.amt
		if take.GreaterThan(left) {
			take = left
		}
		if !take.GreaterThan(floorDec) {
			continue
		}
		out = append(out, burnMintParams{
			SourceChainCAIP2: c.caip,
			DestChainCAIP2:   p.DestChainCAIP2,
			ValueAtomic:      take,
			Recipient:        p.Recipient,
		})
		left = left.Sub(take)
	}
	return out, left
}

func (e *DepositExecutor) configuredMaxFeeAtomic() *big.Int {
	maxFee := e.maxFeeAtomic
	if maxFee == nil || maxFee.Sign() <= 0 {
		return big.NewInt(defaultMaxFeeAtomic)
	}
	return new(big.Int).Set(maxFee)
}

// circleAPIErrorSuffix extracts a short, non-sensitive Circle JSON "message" if present.
func circleAPIErrorSuffix(raw []byte) string {
	var wrap struct {
		Message string `json:"message"`
		Success *bool  `json:"success"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil || strings.TrimSpace(wrap.Message) == "" {
		return ""
	}
	msg := strings.TrimSpace(wrap.Message)
	// Bound length; drop unlikely secret-looking long hex blobs.
	if len(msg) > 240 {
		msg = msg[:240] + "…"
	}
	if strings.Count(msg, "0x") > 3 {
		return ""
	}
	return ": " + msg
}

func (e *DepositExecutor) fetchGatewayBalances(ctx context.Context) ([]gatewayBalanceRow, error) {
	// Query all registered GatewayOK testnet domains for this agent.
	type src struct {
		Domain    int    `json:"domain"`
		Depositor string `json:"depositor"`
	}
	var sources []src
	seen := map[int]bool{}
	for _, c := range liquidity.ListChains() {
		if !c.Testnet || !c.GatewayOK || seen[c.GatewayDomain] {
			continue
		}
		seen[c.GatewayDomain] = true
		sources = append(sources, src{Domain: c.GatewayDomain, Depositor: e.addr.Hex()})
	}
	if len(sources) == 0 {
		return nil, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: no testnet gateway domains registered")
	}
	body, err := json.Marshal(map[string]any{"token": "USDC", "sources": sources})
	if err != nil {
		return nil, liqerr.New(liqerr.CodeInvalidQuery, "deposit execute: balances encode failed")
	}
	base := strings.TrimRight(e.gatewayAPI, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/balances", bytes.NewReader(body))
	if err != nil {
		return nil, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: balances request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.httpDo(req)
	if err != nil {
		return nil, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: balances request failed")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: balances HTTP %d", resp.StatusCode)
	}
	var parsed gatewayBalancesAPIResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, liqerr.New(liqerr.CodeLiquidityRailUnavailable,
			"deposit execute: balances response invalid")
	}
	return parsed.Balances, nil
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
	return d.Mul(decimal.NewFromInt(1_000_000)).Truncate(0), nil
}

func testnetCAIP2ForGatewayDomain(domain int) (string, bool) {
	for _, c := range liquidity.ListChains() {
		if c.GatewayDomain == domain && c.Testnet && c.GatewayOK {
			return c.CAIP2, true
		}
	}
	return "", false
}