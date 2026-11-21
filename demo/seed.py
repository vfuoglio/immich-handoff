#!/usr/bin/env python3
"""Fills the demonstration (run by the `seed` service of demo/docker-compose.yml, once): an Immich administrator, three people, sample photos, and a little history in the app."""
import io, json, os, sys, time, urllib.request, urllib.error, uuid
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

# ---- pictures: real photos from demo/photos (public domain, see photos/CREDITS.md) ----------------------------------------
PHOTOS = os.path.join(os.path.dirname(os.path.abspath(__file__)), "photos")
# who has what: Alice a trip to Innsbruck, Bob a party, Carol travels (the numbers are the file names' prefixes)
OWNS = {1: [26, 27, 28, 29, 30, 32, 33], 2: [1, 3, 16, 17, 20, 25, 18], 3: [34, 35, 36, 37, 7, 10, 11]}

def photo_file(n):
    return next(f for f in sorted(os.listdir(PHOTOS)) if f.startswith(f"{n:02d}-"))

def jpeg(img):
    b = io.BytesIO(); img.save(b, "JPEG", quality=88); return b.getvalue()

def photos(owner_seed):
    """Seven real photos, a near-duplicate of the first (for the duplicate check), a too-dark one and a blurry one."""
    files = [photo_file(n) for n in OWNS[owner_seed]]
    out = []
    for f in files:
        out.append((f, open(os.path.join(PHOTOS, f), "rb").read()))
    first = Image.open(os.path.join(PHOTOS, files[0])).convert("RGB")
    twin = first.copy(); twin.putpixel((5, 5), (1, 2, 3)); d = ImageDraw.Draw(twin); d.ellipse([20, 20, 60, 60], fill=(250, 250, 250))
    out.append(("twin-" + files[0], jpeg(twin)))
    dark = Image.open(os.path.join(PHOTOS, files[1])).convert("RGB").point(lambda v: int(v * 0.06))
    out.append(("too-dark-" + files[1], jpeg(dark)))
    blur = Image.open(os.path.join(PHOTOS, files[2])).convert("RGB").filter(ImageFilter.GaussianBlur(26))
    out.append(("blurry-" + files[2], jpeg(blur)))
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
    s, album, _ = call("POST", IMMICH + "/albums", {"albumName": "Holiday", "assetIds": ids["alice"][:7]}, {"Authorization": "Bearer " + tokens["alice"]})
    print("alice's album Holiday:", s)
    # Alice shares the album with Bob, and shares her whole library with him as a partner: so Bob has photos he can see but
    # does not own, and the Add to my library button has something to show
    _, bobme, _ = call("GET", IMMICH + "/users/me", None, {"Authorization": "Bearer " + tokens["bob"]})
    if isinstance(album, dict) and isinstance(bobme, dict):
        print("alice shares Holiday with bob:", call("PUT", f"{IMMICH}/albums/{album['id']}/users", {"albumUsers": [{"userId": bobme["id"], "role": "viewer"}]}, {"Authorization": "Bearer " + tokens["alice"]})[0])
        print("alice shares her library with bob:", call("POST", f"{IMMICH}/partners/{bobme['id']}", None, {"Authorization": "Bearer " + tokens["alice"]})[0])

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
    seed_1_4(adm, tokens, ids, now)

def seed_1_4(adm, tokens, ids, now):
    """What the features after 1.3.0 need from Immich itself: people (for sharing a person's photos both ways), memories (for On this day),
    and an external library for Bob (for albums from folders and for stacking). The rest is done by hand, as demo/README.md says."""
    H = lambda n: {"Authorization": "Bearer " + tokens[n]}
    # people with a face on a few photos each (machine learning is off in the demo, so Immich finds none by itself)
    for name, person in (("alice", "Mia"), ("bob", "Leo")):
        s, p, _ = call("POST", IMMICH + "/people", {"name": person}, H(name))
        if s not in (200, 201): print("person", person, s, p); continue
        for a in ids[name][:4]:
            call("POST", IMMICH + "/faces", {"assetId": a, "personId": p["id"], "imageWidth": 64, "imageHeight": 64, "x": 10, "y": 10, "width": 30, "height": 30}, H(name))
        print(name, "has a person called", person, "on four photos")
    # On this day: photos of Alice and Bob taken on today's date in earlier years, and a memory for each year
    for name, n in (("alice", 3), ("bob", 2)):
        files = photos({"alice": 1, "bob": 2}[name])
        for y in range(1, n + 1):
            when = now.replace(year=now.year - y, hour=10, minute=y, second=0, microsecond=0)
            fname, data = files[y]
            aid = upload(tokens[name], "memory-" + fname, data, when)
            s, j, _ = call("POST", IMMICH + "/memories", {"type": "on_this_day", "memoryAt": now.replace(hour=0, minute=0, second=0, microsecond=0).isoformat().replace("+00:00", "Z"),
                                                          "data": {"year": now.year - y}, "assetIds": [aid]}, H(name))
            print(name, "memory", now.year - y, s)
    # Bob's external library: /external/photos (folders), /external/photos-old and /external/stacks (RAW, edits and a burst)
    _, bobme, _ = call("GET", IMMICH + "/users/me", None, H("bob"))
    A = {"Authorization": "Bearer " + adm}
    s, lib, _ = call("POST", IMMICH + "/libraries", {"ownerId": bobme["id"], "name": "Bob's folders", "importPaths": ["/external/photos", "/external/photos-old", "/external/stacks"]}, A)
    print("bob's external library:", s)
    if s in (200, 201): print("scan:", call("POST", f"{IMMICH}/libraries/{lib['id']}/scan", None, A)[0])

if __name__ == "__main__":
    main()
