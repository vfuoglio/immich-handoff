# What is inside the pet sidecar

| Part | Licence | Source |
|---|---|---|
| YOLOX-s detector (`yolox_s.onnx`, 36 MB) | Apache-2.0 | https://github.com/Megvii-BaseDetection/YOLOX |
| DINOv2-small, int8 (`dinov2_small_int8.onnx`, 24 MB, an ONNX export by Xenova of Meta's model) | Apache-2.0 | https://huggingface.co/Xenova/dinov2-small, https://github.com/facebookresearch/dinov2 |
| ONNX Runtime | MIT | https://github.com/microsoft/onnxruntime |
| NumPy | BSD-3-Clause | https://numpy.org |
| Pillow | MIT-CMU (HPND) | https://python-pillow.org |
| `server.py` | the licence of Immich Handoff | this repository |

The models are downloaded when the image is built and checked against the checksums in the `Dockerfile`; the running container downloads nothing and writes nothing.
