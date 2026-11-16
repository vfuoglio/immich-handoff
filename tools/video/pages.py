"""The web pages the film's graphics are made from (rendered frame by frame by `go run . video render`), and the captions.
Everything here is plain HTML and CSS; the animations are CSS keyframes, which the renderer steps through exactly."""
import glob, os, html, pathlib

def uri(path):
    return pathlib.Path(path).as_uri()

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, "..", ".."))
FONTS = os.path.join(ROOT, "internal", "web", "static", "fonts")
PHOTOS = sorted(glob.glob(os.path.join(ROOT, "demo", "photos", "*.jpg")))
ICON = os.path.join(ROOT, "docs", "images", "icon-512.png")

def photo(n):
    return next(p for p in PHOTOS if os.path.basename(p).startswith(f"{n:02d}-"))

HEAD = f"""<!doctype html><meta charset="utf-8"><style>
@font-face {{ font-family: Display; src: url('{uri(FONTS + '/bricolage-grotesque-latin-opsz.woff2')}'); font-weight: 200 800; }}
@font-face {{ font-family: Text; src: url('{uri(FONTS + '/figtree-latin.woff2')}'); font-weight: 300 900; }}
* {{ box-sizing: border-box; }}
html, body {{ margin: 0; width: 1920px; height: 1080px; overflow: hidden; }}
body {{ font-family: Text, sans-serif; color: #fff; background: radial-gradient(1100px 700px at 85% 110%, #7a3d8c 0, transparent 60%), linear-gradient(135deg, #14104a 0, #2a1a66 55%, #3a2078 100%); }}
.d {{ font-family: Display, sans-serif; }}
.tile {{ position: absolute; border-radius: 22px; background-size: cover; background-position: center; box-shadow: 0 18px 40px rgb(0 0 0 / .45); }}
.icon {{ width: 190px; height: 190px; border-radius: 44px; box-shadow: 0 16px 46px rgb(0 0 0 / .5); }}
</style>"""

def tile(n, x, y, w, h, extra=""):
    return f'<div class="tile" style="left:{x}px;top:{y}px;width:{w}px;height:{h}px;background-image:url(\'{uri(photo(n))}\');{extra}"></div>'

def intro():
    order = [26, 1, 17, 28, 20, 5, 12, 35, 24, 3, 29, 16]
    tiles = []
    for i, n in enumerate(order):
        col, row = i % 4, i // 4
        x, y = 324 + col * 324, 216 + row * 224
        tiles.append(tile(n, x, y, 300, 200, f"animation: drop .7s cubic-bezier(.2,.9,.3,1) {0.1 + i * 0.11:.2f}s both;"))
    return HEAD + f"""<style>
@keyframes drop {{ from {{ opacity: 0; transform: translateY(-160px) scale(.7) rotate(-4deg); }} to {{ opacity: 1; transform: none; }} }}
@keyframes dim {{ to {{ opacity: .16; filter: blur(10px); transform: scale(1.06); }} }}
@keyframes rise {{ from {{ opacity: 0; transform: translateY(60px) scale(.92); }} to {{ opacity: 1; transform: none; }} }}
@keyframes show {{ from {{ opacity: 0; transform: translateY(18px); }} to {{ opacity: 1; transform: none; }} }}
.grid {{ position: absolute; inset: 0; animation: dim 1s 3.5s both; }}
.title {{ position: absolute; left: 0; right: 0; top: 250px; text-align: center; animation: rise .9s 4.0s both; }}
.title h1 {{ margin: 28px 0 0; font: 800 132px/1 Display; letter-spacing: -3px; }}
.title p {{ margin: 22px 0 0; font: 500 46px Text; color: #cfc3f5; animation: show .8s 6.3s both; }}
.icon {{ display: block; margin: 0 auto; }}
</style><div class="grid">{''.join(tiles)}</div>
<div class="title"><img class="icon" src="{uri(ICON)}"><h1>immich-handoff</h1><p>By tag, button, rule or QR code.</p></div>"""

