# immich-sync-console

Event-driven successor to [immich_family_sync](../immich_family_sync) for **Immich ≥ 3.2**.

A family member tags a photo `share` in the Immich app. An Immich **workflow** calls this app's webhook, and the app moves the photo to the shared family account and tags the copy `inbox`. The original is removed only after the copy is verified. No polling for new photos.

> **Status: scaffold, untested against a real server.** See [Open questions](#open-questions).

## How it works

```
phone: tag "share" ──► Immich workflow ──► POST /webhook/immich ──► import tagged photos
  (trigger: asset tagged, filter: tag=share, action: Trigger Webhook + secret header)
```

- **Webhook = wake-up signal.** The payload is not parsed or trusted. Each pass searches Immich by tag, so a lost, duplicated or malformed event cannot lose or double-import a photo. Bursts are coalesced into one extra pass.
- **Safety net, not polling.** One pass at startup and one sweep per day (`SWEEP_INTERVAL_HOURS`) catch anything missed while the app was down. The sweep also removes the `inbox` tag from photos filed in an album, or unfiled for `INBOX_EXPIRY_DAYS`. Photos are never deleted by cleanup.
- **Streaming import.** The original is streamed from the source account into the destination upload (`io.Pipe` + `TeeReader`): no temp files, no size limit. The SHA-1 is computed in flight and compared with the checksum Immich stored for the new copy before the original is touched.
- **Import rules** (same as the script): checksum-verified copy, duplicates not tagged, a copy sitting in the destination trash refuses the import and keeps the original, originals go to the source trash unless `DELETE_FORCE=true`.
- **Stateless.** Config from environment; last-run info is in memory only.

## Status page (auth option A: Immich's own login)

Serve the app on the **same host as Immich** through the Synology reverse proxy (for example `/family-sync/`). The browser then sends Immich's session cookie; the app forwards it to `GET /api/users/me` and accepts only the family accounts.

- Source user: sees how many of their photos are waiting. Destination user: sees the `inbox` count.
- Both see the last run. "Sync now" is guarded with an `X-Requested-With` header (CSRF).
- The API keys stay in the server environment and never reach the browser.

## Tech

- **Go**, one static binary in a distroless image. Goroutines + a try-lock coalesce webhook bursts; `slog` JSON logs.
- **Typed client generated from Immich's OpenAPI spec** (`oapi-codegen`, spec pinned in `api/`, only the operations we use). If Immich changes one of them, the build breaks instead of the sync silently doing nothing, which is what happened to the old script. CI fails if the generated code is stale.
- Tests run the full import against a fake Immich (`go test ./...`): streaming, duplicate, trashed copy, checksum mismatch.
- To move to a newer Immich: replace the spec in `api/`, update the path in `internal/immich/generate.go`, run `go generate ./...`, fix what breaks.

## Setup

1. Copy `.env.example` to `.env` and fill it in (API keys as in immich_family_sync; pick a long random `WEBHOOK_SECRET`).
2. `docker compose up -d --build` (or `go run ./cmd/sync-console` with the variables exported). Join Immich's compose network (see `docker-compose.yml`) so the app can reach `immich-server`.
3. Set `PUBLIC_WEBHOOK_URL` to the address Immich can reach the app at (for example `http://immich-sync-console:8080/webhook/immich`). On startup the app creates the `share` tag and a workflow named `immich-sync-console` in every source account (trigger *asset tagged*, filter *tag is share*, action *Trigger Webhook* with the secret header). It is idempotent and repairs the workflow if the URL or secret changes. Source API keys need `workflow.create`, `workflow.read`, `workflow.update`, `tag.create`, `tag.read` on top of the permissions in immich_family_sync. Enable tags under Account Settings -> Features. Without `PUBLIC_WEBHOOK_URL` nothing is provisioned and only the startup/daily passes import.
4. Try `DRY_RUN=true` first. Tag one photo and watch `docker logs`.

## Open questions

Verify on your server before relying on this:

- Workflow provisioning follows the v3.2.0 spec and source (trigger `AssetTagged`, methods `immich-plugin-core#assetTagFilter` and `#webhook`) but has only run against a fake server. Check the logs for `workflow ready`, and the workflow in the Immich UI.
- Webhook host restrictions: the plugin manifest allows any host, but check that the container is reachable from Immich.
- Whether the tag trigger fires for tags added from the mobile app.
- Name of Immich's session cookie and whether `/api/users/me` accepts it (the app forwards all cookies, so the name should not matter).
- Whether phones re-upload originals that were trashed on the server (inherited from the script).
- Uploads omit the `x-immich-checksum` hint (the hash is only known after streaming); confirm Immich still reports duplicates correctly without it.
- Search pagination follows `nextCursor`, falling back to `nextPage`; the cursor semantics are untested.
- Workflows are a preview feature in Immich; the interface may change.

## License

MIT, see `LICENSE`.
