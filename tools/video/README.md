# The product film

The film on the README is made from the real product, not mocked up: scripted scenes on the demo, filmed in Chrome, with graphics drawn by web pages and cut together with ffmpeg. Nothing here is needed to run immich-handoff.

You need Docker, Go, Google Chrome (or Chromium), ffmpeg and Python 3 with Pillow. The music is not in the repository: download "Mister Blues" (CC0) from OpenGameArt and point `MUSIC` at it.

```
tools/video/capture.sh                    # resets the demo (demo/) and films every scene into /tmp/handoff-film
MUSIC=/path/to/mister-blues.mp3 FILM_OUT=/tmp/handoff-film tools/video/build.py final
```

| File | What it does |
|---|---|
| [STORYBOARD.md](STORYBOARD.md) | The scenes, their captions and timing (39 bars of the music at 125 beats a minute) |
| `capture.sh`, `reset-demo.sh`, `clip.sh` | Reset the demo, film each scene in the order its state needs, turn a take into a 30 fps video |
| `../screenshots/video*.go` | The recorder (a visible cursor with click ripples, smooth scrolling and typing), the scenes, and the frame-by-frame renderer for the graphics (`go run . video render ...`) |
| `pages.py` | The graphics and the captions, as web pages whose animations are stepped by a clock the renderer sets |
| `build.py` | Cuts the takes to length with a slow push in, puts them in a window or a phone on the background, adds the captions, joins the scenes on the bar lines, mixes the music and encodes (`rough` or `final`, under 10 MB so GitHub plays it on the page) |

The pictures in the film are the public-domain photos in [demo/photos](../../demo/photos/CREDITS.md). When a feature changes, change the scene in `video_scenes.go`, run `capture.sh` again and `build.py`.
