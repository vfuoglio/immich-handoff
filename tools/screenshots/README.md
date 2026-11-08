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

## The animations

The moving pictures in `docs/images/*.gif` (the README's live scene at the top and the "See it move" table) are filmed from the real page, in Chrome, while the demo does what a person would: tags photos, presses Ctrl K, switches the theme. They need `ffmpeg` too. Six scenes are defined in `film.go`; `go run . film` with no valid name lists them:

| Scene | Who, look | What happens |
|---|---|---|
| `live-scene` | Bob, light | His Holiday 2026 scene while Alice's and Carol's photos are handed over: squares travel the lanes, the counter ticks up |
| `arrival-dark` | Alice, dark | Her Overview while her photos go: Waiting jumps up, then Handed over rolls up, the chart climbs |
| `phone-dark` | Alice, dark, phone | The same on a 390 px phone, scrolled to the numbers and the live scene |
| `palette-dark` | Bob, dark | Ctrl K: type, move down, choose; the service opens in place |
| `theme-switch` | Bob | The theme button: light, dark, light |
| `services-light` | Bob, light | The Services tab, a row that opens in place, the cards tilting toward a moving pointer, the Activity tab |
| `invite-qr` | Bob, light | An invitation row: scroll to the link, the QR code opens, Copy a message says Copied |

The scenes that hand photos over (`live-scene`, `arrival-dark`, `phone-dark`) use up the photos `seed.sh prepare` left untagged, so run `prepare` before each of them, with `FIRST` moved on (Immich ignores a photo it already has). Do the other scenes afterwards, so the pages have numbers to show.

```
cd tools/screenshots && export IMMICH_URL=http://localhost:2294 APP_URL=http://localhost:8090/
./seed.sh reset && ./seed.sh setup           # the app runs for real this time, no DRY
FIRST=12 ./seed.sh prepare                   # Alice joins Holiday 2026 as well; 6 untagged photos each for Alice and Carol
go run . film arrival-dark /tmp/film/arrival-dark     # tags the photos itself a few seconds in (seed.sh fire)
FIRST=24 ./seed.sh prepare && go run . film live-scene /tmp/film/live-scene
FIRST=36 ./seed.sh prepare && go run . film phone-dark /tmp/film/phone-dark
for s in palette-dark theme-switch services-light invite-qr; do go run . film $s /tmp/film/$s; done
./gifs.sh /tmp/film ../../docs/images
```

`film` writes JPEG frames and `frames.txt` (each frame with how long it stays), and for a scene that shows only an element, `crop.txt`. `gif.sh` cuts them into a GIF (start and end second, speed, width; `COLORS`, `FPS`, `DITHER` and `CROP` shrink or crop it) and `gifs.sh` holds the cuts that were chosen by looking at each recording; a new recording needs its own times, so look at a few frames first (`ffmpeg -f concat -safe 0 -i frames.txt -vf "fps=0.5,scale=300:-1,tile=5x3" -frames:v 1 sheet.png`). Light pages are small as GIFs; the dark ones, with their drifting light, are the heavy ones, so they use no dithering and a crop where they can. Keep each under about 1.5 MB.
