#!/usr/bin/env bash
# Starts the demo from nothing (a fresh Immich, the three people, the photos) and waits until it has been filled.
set -euo pipefail
cd "$(dirname "$0")/../../demo"
docker compose down -v >/dev/null 2>&1 || true
docker compose up -d --build >/dev/null 2>&1
for _ in $(seq 1 120); do
  docker logs handoff-demo-seed-1 2>&1 | grep -q "bob tagged" && { sleep 5; echo "the demo is ready on ${DEMO_URL:-http://localhost:2295}"; exit 0; }
  docker logs handoff-demo-seed-1 2>&1 | grep -q Traceback && { echo "seeding failed" >&2; exit 1; }
  sleep 5
done
echo "the demo did not become ready" >&2; exit 1
