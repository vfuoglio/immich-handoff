#!/usr/bin/env bash
# Every end-to-end test must be in exactly one group of test/groups.sh, or CI would never run it (or run it twice).
set -euo pipefail
cd "$(dirname "$0")/../.."
. test/groups.sh
fail=0
for t in $(go test -tags e2e -list '.*' ./test/e2e | grep '^Test'); do
  n=0
  for g in $E2E_GROUPS; do
    var="E2E_GROUP_$g"
    if [[ "$t" =~ ${!var} ]]; then n=$((n + 1)); fi
  done
  if [ "$n" -ne 1 ]; then echo "test $t is in $n groups; it must be in exactly one (test/groups.sh)"; fail=1; fi
done
[ "$fail" = 0 ] && echo "every end-to-end test is in exactly one group"
exit $fail
