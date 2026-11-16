#!/usr/bin/env python3
"""Builds the product film from the footage (`capture.sh`), the graphics (pages.py) and the music, following STORYBOARD.md.

    FILM_OUT=/tmp/handoff-film MUSIC=/path/to/mister-blues.mp3 tools/video/build.py [rough|final]

The footage is read from $FILM_OUT (c03.mp4 ...), everything made goes to $FILM_OUT/work, the film to $FILM_OUT/handoff-demo.mp4.
It needs ffmpeg, Chrome (for the graphics: `go run . video render` in tools/screenshots) and Pillow."""
import os, subprocess, sys, shutil
from PIL import Image, ImageDraw, ImageFilter

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import pages

HERE = os.path.dirname(os.path.abspath(__file__))
SHOTS = os.path.join(HERE, "..", "screenshots")
OUT = os.environ.get("FILM_OUT", "/tmp/handoff-film")
WORK = os.path.join(OUT, "work")
MUSIC = os.environ.get("MUSIC", "")
BPM = 125.0
BAR = 60 / BPM * 4
FPS = 30
os.makedirs(WORK, exist_ok=True)

def run(cmd, **kw):
    r = subprocess.run(cmd, capture_output=True, text=True, **kw)
    if r.returncode:
        sys.exit(f"{' '.join(map(str, cmd))[:300]}\n{r.stderr[-1500:]}")
    return r

def ff(*args):
    return run(["ffmpeg", "-v", "error", "-y", *map(str, args)])

def go(*args):
    return run(["go", "run", ".", "video", *map(str, args)], cwd=SHOTS)

def path(name):
    return os.path.join(WORK, name)

# ---- assets ------------------------------------------------------------------------------------------------------------
def rounded(w, h, r, mode="L", fill=255, pad=0):
    im = Image.new(mode, (w + 2 * pad, h + 2 * pad), 0)
    ImageDraw.Draw(im).rounded_rectangle([pad, pad, pad + w - 1, pad + h - 1], radius=r, fill=fill)
    return im

def assets():
    if os.path.exists(path("assets.done")):
        return
    html = path("bg.html")
    open(html, "w").write(pages.HEAD + "<body></body>")
    go("render", html, path("bg"), 0.04, 30)
    shutil.copy(path("bg/00000.png"), path("bg.png"))
    rounded(1536, 864, 26).save(path("mask-desktop.png"))
    rounded(406, 880, 40).save(path("mask-phone.png"))
    sh = rounded(1536, 864, 26, "L", 150, pad=70).filter(ImageFilter.GaussianBlur(34))
    rgba = Image.new("RGBA", sh.size, (0, 0, 0, 0)); rgba.putalpha(sh); rgba.save(path("shadow-desktop.png"))
    ph = rounded(406, 880, 40, "L", 160, pad=70).filter(ImageFilter.GaussianBlur(30))
    rgba = Image.new("RGBA", ph.size, (0, 0, 0, 0)); rgba.putalpha(ph); rgba.save(path("shadow-phone.png"))
    bez = Image.new("RGBA", (430, 904), (0, 0, 0, 0))
    ImageDraw.Draw(bez).rounded_rectangle([0, 0, 429, 903], radius=54, fill=(10, 8, 26, 255), outline=(70, 66, 110, 255), width=3)
    hole = Image.new("L", (430, 904), 0)
    ImageDraw.Draw(hole).rounded_rectangle([12, 12, 12 + 405, 12 + 879], radius=40, fill=255)
    bez.putalpha(Image.eval(Image.merge("L", [bez.getchannel("A")]), lambda v: v).point(lambda v: v))
    a = bez.getchannel("A"); a.paste(0, mask=hole); bez.putalpha(a)
    bez.save(path("bezel.png"))
    open(html, "w").write(pages.captions())
    go("stills", html, path("caps"))
    open(path("assets.done"), "w").write("ok")

