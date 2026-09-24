# SETUP

## Prerequisites

- Go (version from `go.mod`)
- Optional: `golangci-lint`, `gitleaks`, `govulncheck`, `actionlint` for full local CI parity

## Clone

```bash
git clone https://github.com/kilian1103/usdc-liquidity-orchestrator.git
cd usdc-liquidity-orchestrator
go mod download
```

## Run tests

```bash
go test -v -race ./...
bash scripts/check-test-layout.sh
```

## CLI (`usdc-liq`)

Dual-surface peer of the HTTP server. Shared stamps via `internal/planio`.

```bash
go run ./cmd/usdc-liq plan -f examples/plan.json
go run ./cmd/usdc-liq consolidate -f examples/consolidate-testnet.json
go run ./cmd/usdc-liq payment-funding -f -   # stdin JSON
go run ./cmd/usdc-liq chains
go run ./cmd/usdc-liq version
# Easy mode (plan/consolidate/deposit/move): domain|name|caip2 + human USDC; exclusive with -f
go run ./cmd/usdc-liq plan \
  --agent 0x… --dest arc-testnet --amount 42 \
  --balance base-sepolia=100 --gateway-balance 80
go run ./cmd/usdc-liq deposit \
  --agent 0x… --from base-sepolia=3 --from arbitrum-sepolia=2 \
  --balance base-sepolia=3 --balance arbitrum-sepolia=2
# CLI-only:
go run ./cmd/usdc-liq inventory -agent 0x…   # needs RPC env; no secrets in notes
go run ./cmd/usdc-liq demo
```

Dry is default (no live config). Easy mode builds wire requests in-process (no stdin hang when incomplete → exit 2). `--live` loads testnet inventory via RPCs (not with `--balance`). `--execute` needs dual-gate env (`ENABLE_TESTNET_EXECUTE=1` + key + RPCs); incomplete config exits 1 sanitized without Execute. Prefer `AGENT_PRIVATE_KEY` env over `--private-key` (argv may be visible). Body limit 1 MiB. Product skill: [`skills/usdc-liquidity/`](./skills/usdc-liquidity/).

## Demo (worked example)

```bash
cp .env.example .env   # optional; fill AGENT_ADDRESS + payment scenario
go run ./cmd/usdc-liq demo
go run ./cmd/demo      # thin wrapper → same demorun
```

Primary path (when `PAYMENT_CHAIN` / scenario env is set): **full-funding** dry plan via `PlanPaymentFunding` — hard-coded `SOURCE_AMOUNT_*` deposits (scaled by `USDC_SCALE_FACTOR`) + withdraw full payment real to `agent_self`. Wire stamps `amount_atomic` (real) and optional `amount_logical_atomic` / `scale_factor`.

When `AGENT_ADDRESS` (or key) and any testnet RPCs are set, demo tries **live inventory** (`balanceOf` + optional Gateway balances). Funding amounts remain hard-coded; live natives only gate “source real ≤ balance”. On load failure, falls back to asserted inventory from `SOURCE_AMOUNT_*`. Plans still stamp `inventory_unverified=true`.

Also prints shortfall smoke (Arc Testnet `circle_gateway_withdraw` need 42 / native 20 → shortfall 22) and consolidate with unsigned `prepare_calls`.

Legacy Base/Arb fragmented example: `examples/plan-base-fragmented.json`. Scenario path is **not** HTTP `/v1/plan`.

## HTTP server

```bash
go run ./cmd/server
# browser UI (plan / consolidate / chains):
open http://127.0.0.1:8088/
# another terminal:
bash examples/curl.sh              # Arc + gateway (default)
curl -s localhost:8088/v1/chains | jq .
```

The UI is a single embedded page (`internal/httpserver/static/index.html`): set your agent address and asserted balances, run dry plans. No private keys — inventory is client-asserted only.

## Optional testnet Gateway execute (local only)

Dual-gated; **not** for Docker default. See [`OPS.md`](./OPS.md).

```bash
export ENABLE_TESTNET_EXECUTE=1
export LISTEN_ADDR=127.0.0.1:8088   # loopback required (bare :8088 refused)
export AGENT_PRIVATE_KEY=0x…        # throwaway testnet key; never commit
export RPC_URL_BASE_SEPOLIA=https://sepolia.base.org
# export RPC_URL_ARBITRUM_SEPOLIA=…
# export RPC_URL_ARC_TESTNET=…
# export RPC_URL_SOLANA_DEVNET=…    # ops placeholder; not used by EVM execute
# export GATEWAY_API_BASE=https://gateway-api-testnet.circle.com
# or CAIP form: RPC_URL_eip155_84532=… / RPC_URLS_JSON='{"eip155:84532":"https://…"}'
go run ./cmd/server
# demo live consolidate path (stderr tx hashes only):
go run ./cmd/demo
```

Copy [`.env.example`](./.env.example) for named placeholders. Requirements: inventory `agent_address` must match the key; supported live actions are consolidate, deposit_withdraw, and withdraw; deposits re-derive prepare calls; burn mints only to agent_self; mainnet RPCs refused; Solana RPC is stored for ops but not used by EVM execute yet. After deposits, wait for Gateway finality before transfer (executor retries `/v1/transfer`).

## VS Code / Cursor

Requires the [Go extension](https://marketplace.visualstudio.com/items?itemName=golang.Go) (Delve debugger).

1. Open this repo as the workspace folder
2. **Run and Debug** → choose a configuration:
   - **HTTP Server** — plan-only `cmd/server` on `LISTEN_ADDR=:8088`
   - **HTTP Server (testnet execute)** — loads `.env`, forces loopback + `ENABLE_TESTNET_EXECUTE=1`
   - **Demo (CLI)** — `cmd/demo` worked examples (exits when done; loads `.env`)

Configs live in [`.vscode/launch.json`](./.vscode/launch.json). UI amounts are **human USDC** (browser converts ×10^6 for the API).

## Docker

Multi-stage image builds a static `cmd/server` binary (distroless, non-root). **Plan-only by default** — no secrets required. The UI supports:

| Tab / control | Endpoint |
|---|---|
| Load live inventory | `POST /v1/inventory` (`{"agent_address"}`; needs testnet RPCs) |
| Scenario | `POST /v1/payment-funding` (full hard-coded sources + scale) |
| Plan | `POST /v1/plan` (shortfall-only) |
| Consolidate | `POST /v1/consolidate` |
| Chains | `GET /v1/chains` |

```bash
docker compose up --build
# UI:
open http://127.0.0.1:8088/
# or:
docker build -t usdc-liquidity-orchestrator:local .
docker run --rm -p 8088:8088 usdc-liquidity-orchestrator:local
```

Smoke test (another terminal):

```bash
curl -sS localhost:8088/healthz
bash examples/curl.sh
```

Bind address inside the container is `LISTEN_ADDR` (default `:8088`); map host port `8088` as above.

**Live execute is not enabled in Compose.** Dual-gate needs loopback `LISTEN_ADDR` + `AGENT_PRIVATE_KEY` + RPCs — use host `go run ./cmd/server` (see Optional testnet deposit execute). Never bake keys into the image.

## Agent / IDE

- Root `CLAUDE.md` + package `CLAUDE.md` files for coding agents
- Skills under `.claude/skills/` (`/ship_feature`, `/team_review`, `/ci_local`, …)
- Agents under `.claude/agents/`
