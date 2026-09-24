# AGENTS.md

Compact context for coding agents in this repository.

## What this is

Public Go library + CLI + thin HTTP service (equal peers) + product skill: **non-custodial multi-chain USDC liquidity planning** for agentic commerce (Circle Gateway preferred, CCTP fallback). Module: `github.com/kilian1103/usdc-liquidity-orchestrator`. Surfaces: `pkg/liquidity` · `cmd/usdc-liq` · `cmd/server` · `skills/usdc-liquidity/`.

## Read first

1. Root [`CLAUDE.md`](./CLAUDE.md) — invariants, module index, commands  
2. Package `CLAUDE.md` for any path you edit  
3. [`.github/pr-review-instructions.md`](./.github/pr-review-instructions.md) before opening PRs  

## Skills (orchestrate agents)

| Skill | Purpose |
|---|---|
| `/team_review` | Design gate before code |
| `/ship_feature` | team_review → implement → ci_review_loop → PR |
| `/ci_local` | One CI + dual OpenCode pass |
| `/ci_review_loop` | 2× ci-local + fix → create_pr |
| `/create_pr` | Review, test, docs, commit, PR |
| `/session_cleanup` | Remove temp diffs / coverage |

## Agents

| Agent | Model | Role |
|---|---|---|
| `backend-go` | composer-2.5-fast | Design / implement / fix-ci |
| `ci-local` | composer-2.5-fast | CI gates + OpenCode |
| `qa-engineer` | grok-build readonly | Edge cases |
| `data-privacy` | grok-build readonly | PII / inventory privacy |
| `security-researcher` | grok-build readonly | Custody / MoR / confuse-deputy |

## Hard rules (summary)

- agent_self recipients only; never pay_to as bridge dest  
- Phase B shortfall (`PlanOrchestration` / move); Phase A deposits via deposit/consolidate/`PlanPaymentFunding`  
- dry plans never claim funded  
- empty pay_to OK for agent_self withdraw/cctp; never auto-insert deposit+withdraw  
- `decimal.Decimal` atomic units, no float64  
- tests under `tests/` as `package *_test`  
- live execute (optional): testnet deposit + burn/mint; re-derived prepare_calls; agent_self only; loopback + dual gate  

