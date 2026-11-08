#!/usr/bin/env bash
# Makes every README animation from the frames that `go run . film <scene> <dir>` wrote (one directory per scene under
# <frames>), with the cuts chosen by looking at each recording. A different recording needs different times: look at a
# few frames first (see README.md, "The animations").
#   tools/screenshots/gifs.sh <frames> <outdir>      for example: gifs.sh /tmp/film ../../docs/images
set -euo pipefail
cd "$(dirname "$0")"
f=$1 out=$2
g() { ./gif.sh "$f/$1" "$2" "$3" "$4" "$5" "$out/$1.gif" "${6:-1.5}"; }
CROP=984:256:148:522 DITHER=none FPS=10 COLORS=96 g live-scene     8.5 30  1.6 760 2   # two lanes, the counter up to 12
CROP=1000:430:140:262 DITHER=none FPS=10 COLORS=96 g arrival-dark  2.5 26  1.5 760 2   # the numbers roll, the chart climbs
                      DITHER=none FPS=10 COLORS=96 g phone-dark    ${PHONE_CUT:-3.5 27} 1.5 360 2
                      DITHER=none FPS=10 COLORS=96 g palette-dark  0.4 7.4 1   720
                                  FPS=10 COLORS=96 g theme-switch  0.8 7.4 1   720
                      DITHER=none FPS=10 COLORS=80 g services-light 0.4 9.4 1  720
                      DITHER=none FPS=10 COLORS=80 g invite-qr     0.4 8.3 1   640
