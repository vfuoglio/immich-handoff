#!/usr/bin/env bash
# Prints "true" if VERSION should get the `latest` image tag, otherwise "false".
#
#   latest.sh 1.2.3 < tags
#
# `latest` goes only to a final version (no -rc1 or similar suffix) that is at least as high as every final
# version already released, so a fix for an older line (1.1.1 after 1.2.0 is out) never takes `latest` back.
# The released tags come on standard input, one per line, as `v1.2.0` or `1.2.0`; anything that is not a
# final version number (release candidates, other tags) is ignored. The version itself may be in the list.
set -euo pipefail
v="${1:?usage: latest.sh VERSION < tags}"
v="${v#v}"
if ! [[ "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo false
  exit 0
fi
highest="$v"
while IFS= read -r t; do
  t="${t#v}"
  [[ "$t" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || continue
  if [ "$(printf '%s\n%s\n' "$t" "$highest" | sort -V | tail -n1)" = "$t" ]; then
    highest="$t"
  fi
done
if [ "$highest" = "$v" ]; then echo true; else echo false; fi
