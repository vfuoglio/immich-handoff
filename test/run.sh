#!/usr/bin/env bash
# Starts a throwaway Immich, runs the end-to-end tests against it, then tears it down.
# Usage: test/run.sh [group]    no argument: every test; a group (browser, core, hub; see test/groups.sh): only that
#                               group, which is what CI runs, three at a time, each with its own Immich
#        keep=1 test/run.sh     leaves Immich running afterwards, for inspection
#        test/run-parallel.sh   runs the groups at the same time on this machine
# E2E_PORT (Immich, default 2284), E2E_APP_PORT (the app, default 18080) and COMPOSE_PROJECT_NAME move one run
# aside, so several can run together; the tests then follow IMMICH_E2E_URL, which defaults to the Immich port.
set -euo pipefail
cd "$(dirname "$0")/.."
. test/groups.sh
run=()
if [ -n "${1:-}" ]; then
  var="E2E_GROUP_$1"
  [ -n "${!var:-}" ] || { echo "unknown group '$1' (groups: $E2E_GROUPS)" >&2; exit 2; }
  run=(-run "${!var}")
fi
export E2E_PORT="${E2E_PORT:-2284}"
export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-immich-handoff-e2e}"
trap '[ "${keep:-0}" = 1 ] || docker compose -f test/docker-compose.yml down -v' EXIT
docker compose -f test/docker-compose.yml up -d --wait --wait-timeout 300
IMMICH_E2E_URL="${IMMICH_E2E_URL:-http://localhost:$E2E_PORT}" go test -tags e2e -count=1 -v -timeout 10m "${run[@]}" ./test/e2e
