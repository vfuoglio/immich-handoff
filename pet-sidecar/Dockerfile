# The optional pet sidecar of Immich Handoff (docs/guides/automate.md#a-rule-for-a-pet). Python with ONNX Runtime and no PyTorch; the
# two models are fetched once, at build time, and checked against their checksums, so a running container never downloads anything.
FROM python:3.15-slim@sha256:65a65fc7b30b06cb6df4930fd4650697cb23f4c2698ab81fc87e4b031bc36928 AS models
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /models
RUN curl -fsSL -o yolox_s.onnx https://github.com/Megvii-BaseDetection/YOLOX/releases/download/0.1.1rc0/yolox_s.onnx \
 && curl -fsSL -o dinov2_small_int8.onnx https://huggingface.co/Xenova/dinov2-small/resolve/main/onnx/model_quantized.onnx \
 && echo "c5c2d13e59ae883e6af3b45daea64af4833a4951c92d116ec270d9ddbe998063  yolox_s.onnx" | sha256sum -c - \
 && echo "3afdc8bc63b50558d6e5770f5b799bb82455c2311183a2de43803f343a29d917  dinov2_small_int8.onnx" | sha256sum -c -

FROM python:3.15-slim@sha256:65a65fc7b30b06cb6df4930fd4650697cb23f4c2698ab81fc87e4b031bc36928
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt && rm -rf /root/.cache
COPY --from=models /models /models
COPY server.py LICENSES.md /app/
RUN useradd --system --uid 10001 --no-create-home pets
USER 10001
ENV PORT=8090
EXPOSE 8090
HEALTHCHECK --interval=30s --timeout=6s --start-period=30s --retries=3 CMD ["python", "-c", "import os,urllib.request; urllib.request.urlopen('http://127.0.0.1:'+os.environ.get('PORT','8090')+'/ping', timeout=4)"]
CMD ["python", "/app/server.py"]
