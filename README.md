# immich-handoff

Moves photos from personal Immich accounts into a shared family account, triggered by a tag, for **Immich ≥ 3.2**.

A family member tags a photo `share` in the Immich app. An Immich **workflow** calls this app's webhook, and the app moves the photo to the shared family account and tags the copy `inbox`. The original is removed only after the copy is verified. No polling for new photos.

> **Status: end-to-end tested against Immich v3.2.0 in Docker (`test/run.sh`); not yet run on a production server.** See [Requirements and compatibility](#requirements-and-compatibility) and [Troubleshooting](#troubleshooting).

## How it works

```
phone: tag "share" ──► Immich workflow ──► POST /webhook/immich ──► import tagged photos
  (trigger: asset tagged, filter: tag=share, action: Trigger Webhook + secret header)
```

- **Webhook = wake-up signal.** The payload is not parsed or trusted. Each pass searches Immich by tag, so a lost, duplicated or malformed event cannot lose or double-import a photo. Bursts are coalesced into one extra pass.
- **Safety net, not polling.** One pass at startup and one sweep per day (`SWEEP_INTERVAL_HOURS`) catch anything missed while the app was down. The sweep also removes the `inbox` tag from photos filed in an album, or unfiled for `INBOX_EXPIRY_DAYS`. Photos are never deleted by cleanup.
- **Streaming import.** The original is streamed from the source account into the destination upload (`io.Pipe` + `TeeReader`): no temp files, no size limit. The SHA-1 is computed in flight and compared with the checksum Immich stored for the new copy before the original is touched.
- **Import rules** (same as the script): checksum-verified copy, duplicates not tagged, a copy sitting in the destination trash refuses the import: the original is kept and its `share` tag is swapped for `share-duplicate` (`REFUSED_TAG`), so the owner can see why in the Immich app and it is neither retried nor re-reported on every pass (re-add `share` after emptying the trash to retry), originals go to the source trash unless `DELETE_FORCE=true`.
- **Stateless.** Config from environment; last-run info is in memory only.

## Status page (auth option A: Immich's own login)

Serve the app on the **same host as Immich** through the Synology reverse proxy (for example `/handoff/`). The browser then sends Immich's session cookie; the app forwards it to `GET /api/users/me` and accepts only the family accounts.

- Source user: sees how many of their photos are waiting. Destination user: sees the `inbox` count.
- Both see the last run. "Sync now" is guarded with an `X-Requested-With` header (CSRF).
- The API keys stay in the server environment and never reach the browser.

## Tech

- **Go**, one static binary in a distroless image. Goroutines + a try-lock coalesce webhook bursts; `slog` JSON logs.
- **Typed client generated from Immich's OpenAPI spec** (`oapi-codegen`, spec pinned in `api/`, only the operations we use). If Immich changes one of them, the build breaks instead of the sync silently doing nothing, which is what happened to the old script. CI fails if the generated code is stale.
- Tests run the full import against a fake Immich (`go test ./...`): streaming, duplicate, trashed copy, checksum mismatch.
- To move to a newer Immich: replace the spec in `api/`, update the path in `internal/immich/generate.go`, run `go generate ./...`, fix what breaks.

## Testing

- `go test ./...`: unit tests against a fake Immich (fast, no Docker).
- `test/run.sh`: starts a throwaway **Immich v3.2.0** (`test/docker-compose.yml`, port 2284, nothing persisted), builds the real binary, and runs `test/e2e` against it, then tears everything down (`keep=1 test/run.sh` leaves Immich running). It creates three users and checks: workflow creation, a tagged photo arriving through the real workflow webhook, duplicates, a trashed destination copy refusing the import, and inbox cleanup after filing in an album. `IMMICH_VERSION=...` tests another release.

Things the real-server tests found, now handled in the code:

- **Immich is healthy ~30 s before its workflow plugins load.** Creating the workflow right after an Immich start fails with `Unknown method`, so provisioning retries in the background with backoff and is re-checked at every daily sweep.
- **The tag-added event can arrive before the tag is visible through the API** (neither the tag search nor a lookup by id shows it yet). The webhook payload's asset id is used as an *unverified hint*: the app re-checks it with the source account's own key and retries for about a minute before leaving it to the daily sweep.

## Setup

1. Copy `.env.example` to `.env` and fill it in (one API key per account with the permissions under [Requirements and compatibility](#requirements-and-compatibility); pick a long random `WEBHOOK_SECRET`).
2. `docker compose up -d --build` (or `go run ./cmd/immich-handoff` with the variables exported). Join Immich's compose network (see `docker-compose.yml`) so the app can reach `immich-server`.
3. Set `PUBLIC_WEBHOOK_URL` to the address Immich can reach the app at (for example `http://immich-handoff:8080/webhook/immich`). On startup the app creates the `share` tag and a workflow named `immich-handoff` in every source account (trigger *asset tagged*, filter *tag is share*, action *Trigger Webhook* with the secret header). It is idempotent and repairs the workflow if the URL or secret changes. Source API keys need the workflow and tag permissions listed under [Requirements and compatibility](#requirements-and-compatibility). Enable tags under Account Settings -> Features. Without `PUBLIC_WEBHOOK_URL` nothing is provisioned and only the startup/daily passes import.
4. Try `DRY_RUN=true` first. Tag one photo and watch `docker logs`.

## Requirements and compatibility

- **Immich 3.2.0 or later.** The `tag added` workflow trigger arrived in 3.2.0. On an older v3 server the app still runs (and warns), but photos only move at startup and in the daily sweep.
- **Tags enabled** for each account (Account Settings -> Features -> Tags), and Immich's storage writable, because Immich writes tag changes to sidecar files.
- **`PUBLIC_WEBHOOK_URL` reachable from the Immich server container** (not from your browser). With both in one compose network, use the service name, for example `http://immich-handoff:8080/webhook/immich`.
- **API key permissions**, per account:
  - source accounts: `asset.read`, `asset.download`, `asset.delete`, `tag.create`, `tag.read`, `tag.asset`, `workflow.create`, `workflow.read`, `workflow.update`
  - destination account: `asset.upload`, `asset.read`, `tag.create`, `tag.read`, `tag.asset`, `album.read`
- **Workflows are a preview feature in Immich**, so their interface can change between releases. The Immich client is generated from the pinned v3.2.0 spec (a changed operation fails the build), and `IMMICH_VERSION=<tag> test/run.sh` runs the whole end-to-end suite against any other release before you upgrade.

## Phone backup and the trashed original

By default the original is moved to the source account's trash, not deleted permanently. While it is there, Immich's duplicate check (`POST /assets/bulk-upload-check`, the endpoint clients use before uploading) reports it as an existing, trashed duplicate, so a phone's backup skips it. This is asserted by the end-to-end suite. Once Immich empties the trash (30 days by default) that protection ends, and a phone that still has the photo can back it up again; delete the photo from the phone, or let the family copy be the only one you keep.

## Troubleshooting

Tagging a photo should import it within a few seconds. If it does not:

1. **App log.** Each pass logs what it found and did (`LOG_LEVEL=debug` also logs webhook payloads). A line `photos were found by the daily sweep instead of by webhook` means the events are not arriving: check `PUBLIC_WEBHOOK_URL` and that the workflow is enabled.
2. **Workflow log in Immich.** Open the account's `immich-handoff` workflow; each tag event is one run. No runs means the trigger never fired (is the tag really `share`, and are tags enabled for that account?). A failed webhook step means Immich could not reach the URL.
3. **Refused imports.** A photo whose copy sits in the family account's trash is not imported. Its tag becomes `share-duplicate` (see `REFUSED_TAG`); empty the family trash and add `share` again to retry.
4. **Errors in the log mentioning `HTTP 401/403`** mean an API key lacks a permission from the list above.
5. **Status page says "Sign in to Immich first".** The page must be served on the same hostname as Immich (any port) so it receives Immich's login cookie.

## Background

immich-handoff is based on the idea of immich_family_sync.

## License

MIT, see `LICENSE`.
