---
name: create_pr
description: Review against pr-review-instructions, tests, CLAUDE.md sync, commit, open PR for usdc-liquidity-orchestrator.
---

# create_pr

## Phase 1 — Review

Read `.github/pr-review-instructions.md` + `CLAUDE.md`. Run `gofmt -l .`, `go vet ./...`, `golangci-lint run`. Fix violations.

## Phase 2 — Tests

```bash
go test -v -race -coverprofile=coverage.out ./...
bash scripts/check-test-layout.sh
```

## Phase 3 — Docs

Sync root + module `CLAUDE.md`, `README.md`, `OPS.md` if surface changed.

## Phase 4 — Commit

Stage specific files only. Conventional commit. Co-Authored-By optional.

## Phase 5 — PR

```bash
git push -u origin HEAD
gh pr create --title "..." --body "..."
```

Target `main` on `kilian1103/usdc-liquidity-orchestrator`.
