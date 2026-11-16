#!/usr/bin/env bash
# Turns the frames that `go run . video <scene> <dir>` wrote (frames.txt: each frame with how long it stays) into an
# even 30 frames a second video, 1920 by 1080 unless a size is given. The result is a work file, not the film.
#   tools/video/clip.sh <framedir> <out.mp4> [width height]
set -euo pipefail
dir=$1 out=$(realpath -m "$2") w=${3:-1920} h=${4:-1080}
cd "$dir"
ffmpeg -v error -y -f concat -safe 0 -i frames.txt -vf "fps=30,scale=${w}:${h}:flags=lanczos,format=yuv420p" -c:v libx264 -preset slow -crf 14 -movflags +faststart "$out"
printf '%s: %s, %s KB\n' "$(basename "$out")" "$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$out" | cut -c1-5)s" "$(( $(stat -c %s "$out") / 1024 ))"
