#!/usr/bin/env python3
"""Fills the demonstration (run by the `seed` service of demo/docker-compose.yml, once): an Immich administrator, three people, sample photos, and a little history in the app."""
import io, json, math, os, random, sys, time, urllib.request, urllib.error, uuid
from datetime import datetime, timedelta, timezone
from PIL import Image, ImageDraw, ImageFilter

IMMICH = os.environ.get("DEMO_IMMICH", "http://localhost:2295") + "/api"
APP = os.environ.get("DEMO_APP", "http://localhost:2295/handoff")
PW = {"admin": "Demo-admin-1", "alice": "Demo-pass-1", "bob": "Demo-pass-1", "carol": "Demo-pass-1"}

def call(method, url, body=None, headers=None, raw=False):
    h = dict(headers or {})
    data = None
    if body is not None and not isinstance(body, (bytes, bytearray)):
        data = json.dumps(body).encode(); h["Content-Type"] = "application/json"
    elif body is not None:
        data = body
    req = urllib.request.Request(url, data=data, method=method, headers=h)
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            b = r.read()
            return r.status, (b if raw else (json.loads(b) if b else None)), r.headers
    except urllib.error.HTTPError as e:
        b = e.read()
        try: return e.code, json.loads(b), e.headers
        except Exception: return e.code, b, e.headers

def wait():
    for _ in range(120):
        try:
            if call("GET", IMMICH + "/server/ping")[0] == 200: return
        except Exception: pass
        time.sleep(2)
    sys.exit("Immich did not come up")

def login(email, pw):
    s, j, h = call("POST", IMMICH + "/auth/login", {"email": email, "password": pw})
    assert s in (200, 201), (email, s, j)
    cookies = "; ".join(c.split(";")[0] for c in h.get_all("Set-Cookie") or [])
    return j["accessToken"], cookies

