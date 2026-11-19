"""Posts the sample photos to a running sidecar and checks what it finds (used by CI: python3 check.py http://localhost:8090)."""
import json
import math
import sys
import urllib.request

base = sys.argv[1] if len(sys.argv) > 1 else "http://localhost:8090"


def post(path, data):
    req = urllib.request.Request(base + path, data=data, method="POST", headers={"Content-Type": "image/jpeg"})
    with urllib.request.urlopen(req, timeout=60) as r:
        return json.load(r)


def analyze(name):
    return post("/analyze", open("samples/" + name, "rb").read())["animals"]


def cos(a, b):
    return sum(x * y for x, y in zip(a, b))


assert urllib.request.urlopen(base + "/ping", timeout=10).read() == b"ok", "ping"
fails = []
found = {}
for name, kind in [("cat-tabby.jpg", "cat"), ("cat-black.jpg", "cat"), ("dog.jpg", "dog"), ("dog-with-person.jpg", "dog")]:
    a = analyze(name)
    found[name] = a
    ok = any(x["kind"] == kind and x["score"] >= 0.4 for x in a)
    print(("ok   " if ok else "FAIL ") + name + ": " + ", ".join("%s %.2f" % (x["kind"], x["score"]) for x in a))
    if not ok:
        fails.append(name)
    for x in a:
        if len(x["print"]) != 384 or abs(math.sqrt(cos(x["print"], x["print"])) - 1) > 0.01 or not all(0 <= v <= 1 for v in x["box"]):
            fails.append(name + " (shape)")
# an image with no animal in it, and the same cat mirrored: it is nearer to itself than to a dog
import io
from PIL import Image, ImageOps


def jpeg(img):
    buf = io.BytesIO()
    img.save(buf, "JPEG")
    return buf.getvalue()


none = post("/analyze", jpeg(Image.new("RGB", (320, 240), (120, 160, 200))))["animals"]
print(("ok   " if not none else "FAIL ") + "a plain picture: %d animals" % len(none))
if none:
    fails.append("plain picture")
cat = found["cat-tabby.jpg"][0]["print"]
mirrored = post("/analyze", jpeg(ImageOps.mirror(Image.open("samples/cat-tabby.jpg").convert("RGB"))))["animals"][0]["print"]
dog = max(found["dog-with-person.jpg"], key=lambda x: x["score"])["print"]
print("cat~mirrored cat %.2f, cat~dog %.2f" % (cos(cat, mirrored), cos(cat, dog)))
if cos(cat, mirrored) < 0.8 or cos(cat, mirrored) <= cos(cat, dog):
    fails.append("a cat is nearer itself than a dog")
# a body that is not a photo is refused, not crashed on
try:
    post("/analyze", b"not a photo")
    fails.append("garbage accepted")
except urllib.error.HTTPError as e:
    print("ok   garbage refused: %d" % e.code)
if fails:
    print("FAILED:", fails)
    sys.exit(1)
print("all good")
