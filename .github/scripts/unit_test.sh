#!/usr/bin/env bash
# Every Go package is in exactly one of the lists of unit.sh, or CI would not test it (or test it twice).
set -euo pipefail
cd "$(dirname "$0")/../.."
listed=$(.github/scripts/unit.sh lists | tr ' ' '\n' | sort)
dups=$(echo "$listed" | uniq -d)
all=$(go list -f '{{.Dir}}' ./... | sed "s|^$PWD/||" | sort)
fail=0
[ -z "$dups" ] || { echo "in more than one list: $dups"; fail=1; }
[ "$all" = "$(echo "$listed" | sort -u)" ] || { echo "the lists differ from the packages:"; diff <(echo "$all") <(echo "$listed" | sort -u) || true; fail=1; }
[ "$fail" = 0 ] && echo "every package is in one list"
exit $fail
