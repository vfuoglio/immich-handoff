#!/usr/bin/env bash
# Prints the dated nightly tags that should be deleted, one per line.
#
#   prune_nightly.sh 14 < tags
#
# The newest KEEP dated nightly tags (`20261107-nightly`, newest by date) stay; every older one is printed. Anything else
# on standard input is ignored, so `nightly`, release versions such as `1.2.0`, `latest` and any other tag can never
# be printed, whatever the registry lists.
set -euo pipefail
keep="${1:?usage: prune_nightly.sh KEEP < tags}"
[[ "$keep" =~ ^[0-9]+$ ]] || { echo "KEEP must be a number" >&2; exit 2; }
grep -E '^[0-9]{8}-nightly$' | sort -ru | tail -n +"$((keep + 1))" || true
