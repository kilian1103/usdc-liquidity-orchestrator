#!/usr/bin/env bash
# Dry plan/consolidate against a local orchestrator (go run ./cmd/server).
# Default: Arc Testnet + circle_gateway (hackathon example).
# Usage: bash examples/curl.sh [plan.json|consolidate-testnet.json]
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ADDR="${LISTEN_ADDR:-http://127.0.0.1:8088}"
EXAMPLE="${1:-$ROOT/examples/plan.json}"
PATH_SUFFIX="/v1/plan"
case "$(basename "$EXAMPLE")" in
  consolidate*) PATH_SUFFIX="/v1/consolidate" ;;
esac
curl -sS -X POST "$ADDR$PATH_SUFFIX" \
  -H 'Content-Type: application/json' \
  --data-binary @"$EXAMPLE" | jq .
