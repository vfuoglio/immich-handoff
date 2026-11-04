#!/usr/bin/env bash
# Runs the app against the test Immich started by `keep=1 test/run.sh` (or `docker compose -f test/docker-compose.yml up -d --wait`).
# Creates the users if needed, makes fresh API keys, and starts the app in the foreground (Ctrl-C to stop).
set -euo pipefail
cd "$(dirname "$0")/.."
B=${IMMICH_E2E_URL:-http://localhost:2284}/api
PW='Passw0rd!x'
J='Content-Type: application/json'
field() { python3 -c "import sys,json;print(json.load(sys.stdin)['$1'])"; }
login() { curl -sf -XPOST "$B/auth/login" -H "$J" -d "{\"email\":\"$1@e2e.test\",\"password\":\"$PW\"}" | field accessToken; }

curl -s -XPOST "$B/auth/admin-sign-up" -H "$J" -d "{\"email\":\"admin@e2e.test\",\"name\":\"admin\",\"password\":\"$PW\"}" >/dev/null || true
AT=$(login admin)
for u in alice bob family; do
  curl -s -XPOST "$B/admin/users" -H "Authorization: Bearer $AT" -H "$J" \
    -d "{\"email\":\"$u@e2e.test\",\"name\":\"$u\",\"password\":\"$PW\",\"shouldChangePassword\":false}" >/dev/null || true
done
key() { curl -sf -XPOST "$B/api-keys" -H "Authorization: Bearer $(login "$1")" -H "$J" \
  -d "{\"name\":\"dev-$(date +%s)\",\"permissions\":[\"all\"]}" | field secret; }

export IMMICH_BASE_URL=${IMMICH_E2E_URL:-http://localhost:2284}
export IMMICH_SOURCE_API_KEYS="$(key alice),$(key bob)"
export IMMICH_DEST_API_KEY="$(key family)"
export WEBHOOK_SECRET=dev-secret
export PUBLIC_WEBHOOK_URL=http://host.docker.internal:18080/webhook/immich   # Immich container -> app on this host
export LISTEN_ADDR=:18080
export LOG_LEVEL=${LOG_LEVEL:-info}
cat <<MSG
Immich UI:   ${IMMICH_BASE_URL}     logins: alice@e2e.test, bob@e2e.test, family@e2e.test   password: $PW
Try it:      log in as alice, select a photo, Add tag "share"; watch this log, then look at family's library.
MSG
exec go run ./cmd/immich-handoff