# ---- pictures: scenes with a sky, a sun, hills and some texture, so each one is different and sharp --------------------
def scene(seed, w=1600, h=1067):
    r = random.Random(seed)
    palettes = [((255, 190, 120), (90, 120, 200)), ((120, 200, 255), (20, 60, 140)), ((255, 140, 160), (70, 40, 120)), ((250, 230, 150), (50, 150, 120)),
                ((200, 220, 255), (60, 80, 120)), ((255, 170, 90), (40, 20, 60)), ((170, 255, 200), (20, 90, 100))]
    top, bottom = palettes[seed % len(palettes)]
    img = Image.new("RGB", (w, h))
    px = img.load()
    for y in range(h):
        t = y / h
        c = tuple(int(top[i] * (1 - t) + bottom[i] * t) for i in range(3))
        for x in range(w): px[x, y] = c
    d = ImageDraw.Draw(img)
    sx, sy, sr = r.randint(w // 5, 4 * w // 5), r.randint(h // 8, h // 3), r.randint(50, 110)
    d.ellipse([sx - sr, sy - sr, sx + sr, sy + sr], fill=(255, 245, 200))
    for layer in range(4):
        base = h * (0.55 + layer * 0.1)
        col = tuple(max(0, int(c * (0.55 - layer * 0.1))) for c in (90, 140, 90))
        pts = [(0, h)]
        for x in range(0, w + 40, 40):
            pts.append((x, base + 60 * math.sin(x / (110 + layer * 40) + r.random() * 6) + r.randint(-18, 18)))
        pts.append((w, h))
        d.polygon(pts, fill=col)
    for _ in range(900):  # grain and detail, so the picture is sharp
        x, y = r.randint(0, w - 1), r.randint(h // 2, h - 1)
        d.point((x, y), fill=tuple(r.randint(30, 200) for _ in range(3)))
    for _ in range(70):
        x, y = r.randint(0, w), r.randint(h // 2, h)
        d.rectangle([x, y, x + r.randint(2, 9), y + r.randint(2, 9)], fill=tuple(r.randint(20, 220) for _ in range(3)))
    return img

def jpeg(img):
    b = io.BytesIO(); img.save(b, "JPEG", quality=88); return b.getvalue()

def photos(owner_seed):
    out = []
    for i in range(6):
        out.append((f"scene-{owner_seed}-{i + 1}.jpg", jpeg(scene(owner_seed * 10 + i))))
    base = scene(owner_seed * 10 + 7)
    out.append((f"twin-a-{owner_seed}.jpg", jpeg(base)))
    twin = base.copy(); twin.putpixel((5, 5), (1, 2, 3)); d = ImageDraw.Draw(twin); d.ellipse([20, 20, 60, 60], fill=(250, 250, 250))
    out.append((f"twin-b-{owner_seed}.jpg", jpeg(twin)))
    dark = scene(owner_seed * 10 + 8).point(lambda v: int(v * 0.06))
    out.append((f"too-dark-{owner_seed}.jpg", jpeg(dark)))
    blur = scene(owner_seed * 10 + 9).filter(ImageFilter.GaussianBlur(26))
    out.append((f"blurry-{owner_seed}.jpg", jpeg(blur)))
    return out

def upload(token, name, data, when):
    boundary = uuid.uuid4().hex
    fields = {"deviceAssetId": f"{name}-{uuid.uuid4().hex[:8]}", "deviceId": "demo-seed", "fileCreatedAt": when.isoformat().replace("+00:00", "Z"),
              "fileModifiedAt": when.isoformat().replace("+00:00", "Z")}
    body = b""
    for k, v in fields.items():
        body += f'--{boundary}\r\nContent-Disposition: form-data; name="{k}"\r\n\r\n{v}\r\n'.encode()
    body += f'--{boundary}\r\nContent-Disposition: form-data; name="assetData"; filename="{name}"\r\nContent-Type: image/jpeg\r\n\r\n'.encode() + data + f"\r\n--{boundary}--\r\n".encode()
    s, j, _ = call("POST", IMMICH + "/assets", body, {"Authorization": "Bearer " + token, "Content-Type": "multipart/form-data; boundary=" + boundary})
    assert s in (200, 201), (name, s, j)
    return j["id"]

def app_post(cookie, path, form):
    data = "&".join(f"{k}={urllib.request.quote(str(v))}" for k, v in form.items()).encode()
    req = urllib.request.Request(APP + path, data=data, method="POST", headers={"Cookie": cookie, "X-Requested-With": "immich-handoff", "Content-Type": "application/x-www-form-urlencoded"})
    try:
        with urllib.request.urlopen(req, timeout=120) as r: return r.status, r.read().decode()
    except urllib.error.HTTPError as e: return e.code, e.read().decode()

def main():
    wait()
    print("Immich is up")
    call("POST", IMMICH + "/auth/admin-sign-up", {"email": "admin@demo.test", "name": "Demo Admin", "password": PW["admin"]})
    adm, _ = login("admin@demo.test", PW["admin"])
    A = {"Authorization": "Bearer " + adm}
    for name in ("alice", "bob", "carol"):
        call("POST", IMMICH + "/admin/users", {"email": f"{name}@demo.test", "name": name.capitalize(), "password": PW[name], "shouldChangePassword": False}, A)
    tokens, cookies = {}, {}
    for name in ("alice", "bob", "carol"):
        tokens[name], cookies[name] = login(f"{name}@demo.test", PW[name])
        call("PUT", IMMICH + "/users/me/preferences", {"tags": {"enabled": True, "sidebarWeb": True}}, {"Authorization": "Bearer " + tokens[name]})
    now = datetime.now(timezone.utc)
    ids = {}
    for n, name in enumerate(("alice", "bob", "carol"), start=1):
        ids[name] = []
        for i, (fname, data) in enumerate(photos(n)):
            ids[name].append(upload(tokens[name], fname, data, now - timedelta(days=7 - i % 6, hours=i)))
        print(name, "has", len(ids[name]), "photos")
    s, album, _ = call("POST", IMMICH + "/albums", {"albumName": "Holiday", "assetIds": ids["alice"][:6]}, {"Authorization": "Bearer " + tokens["alice"]})
    print("alice's album Holiday:", s)

    # a little history in the app: Alice offers "Family album" (open); Bob joins it and hands over five photos
    print("app:", app_post(cookies["alice"], "/services", {"name": "Family album", "tag": "family", "policy": "open", "visibility": "listed"})[0])
    print("app: bob joins:", app_post(cookies["bob"], "/join", {"service": "Family album", "mode": "copy"})[0])
    # Alice also offers a service that needs approval, for Carol to ask to join
    print("app: club:", app_post(cookies["alice"], "/services", {"name": "Photo club", "tag": "club", "policy": "approval", "visibility": "listed"})[0])
    time.sleep(8)  # Bob's workflow is made in the background
    s, j, _ = call("PUT", IMMICH + "/tags", {"tags": ["family"]}, {"Authorization": "Bearer " + tokens["bob"]})
    tag = j[0]["id"]
    call("PUT", f"{IMMICH}/tags/{tag}/assets", {"ids": ids["bob"][:5]}, {"Authorization": "Bearer " + tokens["bob"]})
    print("bob tagged five photos 'family'")

if __name__ == "__main__":
    main()
