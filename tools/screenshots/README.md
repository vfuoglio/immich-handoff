# Documentation images

The pictures in `docs/images/` are made from a real, running app by this small tool, so they can be
remade whenever the page changes. Nothing here is needed to run immich-handoff.

Needs Docker, Google Chrome (or Chromium) and Go. It uses its own throwaway Immich on port 2294 and the app on 8090, so a demo on the usual ports is left alone.

```
cd tools/screenshots && export IMMICH_URL=http://localhost:2294 APP_URL=http://localhost:8090/
./seed.sh reset                  # fresh Immich with users alice, bob and family (password Passw0rd!x)
DRY=1 ./seed.sh setup            # run the app, create people and services, make sample photos
./seed.sh waiting                # upload photos and tag some; the dry-run app leaves them waiting
go run . shots sender ../../docs/images && go run . hero ../../docs/images
./seed.sh handover               # restart the app for real: the photos are handed over
go run . shots owner ../../docs/images && go run . shots immich ../../docs/images
./seed.sh lost                   # Carol deletes her key
go run . shots lost ../../docs/images
```

The sample photos are generated landscapes (`go run . gen <dir>`), so no real pictures or people appear.

Every picture is taken in English: the tool sends `Accept-Language: en` and sets the page's language cookie to English, whatever language the machine uses. The pictures belong to the English documentation.

## The animation

`docs/images/live-scene.gif` (the README's moving picture) films Bob's live scene while photos are handed over for real. It needs `ffmpeg` too. A fresh Immich is best, because the photos must not have been handed over before (`FIRST` moves to unused sample photos for a second take).

```
cd tools/screenshots && export IMMICH_URL=http://localhost:2294 APP_URL=http://localhost:8090/
./seed.sh reset && ./seed.sh setup           # the app runs for real this time, no DRY
./seed.sh prepare                            # Alice joins Holiday 2026 as well; 6 untagged photos each for Alice and Carol
(go run . record bob 36 /tmp/frames &)       # films the scene as JPEG frames
sleep 7 && ./seed.sh fire                    # tag them: the hand-overs run and the scene moves
```

Then cut it to the hand-overs (here from 5.8 s to 25.5 s, 1.6 times faster, with the last frame held for 2 s) and make the GIF, 760 px wide and 96 colours to keep it under 1 MB:

```
cd /tmp/frames && ffmpeg -f concat -safe 0 -i frames.txt -vf "trim=start=5.8:end=25.5,setpts=(PTS-STARTPTS)/1.6,crop=984:256:0:0,fps=10,scale=760:-1:flags=lanczos,tpad=stop_mode=clone:stop_duration=2,split[a][b];[a]palettegen=max_colors=96:stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=5:diff_mode=rectangle" -loop 0 live-scene.gif
```

Look at a few frames before keeping it (`ffmpeg -i live-scene.gif -vf "select='eq(n,0)+eq(n,60)'" -vsync 0 f-%d.png`), and adjust the cut so it ends when the counter has reached the number of photos. The crop removes the log lines under the card, which grow while it runs.