# ---- the pieces of a scene ---------------------------------------------------------------------------------------------
def piece(name, clip, start, end, alloc, z0=1.0, z1=1.0, fx=0.5, fy=0.5, phone=False):
    """Part of a clip, sped up or slowed to take `alloc` seconds, with a slow push in toward a point of the picture."""
    src = os.path.join(OUT, clip + ".mp4")
    speed = f"(PTS-STARTPTS)*{alloc}/{end - start}"
    if phone:
        vf = f"setpts={speed},fps={FPS},scale=406:880:flags=lanczos"
    else:
        vf = (f"setpts={speed},fps={FPS},scale=w='trunc(1536*({z0}+({z1}-{z0})*t/{alloc})/2)*2':h=-2:eval=frame:flags=lanczos,"
              f"crop=1536:864:'(iw-1536)*{fx}':'(ih-864)*{fy}'")
    ff("-ss", start, "-t", end - start, "-i", src, "-vf", vf, "-t", alloc, "-c:v", "libx264", "-crf", 12, "-preset", "veryfast", "-pix_fmt", "yuv420p", path(name))
    return path(name)

def join(name, files):
    lst = path(name + ".txt")
    open(lst, "w").write("".join(f"file '{f}'\n" for f in files))
    ff("-f", "concat", "-safe", 0, "-i", lst, "-c", "copy", path(name))
    return path(name)

def caption_chain(idx, caps, dur):
    """The filter parts that fade the captions in and out, and the inputs they need."""
    ins, parts, last = [], [], None
    for k, (cid, a, b) in enumerate(caps):
        ins += ["-loop", 1, "-t", dur, "-i", path(f"caps/{cid}.png")]
        parts.append(f"[{idx + k}:v]format=rgba,fade=t=in:st={a}:d=0.3:alpha=1,fade=t=out:st={b - 0.3}:d=0.3:alpha=1[cap{k}]")
    return ins, parts

def compose(name, dur, body=None, kind="desktop", caps=(), full=None):
    """Puts a body (footage in a window, or a phone) on the background with its captions; full is a finished 1920x1080 video."""
    inputs = ["-loop", 1, "-t", dur, "-i", path("bg.png")]
    parts = []
    n = 1
    if full:
        inputs += ["-i", full]
        base = f"[{n}:v]"; n += 1
        chain = f"{base}fps={FPS},format=yuv420p[b0]"
        parts.append(chain)
        cur = "b0"
    else:
        sh = "shadow-desktop.png" if kind == "desktop" else "shadow-phone.png"
        mk = "mask-desktop.png" if kind == "desktop" else "mask-phone.png"
        inputs += ["-loop", 1, "-t", dur, "-i", path(sh), "-i", body, "-loop", 1, "-t", dur, "-i", path(mk)]
        if kind == "desktop":
            win_x, win_y, sh_x, sh_y = 192, 40, 192 - 70, 40 - 50
        else:
            win_x, win_y, sh_x, sh_y = 757, 40, 757 - 70, 40 - 50
        parts.append(f"[{n + 2}:v]format=gray[m]")
        parts.append(f"[{n + 1}:v]format=yuva420p[bd]")
        parts.append(f"[bd][m]alphamerge[win]")
        parts.append(f"[0:v][{n}:v]overlay={sh_x}:{sh_y}[s0]")
        parts.append(f"[s0][win]overlay={win_x}:{win_y}[b0]")
        cur = "b0"
        n += 3
        if kind == "phone":
            inputs += ["-loop", 1, "-t", dur, "-i", path("bezel.png")]
            parts.append(f"[{n}:v]format=rgba[bz]")
            parts.append(f"[b0][bz]overlay=745:28[b1]")
            cur = "b1"
            n += 1
    cins, cparts = caption_chain(n, caps, dur)
    inputs += cins
    parts += cparts
    for k in range(len(caps)):
        parts.append(f"[{cur}][cap{k}]overlay=192:930[c{k}]")
        cur = f"c{k}"
    parts.append(f"[{cur}]format=yuv420p,fps={FPS}[out]")
    ff(*inputs, "-filter_complex", ";".join(parts), "-map", "[out]", "-t", dur, "-c:v", "libx264", "-crf", 14, "-preset", "veryfast", "-pix_fmt", "yuv420p", path(name))
    return path(name)

def graphic(name, html, dur):
    if os.environ.get("REUSE") and os.path.exists(path(name + ".mp4")):
        return path(name + ".mp4")
    open(path(name + ".html"), "w").write(html)
    go("render", path(name + ".html"), path(name + "-frames"), dur, FPS)
    ff("-framerate", FPS, "-i", path(name + "-frames/%05d.png"), "-c:v", "libx264", "-crf", 14, "-preset", "veryfast", "-pix_fmt", "yuv420p", path(name + ".mp4"))
    return path(name + ".mp4")