def bestof():
    loved = [28, 26, 17, 20, 1, 5]
    t = []
    for i, n in enumerate(loved):
        col, row = i % 3, i // 3
        x, y = 360 + col * 400, 250 + row * 270
        t.append(tile(n, x, y, 380, 250, f"animation: drop .6s cubic-bezier(.2,.9,.3,1) {0.12 + i * 0.1:.2f}s both;"))
        t.append(f'<div style="position:absolute;left:{x + 318}px;top:{y + 12}px;font-size:46px;color:#ff5c93;filter:drop-shadow(0 3px 6px #0008);animation:pop .5s {0.5 + i * 0.1:.2f}s both">&#9829;</div>')
    return HEAD + f"""<style>
@keyframes drop {{ from {{ opacity: 0; transform: translateY(-90px) scale(.8); }} to {{ opacity: 1; transform: none; }} }}
@keyframes pop {{ 0% {{ opacity: 0; transform: scale(.2); }} 70% {{ opacity: 1; transform: scale(1.35); }} 100% {{ opacity: 1; transform: scale(1); }} }}
@keyframes show {{ from {{ opacity: 0; }} to {{ opacity: 1; }} }}
h2 {{ position: absolute; left: 0; right: 0; top: 130px; text-align: center; margin: 0; font: 700 58px Display; animation: show .6s .1s both; }}
h2 small {{ display: block; font: 500 32px Text; color: #cfc3f5; margin-top: 10px; }}
</style><h2>Mia's birthday<small>the best-of album: what guests loved</small></h2>{''.join(t)}"""

