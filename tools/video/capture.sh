#!/usr/bin/env bash
# Films every scene of the product film from a freshly reset demo, in the order that makes each scene's state, and turns each take into
# a 30 fps video under $OUT (default /tmp/handoff-film). The scenes depend on one another (the guest sends to the box the host made).
#   tools/video/capture.sh
set -euo pipefail
cd "$(dirname "$0")/../.."
OUT=${OUT:-/tmp/handoff-film}
mkdir -p "$OUT"
tools/video/reset-demo.sh
cd tools/screenshots
run() { # scene, folder, width, height
  go run . video "$1" "$OUT/$2"
  ../video/clip.sh "$OUT/$2" "$OUT/$2.mp4" "${3:-1920}" "${4:-1080}"
}
go run . video ruleprep x
run host c03
run guest c04 780 1688
run arrive c05
run wall c06
run inside c07
run rule c08
run client c09 780 1688
echo "footage is in $OUT"