# ---- the film ----------------------------------------------------------------------------------------------------------
def build(mode):
    assets()
    segs = []
    D = lambda bars: round(bars * BAR, 3)

    d = D(4)
    segs.append(compose("s01.mp4", d, full=graphic("g-intro", pages.intro(), d), caps=[("c01", 0.25, 3.55), ("c02", 4.0, d - 0.3)]))

    d = D(4)
    parts = [piece("p3a.mp4", "c03", 5.5, 10.5, 2.6, 1.6, 1.7, 0.5, 0.37), piece("p3b.mp4", "c03", 10.5, 15.5, 1.9, 1.7, 1.75, 0.3, 0.6),
             piece("p3c.mp4", "c03", 15.5, 19.7, round(d - 4.5, 3), 1.8, 1.9, 0.08, 0.95)]
    segs.append(compose("s03.mp4", d, join("b3.mp4", parts), caps=[("c03", 0.3, d - 0.3)]))

    d = D(4)
    segs.append(compose("s04.mp4", d, piece("p4.mp4", "c04", 1.0, 10.5, d, phone=True), kind="phone", caps=[("c04", 0.3, d - 0.3)]))

    d = D(4)
    segs.append(compose("s05.mp4", d, piece("p5.mp4", "c05", 1.2, 10.8, d, 1.85, 2.0, 0.5, 0.36), caps=[("c05", 0.3, d - 0.3)]))

    d = D(3)
    segs.append(compose("s06.mp4", d, piece("p6.mp4", "c06", 3.0, 16.0, d, 1.0, 1.06), caps=[("c06", 0.3, d - 0.3)]))
    d = D(1)
    segs.append(compose("s06b.mp4", d, full=graphic("g-bestof", pages.bestof(), d)))

    d = D(3)
    segs.append(compose("s07.mp4", d, piece("p7.mp4", "c07", 0.8, 9.6, d, 1.0, 1.12, 0.85, 0.1), caps=[("c07", 0.3, d - 0.3)]))
    segs.append(compose("s08.mp4", d, piece("p8.mp4", "c08", 1.5, 12.5, d, 1.75, 1.85, 0.5, 0.5), caps=[("c08", 0.3, d - 0.3)]))
    segs.append(compose("s09.mp4", d, piece("p9.mp4", "c09", 1.5, 13.0, d, phone=True), kind="phone", caps=[("c09", 0.3, d - 0.3)]))
    segs.append(compose("s10.mp4", d, full=graphic("g-servers", pages.servers(), d), caps=[("c10", 0.3, d - 0.3)]))
    segs.append(compose("s11.mp4", d, full=graphic("g-trust", pages.trust(), d), caps=[("c11", 0.3, d - 0.3)]))
    d = D(4)
    segs.append(compose("s12.mp4", d, full=graphic("g-end", pages.end(), d)))

    video = join("film-silent.mp4", segs)
    total = sum(float(run(["ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", s]).stdout) for s in segs)
    print(f"{len(segs)} segments, {total:.2f} s")
    final = os.path.join(OUT, "handoff-demo.mp4")
    afilter = f"afade=t=in:d=0.8,afade=t=out:st={total - BAR:.2f}:d={BAR:.2f},loudnorm=I=-16:TP=-1.5:LRA=11"
    vfilter = f"fade=t=in:d=0.5,fade=t=out:st={total - 1.2:.2f}:d=1.2"
    crf = "26" if mode == "final" else "22"
    ff("-i", video, *( ["-ss", "0", "-t", f"{total:.3f}", "-i", MUSIC] if MUSIC else []), "-vf", vfilter, *( ["-af", afilter, "-c:a", "aac", "-b:a", "128k"] if MUSIC else ["-an"]),
       "-c:v", "libx264", "-preset", "slow", "-crf", crf, "-pix_fmt", "yuv420p", "-movflags", "+faststart", "-t", f"{total:.3f}", final)
    size = os.path.getsize(final) / 1e6
    print(f"{final}: {size:.1f} MB")

if __name__ == "__main__":
    build(sys.argv[1] if len(sys.argv) > 1 else "rough")
