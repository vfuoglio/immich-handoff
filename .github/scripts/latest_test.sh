#!/usr/bin/env bash
# Checks latest.sh against the cases that matter. Run by CI.
set -euo pipefail
cd "$(dirname "$0")"
fail=0
check() { # check EXPECTED VERSION TAG...
  local want="$1" v="$2"; shift 2
  local got
  got="$(printf '%s\n' "$@" | ./latest.sh "$v")"
  if [ "$got" != "$want" ]; then echo "FAIL: latest.sh $v with tags [$*] gave $got, wanted $want"; fail=1; fi
}
check true  1.1.0                                  # the first release
check true  1.1.0   v1.0.0 v1.1.0                  # the tag being released is already in the list
check true  1.2.0   v1.1.0 v1.2.0
check false 1.1.1   v1.1.0 v1.2.0 v1.1.1           # a fix for an older line must not take latest
check true  1.2.1   v1.2.0 v1.2.1
check true  1.1.1   v1.1.0 v1.2.0-rc1 v1.1.1       # a release candidate does not count
check false 1.2.0-rc1 v1.1.0                       # a release candidate is never latest
check true  1.10.0  v1.9.0 v1.10.0                 # numbers, not text: 1.10 is above 1.9
check false 1.9.9   v2.0.0 v1.9.9
check true  2.0.0   v1.9.9 v2.0.0
check true  1.0.0   not-a-version v1.0.0           # unrelated tags are ignored
[ "$fail" = 0 ] && echo "latest.sh: all cases pass"
exit "$fail"
