#!/usr/bin/env bash
# Cuts the frames that `go run . film <scene> <dir>` wrote into a GIF.
#   tools/screenshots/gif.sh <framedir> <start s> <end s> <speed> <width px> <out.gif> [hold s]
# The start and end are seconds into the filming (look at a few frames first), the speed is 1 for real time or more for
# faster, the width is that of the finished GIF, and hold keeps the last frame for that many seconds (default 2).
# CROP=1000:380:140:310 keeps only that part of the frame (width:height:x:y).
# Smaller files: COLORS=64 (default 128), FPS=10 (default 12), DITHER=none (default bayer:bayer_scale=5; none bands a little but is far smaller).
set -euo pipefail
dir=$1 start=$2 end=$3 speed=$4 width=$5 out=$(realpath -m "$6") hold=${7:-2}
crop=""
[ -f "$dir/crop.txt" ] && crop="crop=$(cat "$dir/crop.txt"),"
[ -n "${CROP:-}" ] && crop="crop=$CROP," # width:height:x:y, to show only part of the page
cd "$dir"
ffmpeg -v error -y -f concat -safe 0 -i frames.txt -vf "trim=start=$start:end=$end,setpts=(PTS-STARTPTS)/$speed,${crop}fps=${FPS:-12},scale=$width:-2:flags=lanczos,tpad=stop_mode=clone:stop_duration=$hold,split[a][b];[a]palettegen=max_colors=${COLORS:-128}:stats_mode=diff[p];[b][p]paletteuse=dither=${DITHER:-bayer:bayer_scale=5}:diff_mode=rectangle" -loop 0 "$out"
printf '%s: %s KB\n' "$(basename "$out")" "$(( $(stat -c %s "$out") / 1024 ))"
