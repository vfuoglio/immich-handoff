#!/usr/bin/env bash
# The unit tests, in the groups CI runs side by side. The race detector makes tests 15 to 30 times slower, so it runs only on the
# packages that start goroutines or share state between them (RACE), and the biggest two (SHARDED) are cut into shards that run at the
# same time. Every package must be in exactly one list: unit_test.sh checks it, so a new package cannot be left out.
#   unit.sh plain            the packages without goroutines, no race detector
#   unit.sh race             the other packages, with the race detector
#   unit.sh shard PKG K N    the K-th of N shards of PKG (one of SHARDED), with the race detector
set -euo pipefail
cd "$(dirname "$0")/../.."
PLAIN="test/gen-stack-samples internal/albumtree internal/safeimg internal/config internal/doctor internal/i18n internal/mailin internal/mark internal/pets internal/push internal/store internal/vault"
RACE="cmd/immich-handoff internal/backup internal/bridge internal/dest internal/immich internal/live internal/mailin/mailtest internal/notify internal/peer internal/syncer"
SHARDED="internal/hub internal/web"
if [ "${1:-}" = lists ]; then echo "$PLAIN $RACE $SHARDED"; exit 0; fi
dirs() { for p in "$@"; do echo "./$p"; done; }
case "${1:-}" in
  plain) go test $(dirs $PLAIN) ;;
  race) go test -race $(dirs $RACE) ;;
  shard)
    pkg=$2; k=$3; n=$4
    # tests are dealt out one by one, so a shard gets a mix of the quick and the slow
    tests=$(go test -list '.*' "./$pkg" | grep '^Test' | sort | awk -v k="$k" -v n="$n" '(NR - 1) % n == k - 1' | paste -sd'|')
    [ -n "$tests" ] || { echo "shard $k of $n of $pkg has no tests"; exit 1; }
    go test -race -run "^($tests)\$" "./$pkg" ;;
  *) echo "usage: unit.sh plain | race | shard PKG K N"; exit 2 ;;
esac
