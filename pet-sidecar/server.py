"""The pet sidecar of Immich Handoff: finds the cats and dogs in a photo and fingerprints each one.

Stateless: POST /analyze with a photo's bytes answers with every cat or dog found (kind, score, box and a 384-number fingerprint
of the crop); GET /ping answers "ok". It keeps nothing, writes nothing and calls nobody. YOLOX-s finds the animals and DINOv2-small
(int8) fingerprints the crops; both Apache-2.0, run with ONNX Runtime only (no PyTorch).
"""
import io
import json
import os
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import numpy as np
import onnxruntime as ort
from PIL import Image, ImageOps

MODELS = os.environ.get("MODELS_DIR", "/models")
MAX_BYTES = int(os.environ.get("MAX_IMAGE_MB", "25")) * 1024 * 1024
THREADS = int(os.environ.get("ORT_THREADS", "2"))
CONCURRENT = int(os.environ.get("MAX_CONCURRENT", "2"))
TIMEOUT = int(os.environ.get("REQUEST_TIMEOUT", "60"))
MIN_SCORE = float(os.environ.get("MIN_SCORE", "0.30"))
MAX_ANIMALS = 6
CLASSES = {15: "cat", 16: "dog"}  # COCO ids
SIZE = 640

Image.MAX_IMAGE_PIXELS = 120_000_000


def session(name):
    opts = ort.SessionOptions()
    opts.intra_op_num_threads = THREADS
    opts.inter_op_num_threads = 1
    return ort.InferenceSession(os.path.join(MODELS, name), opts, providers=["CPUExecutionProvider"])


detector = session("yolox_s.onnx")
embedder = session("dinov2_small_int8.onnx")
det_in = detector.get_inputs()[0].name
emb_in = embedder.get_inputs()[0].name
slots = threading.BoundedSemaphore(CONCURRENT)

# the grid and stride of each of YOLOX's 8400 outputs
_grids, _strides = [], []
for stride in (8, 16, 32):
    n = SIZE // stride
    ys, xs = np.meshgrid(np.arange(n), np.arange(n))
    _grids.append(np.stack((xs, ys), 2).reshape(-1, 2))
    _strides.append(np.full((n * n, 1), stride))
GRID, STRIDE = np.concatenate(_grids).astype(np.float32), np.concatenate(_strides).astype(np.float32)

MEAN = np.array([0.485, 0.456, 0.406], dtype=np.float32)
STD = np.array([0.229, 0.224, 0.225], dtype=np.float32)


def detect(img):
    """Cats and dogs in a PIL image: [(kind, score, (x0, y0, x1, y1) in pixels)]."""
    w, h = img.size
    r = min(SIZE / w, SIZE / h)
    nw, nh = max(1, int(w * r)), max(1, int(h * r))
    canvas = np.full((SIZE, SIZE, 3), 114, dtype=np.float32)
    canvas[:nh, :nw] = np.asarray(img.resize((nw, nh), Image.BILINEAR), dtype=np.float32)[:, :, ::-1]  # YOLOX wants BGR, 0 to 255
    out = detector.run(None, {det_in: canvas.transpose(2, 0, 1)[None]})[0][0]
    xy = (out[:, :2] + GRID) * STRIDE
    wh = np.exp(out[:, 2:4]) * STRIDE
    boxes = np.concatenate([xy - wh / 2, xy + wh / 2], 1) / r
    obj, cls = out[:, 4], out[:, 5:]
    found = []
    for cid, kind in CLASSES.items():
        score = obj * cls[:, cid]
        keep = np.where(score >= MIN_SCORE)[0]
        for i in nms(boxes[keep], score[keep]):
            b = boxes[keep][i]
            found.append((kind, float(score[keep][i]), (max(0.0, b[0]), max(0.0, b[1]), min(float(w), b[2]), min(float(h), b[3]))))
    found.sort(key=lambda f: -f[1])
    return found[:MAX_ANIMALS]


def nms(boxes, scores, thr=0.45):
    order, keep = scores.argsort()[::-1], []
    while len(order):
        i = order[0]
        keep.append(i)
        if len(order) == 1:
            break
        rest = order[1:]
        x0 = np.maximum(boxes[i, 0], boxes[rest, 0])
        y0 = np.maximum(boxes[i, 1], boxes[rest, 1])
        x1 = np.minimum(boxes[i, 2], boxes[rest, 2])
        y1 = np.minimum(boxes[i, 3], boxes[rest, 3])
        inter = np.maximum(0, x1 - x0) * np.maximum(0, y1 - y0)
        area = lambda b: (b[..., 2] - b[..., 0]) * (b[..., 3] - b[..., 1])
        iou = inter / (area(boxes[i]) + area(boxes[rest]) - inter + 1e-9)
        order = rest[iou <= thr]
    return keep


def fingerprint(crop):
    x = np.asarray(crop.convert("RGB").resize((224, 224), Image.BICUBIC), dtype=np.float32) / 255.0
    x = ((x - MEAN) / STD).transpose(2, 0, 1)[None].astype(np.float32)
    v = embedder.run(None, {emb_in: x})[0][0, 0]  # the first token of the last layer
    v = v / (np.linalg.norm(v) + 1e-9)
    return [round(float(a), 5) for a in v]


def analyze(data):
    img = ImageOps.exif_transpose(Image.open(io.BytesIO(data))).convert("RGB")
    w, h = img.size
    animals = []
    for kind, score, (x0, y0, x1, y1) in detect(img):
        if x1 - x0 < 8 or y1 - y0 < 8:
            continue
        animals.append({"kind": kind, "score": round(score, 3), "box": [round(float(x0) / w, 4), round(float(y0) / h, 4), round(float(x1 - x0) / w, 4), round(float(y1 - y0) / h, 4)],
                        "print": fingerprint(img.crop((int(x0), int(y0), int(x1), int(y1))))})
    return {"animals": animals}


class Handler(BaseHTTPRequestHandler):
    server_version = "pet-sidecar"
    timeout = TIMEOUT

    def reply(self, code, body, ctype="application/json"):
        data = body if isinstance(body, bytes) else json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path == "/ping":
            self.reply(200, b"ok", "text/plain")
        else:
            self.reply(404, {"error": "not found"})

    def do_POST(self):
        if self.path != "/analyze":
            return self.reply(404, {"error": "not found"})
        try:
            n = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            n = 0
        if n <= 0 or n > MAX_BYTES:
            return self.reply(413 if n > MAX_BYTES else 400, {"error": "send one photo in the body, at most %d MB" % (MAX_BYTES // 1048576)})
        data = self.rfile.read(n)
        if not slots.acquire(timeout=TIMEOUT):
            return self.reply(503, {"error": "busy"})
        try:
            self.reply(200, analyze(data))
        except Exception as e:  # not a photo, or one that cannot be read (only the kind of error is logged, never anything of the photo)
            print("could not analyze a photo:", type(e).__name__, flush=True)
            self.reply(422, {"error": "not a photo that can be read"})
        finally:
            slots.release()

    def log_message(self, *a):  # nothing about the photos is logged
        pass


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", int(os.environ.get("PORT", "8090"))), Handler).serve_forever()
