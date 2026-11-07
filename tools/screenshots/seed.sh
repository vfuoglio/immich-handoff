#!/usr/bin/env bash
# Builds the demo used for the documentation images, against the test Immich from `docker compose -f test/docker-compose.yml up -d`
# (Immich on :2284, users alice, bob, family @test.local, password Passw0rd!x) and a locally built app on :8080.
#
#   tools/screenshots/seed.sh reset      throw away and rebuild the Immich stack, users and API keys
#   tools/screenshots/seed.sh setup      start the app, create people and services, upload sample photos
#   tools/screenshots/seed.sh waiting    tag photos so some are waiting (run the app in dry-run mode first: DRY=1 ... setup)
#   tools/screenshots/seed.sh handover   restart the app for real so the waiting photos are handed over
#   tools/screenshots/seed.sh lost       Carol deletes her key; restart so the app notices
set -euo pipefail
cd "$(dirname "$0")/../.."
IMMICH_PORT="${IMMICH_PORT:-2294}"      # a throwaway Immich of its own, so a demo on :2284 is left alone
APP_PORT="${APP_PORT:-8090}"
STACK="${STACK:-hv-shots}"
DEMO="${DEMO:-$HOME/.local/share/immich-handoff-shots}"
B="http://localhost:$IMMICH_PORT/api"
APP="http://localhost:$APP_PORT"
PW='Passw0rd!x'
J='Content-Type: application/json'
field() { python3 -c "import sys,json;print(json.load(sys.stdin)['$1'])"; }
login_token() { curl -s -XPOST "$B/auth/login" -H "$J" -d "{\"email\":\"$1@test.local\",\"password\":\"$PW\"}" | field accessToken; }
cookie_of() { curl -s -i -XPOST "$B/auth/login" -H "$J" -d "{\"email\":\"$1@test.local\",\"password\":\"$PW\"}" | grep -i '^set-cookie' | sed -E 's/^[^:]+: ([^;]+);.*/\1/' | paste -sd';' | sed 's/;/; /g'; }
app_post() { # user path [curl data args...]
  local who=$1 path=$2; shift 2
  curl -s -o /dev/null -w '%{http_code}' -XPOST "$APP$path" -H "Cookie: $(cookie_of "$who")" -H 'X-Requested-With: immich-handoff' "$@"
}
upload() { # user file
  local T; T=$(login_token "$1"); local now; now=$(date -u +%FT%TZ)
  curl -s -XPOST "$B/assets" -H "Authorization: Bearer $T" -F fileCreatedAt="$now" -F fileModifiedAt="$now" -F assetData=@"$2" | field id
}
processed() { # user id: wait until Immich has read the photo and made its thumbnail
  local T; T=$(login_token "$1")
  for _ in $(seq 1 120); do
    curl -s "$B/assets/$2" -H "Authorization: Bearer $T" | python3 -c "import sys,json;d=json.load(sys.stdin);sys.exit(0 if d.get('hasMetadata') and d.get('exifInfo') and d.get('thumbhash') else 1)" && return 0
    sleep 1
  done
  echo "Immich did not finish $2" >&2; return 1
}
has_tag() { # user tag id
  curl -s "$B/assets/$3" -H "Authorization: Bearer $(login_token "$1")" | python3 -c "import sys,json;d=json.load(sys.stdin);sys.exit(0 if '$2' in [t['name'] for t in d.get('tags') or []] else 1)"
}
tag_assets() { # user tag id...
  local who=$1 tag=$2; shift 2
  local T; T=$(login_token "$who")
  local tid; tid=$(curl -s -XPUT "$B/tags" -H "Authorization: Bearer $T" -H "$J" -d "{\"tags\":[\"$tag\"]}" | python3 -c "import sys,json;print(json.load(sys.stdin)[0]['id'])")
  local ids; ids=$(printf '"%s",' "$@")
  curl -s -XPUT "$B/tags/$tid/assets" -H "Authorization: Bearer $T" -H "$J" -d "{\"ids\":[${ids%,}]}" >/dev/null
  # Immich occasionally drops a tag applied right after an upload: check, and add again where it did not stick
  for _ in 1 2 3 4 5; do
    sleep 2; local missing=()
    for id in "$@"; do has_tag "$who" "$tag" "$id" || missing+=("$id"); done
    [ ${#missing[@]} -eq 0 ] && return 0
    ids=$(printf '"%s",' "${missing[@]}")
    curl -s -XPUT "$B/tags/$tid/assets" -H "Authorization: Bearer $T" -H "$J" -d "{\"ids\":[${ids%,}]}" >/dev/null
  done
}
start_app() { # DRY=1 for dry-run
  pkill -f "^$DEMO/immich-handoff" 2>/dev/null || true; sleep 1
  ( . "$DEMO/env.sh"; export DRY_RUN="${DRY:-false}"; nohup setsid "$DEMO/immich-handoff" >> "$DEMO/app.log" 2>&1 < /dev/null & )
  for _ in $(seq 1 30); do curl -sf "$APP/healthz" >/dev/null && return; sleep 1; done
  echo "app did not start" >&2; exit 1
}
wait_log() { for _ in $(seq 1 90); do grep -q "$1" "$DEMO/app.log" && return 0; sleep 2; done; echo "timed out waiting for: $1" >&2; return 1; }

case "${1:-}" in
reset)
  pkill -f "^$DEMO/immich-handoff" 2>/dev/null || true
  export E2E_PORT=$IMMICH_PORT
  docker compose -p "$STACK" -f test/docker-compose.yml down -v >/dev/null 2>&1 || true
  docker compose -p "$STACK" -f test/docker-compose.yml up -d --wait --wait-timeout 300 >/dev/null
  curl -s -XPOST "$B/auth/admin-sign-up" -H "$J" -d "{\"email\":\"admin@test.local\",\"name\":\"Admin\",\"password\":\"$PW\"}" >/dev/null
  AT=$(login_token admin)
  for u in alice bob family; do
    curl -s -XPOST "$B/admin/users" -H "Authorization: Bearer $AT" -H "$J" -d "{\"email\":\"$u@test.local\",\"name\":\"${u^}\",\"password\":\"$PW\",\"shouldChangePassword\":false}" >/dev/null
    curl -s -XPUT "$B/users/me/preferences" -H "Authorization: Bearer $(login_token $u)" -H "$J" -d '{"tags":{"enabled":true}}' >/dev/null
  done
  SRC='["user.read","tag.read","tag.create","tag.asset","asset.read","asset.download","asset.delete","workflow.read","workflow.create","workflow.update"]'
  DST='["user.read","tag.read","tag.create","tag.asset","asset.read","asset.upload"]'
  key() { curl -s -XPOST "$B/api-keys" -H "Authorization: Bearer $(login_token "$1")" -H "$J" -d "{\"name\":\"demo\",\"permissions\":$2}" | field secret; }
  mkdir -p "$DEMO/data"; rm -rf "$DEMO/data"/* "$DEMO/app.log"
  cat > "$DEMO/env.sh" <<ENV
export IMMICH_BASE_URL=http://localhost:$IMMICH_PORT
export ALICE_KEY=$(key alice "$SRC")
export FAMILY_KEY=$(key family "$DST")
export ROUTES_FILE=$DEMO/routes.yaml
export WEBHOOK_SECRET=demo-webhook-secret
export PUBLIC_WEBHOOK_URL=http://host.docker.internal:$APP_PORT/webhook/immich
export PUBLIC_IMMICH_URL=http://localhost:$IMMICH_PORT
export HANDOFF_STORE_KEY=demo-store-key-0123456789abcdef
export DATA_DIR=$DEMO/data
export LISTEN_ADDR=:$APP_PORT
ENV
  echo "stack $STACK ready: Immich http://localhost:$IMMICH_PORT, app will run on $APP"
  ;;
setup)
  mkdir -p "$DEMO"; go build -o "$DEMO/immich-handoff" ./cmd/immich-handoff
  rm -f "$DEMO/data/handoff.db"*
  ( cd tools/screenshots && go run . gen "$DEMO/photos" 24 )
  AT=$(login_token admin)
  for u in carol dave; do curl -s -XPOST "$B/admin/users" -H "Authorization: Bearer $AT" -H "$J" -d "{\"email\":\"$u@test.local\",\"name\":\"${u^}\",\"password\":\"$PW\",\"shouldChangePassword\":false}" >/dev/null || true
    curl -s -XPUT "$B/users/me/preferences" -H "Authorization: Bearer $(login_token $u)" -H "$J" -d '{"tags":{"enabled":true}}' >/dev/null; done
  # routes: one fixed service named Family; everything else is made on the page
  cat > "$DEMO/routes.yaml" <<YAML
accounts:
  alice:  { key: \${ALICE_KEY} }
  family: { key: \${FAMILY_KEY} }
services:
  - name: Family
    tag: share
    senders: [alice]
    receivers: [family]
    allow_join: true
YAML
  start_app
  wait_log '"workflow ready"'
  # Bob offers two services; Alice and Carol ask to join Holiday 2026; Bob approves Carol, who activates
  app_post bob /services -d 'name=Holiday 2026' -d 'tag=holiday' -d 'policy=approval' -d 'visibility=listed'; echo
  app_post bob /services -d 'name=Photo club' -d 'tag=club' -d 'policy=open' -d 'visibility=invite'; echo
  app_post alice /ask -d 'service=Holiday 2026'; echo
  app_post carol /ask -d 'service=Holiday 2026'; echo
  # find Carol's request on Bob's page (Alice's stays pending)
  CRID=$(curl -s "$APP/" -H "Cookie: $(cookie_of bob)" | python3 -c "
import re,sys,html
t=html.unescape(sys.stdin.read())
blocks=re.split(r'<article', t)
for b in blocks:
    if 'carol@test.local' in b:
        m=re.search(r'\"request\":\"([0-9a-f-]{36})\"', b)
        if m: print(m.group(1)); break")
  app_post bob /decide -d 'service=Holiday 2026' -d "request=$CRID" -d 'decision=approve'; echo
  app_post carol /join -d 'service=Holiday 2026' -d 'mode=copy'; echo
  # sample photos: Alice has some, Carol has a few
  i=0; for f in "$DEMO"/photos/IMG_*.jpg; do i=$((i+1)); echo "$f"; done > "$DEMO/photo-list"
  echo "setup done; photos in $DEMO/photos"
  ;;
waiting)
  mapfile -t P < "$DEMO/photo-list"
  A=(); for k in 0 1 2 3 4 5 6 7; do A+=("$(upload alice "${P[$k]}")"); done
  C=(); for k in 8 9 10; do C+=("$(upload carol "${P[$k]}")"); done
  for id in "${A[@]}"; do processed alice "$id"; done
  for id in "${C[@]}"; do processed carol "$id"; done
  tag_assets alice share "${A[0]}" "${A[1]}" "${A[2]}" "${A[3]}" "${A[4]}"
  tag_assets carol holiday "${C[0]}" "${C[1]}" "${C[2]}"
  sleep 3
  echo "tagged 5 of Alice's and 3 of Carol's photos"
  ;;
handover)
  start_app
  for _ in $(seq 1 40); do
    n=$(grep -c '"msg":"imported"\|"msg":"duplicate"' "$DEMO/app.log" || true); [ "$n" -ge 8 ] && break; sleep 3
  done
  echo "hand-overs logged: $n"
  ;;
lost)
  T=$(login_token carol)
  KID=$(curl -s "$B/api-keys" -H "Authorization: Bearer $T" | python3 -c "import sys,json;print(next(k['id'] for k in json.load(sys.stdin) if k['name']=='handoff: Holiday 2026'))")
  curl -s -XDELETE "$B/api-keys/$KID" -H "Authorization: Bearer $T" -o /dev/null -w 'key deleted: HTTP %{http_code}\n'
  start_app; sleep 6
  ;;
*) echo "usage: $0 reset|setup|waiting|handover|lost" >&2; exit 2 ;;
esac
