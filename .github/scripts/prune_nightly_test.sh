#!/usr/bin/env bash
# Checks prune_nightly.sh against the cases that matter. Run by CI.
set -euo pipefail
cd "$(dirname "$0")"
fail=0
check() { # check "EXPECTED (space separated, may be empty)" KEEP TAG...
  local want="$1" keep="$2"; shift 2
  local got
  got="$(printf '%s\n' "$@" | ./prune_nightly.sh "$keep" | tr '\n' ' ' | sed 's/ $//')"
  if [ "$got" != "$want" ]; then echo "FAIL: prune_nightly.sh $keep with [$*] gave '$got', wanted '$want'"; fail=1; fi
}
check ""                                  14 20261105-nightly 20261106-nightly                       # fewer than the limit: nothing
check ""                                  2  20261105-nightly 20261106-nightly                       # exactly the limit: nothing
check "20261105-nightly"                  2  20261105-nightly 20261106-nightly 20261107-nightly     # the oldest goes
check "20261105-nightly 20261104-nightly" 1  20261107-nightly 20261104-nightly 20261105-nightly     # order of the list does not matter; oldest last
check "20261031-nightly"                  2  20261101-nightly 20261031-nightly 20261102-nightly     # across a month: by date, not by text length
check ""                                  1  nightly latest 1.2.0 1.2 20261107-nightly              # other tags are never printed
check ""                                  0  nightly latest 1.2.0 sha256-abc.sig                     # keep none: still only dated nightlies
check "20261107-nightly"                  0  nightly 20261107-nightly 1.2.0                          # keep none removes dated ones only
check ""                                  3                                                          # no tags at all
check ""                                  1  2026110-nightly 20261107-nightly-test 20261107-nightlyx # near misses are ignored
[ "$fail" = 0 ] && echo "prune_nightly.sh: all cases pass"
exit "$fail"
