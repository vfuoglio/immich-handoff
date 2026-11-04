#!/usr/bin/env bash
# Starts a throwaway Immich, runs the end-to-end tests against it, then tears it down.
# Usage: test/run.sh            (keep=1 test/run.sh leaves Immich running for inspection)
set -euo pipefail
cd "$(dirname "$0")/.."
trap '[ "${keep:-0}" = 1 ] || docker compose -f test/docker-compose.yml down -v' EXIT
docker compose -f test/docker-compose.yml up -d --wait --wait-timeout 300
IMMICH_E2E_URL=http://localhost:2284 go test -tags e2e -count=1 -v -timeout 10m ./test/e2e