def servers():
    left = [26, 28, 29, 32]
    right = [1, 17, 20, 12]
    def card(x, tint, label, nums):
        ph = ''.join(tile(n, x + 30 + (i % 2) * 190, 322 + (i // 2) * 140, 175, 125, "box-shadow:none;border-radius:14px;") for i, n in enumerate(nums))
        return f'<div style="position:absolute;left:{x}px;top:250px;width:420px;height:420px;border-radius:34px;background:{tint};border:2px solid rgb(255 255 255 / .22);box-shadow:0 24px 60px rgb(0 0 0 / .45)"></div><div class="d" style="position:absolute;left:{x + 30}px;top:268px;font-size:34px;font-weight:700">{label}</div>{ph}'
    tokens = []
    for i in range(3):
        tokens.append(f'<div class="tok" style="top:392px;animation:r 2.2s linear {0.9 + i * 0.7:.1f}s infinite;background-image:url(\'{uri(photo(left[i]))}\')"></div>')
        tokens.append(f'<div class="tok" style="top:520px;animation:l 2.2s linear {1.1 + i * 0.7:.1f}s infinite;background-image:url(\'{uri(photo(right[i]))}\')"></div>')
    return HEAD + f"""<style>
@keyframes r {{ 0% {{ left: 560px; opacity: 0; }} 12% {{ opacity: 1; }} 88% {{ opacity: 1; }} 100% {{ left: 1290px; opacity: 0; }} }}
@keyframes l {{ 0% {{ left: 1290px; opacity: 0; }} 12% {{ opacity: 1; }} 88% {{ opacity: 1; }} 100% {{ left: 560px; opacity: 0; }} }}
@keyframes show {{ from {{ opacity: 0; transform: translateY(14px) scale(.9); }} to {{ opacity: 1; transform: none; }} }}
.tok {{ position: absolute; width: 76px; height: 76px; border-radius: 16px; background-size: cover; box-shadow: 0 8px 22px rgb(0 0 0 / .5); opacity: 0; }}
.lane {{ position: absolute; left: 560px; width: 790px; height: 4px; border-top: 4px dashed rgb(152 166 255 / .55); }}
.chip {{ position: absolute; left: 790px; top: 690px; padding: 10px 26px; border-radius: 99px; font: 700 34px Text; background: #1f7a52; border: 2px solid #5be0a3; animation: show .6s .5s both; }}
.sub {{ position: absolute; left: 0; right: 0; top: 760px; text-align: center; font: 500 34px Text; color: #cfc3f5; animation: show .6s 1.1s both; }}
</style>{card(120, 'rgb(90 80 200 / .35)', 'Your Immich', left)}{card(1380, 'rgb(190 70 150 / .35)', 'Their Immich', right)}
<div class="lane" style="top:430px"></div><div class="lane" style="top:558px"></div>{''.join(tokens)}
<div class="chip">Agreed</div><div class="sub">Photos both ways. Signed. No echo.</div>"""

def trust():
    ims = [26, 1, 17, 28, 20, 5, 12, 35, 24, 3, 29, 16, 33, 36, 7, 10]
    bg = ''.join(tile(n, (i % 4) * 500 - 60, (i // 4) * 290 - 20, 480, 276, "box-shadow:none;border-radius:0;opacity:.55;") for i, n in enumerate(ims))
    lines = ["Keys you can delete any time", "A verified copy before anything moves", "Tested against a real Immich"]
    li = ''.join(f'<div class="ln" style="animation-delay:{0.4 + i * 1.15:.2f}s"><span>&#10003;</span>{html.escape(t)}</div>' for i, t in enumerate(lines))
    return HEAD + f"""<style>
@keyframes show {{ from {{ opacity: 0; transform: translateY(26px); }} to {{ opacity: 1; transform: none; }} }}
@keyframes drift {{ from {{ transform: translateX(0) scale(1.05); }} to {{ transform: translateX(-70px) scale(1.05); }} }}
.bg {{ position: absolute; inset: 0; filter: blur(6px); animation: drift 6s linear both; }}
.veil {{ position: absolute; inset: 0; background: linear-gradient(135deg, rgb(20 16 74 / .82), rgb(42 26 102 / .72)); }}
.col {{ position: absolute; left: 320px; top: 290px; }}
.ln {{ margin: 0 0 34px; font: 700 66px Display; letter-spacing: -1px; animation: show .7s both; }}
.ln span {{ display: inline-block; width: 74px; height: 74px; line-height: 74px; margin-right: 26px; text-align: center; border-radius: 50%; background: #1f7a52; border: 3px solid #5be0a3; font-size: 44px; }}
</style><div class="bg">{bg}</div><div class="veil"></div><div class="col">{li}</div>"""

def end():
    return HEAD + f"""<style>
@keyframes show {{ from {{ opacity: 0; transform: translateY(22px); }} to {{ opacity: 1; transform: none; }} }}
.c {{ position: absolute; left: 0; right: 0; top: 190px; text-align: center; }}
.c h1 {{ margin: 30px 0 0; font: 800 124px/1 Display; letter-spacing: -3px; animation: show .8s .3s both; }}
.c p {{ margin: 26px 0 0; font: 600 48px Text; animation: show .8s 1s both; }}
.url {{ color: #98a6ff; }}
.c .demo {{ margin-top: 40px; display: inline-block; padding: 16px 38px; border-radius: 18px; background: rgb(255 255 255 / .1); border: 2px solid rgb(255 255 255 / .22); font: 500 40px 'Courier New', monospace; animation: show .8s 1.7s both; }}
.credit {{ position: absolute; left: 0; right: 0; bottom: 36px; text-align: center; font: 400 24px Text; color: #b9acea; animation: show .8s 2.4s both; }}
.icon {{ display: block; margin: 0 auto; animation: show .8s both; }}
</style><div class="c"><img class="icon" src="{uri(ICON)}"><h1>immich-handoff</h1><p class="url">github.com/vfuoglio/immich-handoff</p><div class="demo">Try the demo: docker compose up</div></div>
<div class="credit">Photos: Wikimedia Commons, CC0 and public domain. Music: "Mister Blues", CC0, via OpenGameArt. Independent project, not affiliated with Immich.</div>"""

CAPTIONS = [("c01", "Everyone's photos..."), ("c02", "...in the right library."), ("c03", "Make a link. Share a QR code."), ("c04", "Guests need only a browser."),
            ("c05", "They arrive in your library."), ("c06", "Show them. Keep the best."), ("c07", "Hand over from inside Immich."), ("c08", "Rules suggest. You decide."),
            ("c09", "Deliver a shoot to a client."), ("c10", "Share albums across servers."), ("c11", "You stay in control.")]

def captions():
    """All the captions on one page, two columns, so each is inside the 1080 pixels the renderer can see."""
    items = ''.join(f'<div class="cap" data-still="{k}"><i></i>{html.escape(t)}</div>' for k, t in CAPTIONS)
    return HEAD + f"""<style>
body {{ background: none !important; }}
.wrap {{ display: grid; grid-template-columns: 940px 940px; gap: 30px 0; padding: 20px; justify-items: start; }}
.cap {{ display: inline-flex; align-items: center; gap: 22px; padding: 20px 40px 20px 28px; border-radius: 24px; font: 700 58px/1.1 Display; letter-spacing: -1px;
  background: rgb(14 10 48 / .82); border: 2px solid rgb(255 255 255 / .18); box-shadow: 0 10px 30px rgb(0 0 0 / .4); white-space: nowrap; }}
.cap i {{ width: 9px; height: 62px; border-radius: 6px; background: linear-gradient(180deg, #98a6ff, #e1409a, #f39a5b); }}
</style><div class="wrap">{items}</div>"""
