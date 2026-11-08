#!/usr/bin/env bash
# Runs the end-to-end test groups (test/groups.sh) at the same time, each against its own throwaway Immich, so the whole
# suite takes as long as its longest group (about two minutes) instead of the sum (about five). It needs three Immich
# stacks at once (a few GB of memory). Each group's output is kept in a file, and the failures are shown at the end.
# Usage: test/run-parallel.sh
set -uo pipefail
cd "$(dirname "$0")/.."
. test/groups.sh
out=$(mktemp -d)
i=0
pids=()
for g in $E2E_GROUPS; do
  (
    export E2E_PORT=$((2284 + i * 10)) E2E_APP_PORT=$((18080 + i * 100)) COMPOSE_PROJECT_NAME="immich-handoff-e2e-$g"
    start=$(date +%s)
    if test/run.sh "$g" > "$out/$g.log" 2>&1; then r=PASS; else r=FAIL; fi
    echo "$g: $r in $(( $(date +%s) - start )) s" > "$out/$g.result"
  ) &
  pids+=($!)
  i=$((i + 1))
done
status=0
for p in "${pids[@]}"; do wait "$p" || status=1; done
for g in $E2E_GROUPS; do
  cat "$out/$g.result"
  if grep -q '^FAIL' "$out/$g.result" 2>/dev/null || grep -q ': FAIL' "$out/$g.result"; then
    status=1
    grep -E '^(--- FAIL|    --- FAIL|\s+[a-z_]+\.go:[0-9]+:)' "$out/$g.log" | head -20
    echo "(full output: $out/$g.log)"
  fi
done
exit $status
