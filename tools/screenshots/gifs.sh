#!/usr/bin/env bash
# Makes every README animation from the frames that `go run . film <scene> <dir>` wrote (one directory per scene under
# <frames>), with the cuts chosen by looking at each recording. A different recording needs different times: look at a
# few frames first (see README.md, "The animations").
#   tools/screenshots/gifs.sh <frames> <outdir>      for example: gifs.sh /tmp/film ../../docs/images
set -euo pipefail
cd "$(dirname "$0")"
f=$1 out=$2
g() { ./gif.sh "$f/$1" "$2" "$3" "$4" "$5" "$out/$1.gif" "${6:-1.5}"; }
CROP=908:610:186:0 DITHER=none FPS=8 COLORS=64 g arrival-dark 12.5 28 1.4 700 2   # hero, two stages: the photos fly out of You, the counters climb
                      DITHER=none FPS=10 COLORS=96 g live-scene     7 30 1.5 760 1.6 # Bob's stage: two senders' photos fly in, the ring spreads
                      DITHER=none FPS=10 COLORS=96 g phone-dark    ${PHONE_CUT:-10 27} 1.5 360 2
                      DITHER=none FPS=10 COLORS=96 g palette-dark  0.4 7.4 1   720
                                  FPS=10 COLORS=96 g theme-switch  0.4 13.6 1  720
                      DITHER=none FPS=8 COLORS=64 g services-light 0.4 9.4 1  680
                      DITHER=none FPS=10 COLORS=80 g invite-qr     0.4 8.3 1   640
                      DITHER=none FPS=8  COLORS=80 g wall          0.3 35 1.3  640 1.5 # a photo arrives, then the styles and the layouts
