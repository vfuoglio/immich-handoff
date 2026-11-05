# Changelog

## Unreleased
- **Photos filed in an album lose their inbox tag within minutes**, not up to a day later. Immich sends no event for albums, so a small timer does the check (`CLEANUP_INTERVAL_MINUTES`, default 15; `0` = daily only). The docs explain the limitation.
- Fix: the invitation page lost its styling and buttons behind a path prefix, and accepting an invitation ended on an error page.
- **The sender decides what happens to their originals**, when they join: keep them (the default) or move them to their trash. The owner of a service no longer sets this. The key made for a sender who keeps their originals has no delete permission. Existing members keep the behaviour they had.
- Fixed-account services (`.env`, routes file) now **default to copy** (`HANDOFF_MODE=copy`). Set `HANDOFF_MODE=move`, or `mode: move` on a service, to keep the old behaviour.
- Services created on the page get their own nested inbox tag (`inbox/<name>`) by default, so one owner with several services sees the arrivals separately.
- The page: the offer form is a collapsed section, the last run is described in plain words, a pending request no longer leaves an empty row, cards are tidier.
- Fix: a photo handed over a moment ago is no longer handed over again while Immich's search still lists it.
- `WEBHOOK_SECRET` must be at least 16 characters.
- `.env.example` is now hub-first: a fresh install needs no API keys.
- Documentation reorganised into `docs/` with screenshots; tooling to remake them in `tools/screenshots`.
- The release workflow can be started by hand from the Actions tab.

## 1.0.0
- Release workflow publishes a multi-architecture container image for each tag.
- Security headers (strict CSP, no framing), request size limit, per-person rate limit on changes, `/metrics`.
- Optional thumbnails of photos waiting to be handed over (`PUBLIC_IMMICH_URL`).
- Backup, upgrade and security documentation.

## 0.7.0
- Services either **move** or **copy** originals; copy-mode keys have no delete permission.
- Owners choose the inbox tag and expiry when they create a service.
- Tag changes on originals are verified and retried (Immich can drop a change made right after another).

## 0.6.0
- Directory visibility and invitation links; a new link revokes the old one.

## 0.5.0
- Link health: a deleted key (either side) pauses the connection, tells both sides, never touches a photo; clean up and reconnect. Activity log.

## 0.4.0
- Anyone can offer a service; senders ask, the owner approves, the sender activates. Deleting a service lets members clean up.

## 0.3.0
- Self-service join and leave. Encrypted SQLite store (AES-256-GCM) for API keys, created through the person's own Immich session.

## 0.2.0
- New web page (Go templates with templ, htmx, light and dark themes, live status).

## 0.1.0
- Services with several senders and receivers via a routes file; one-to-many.

## 0.0.1
- Tag a photo `share`; the photo is handed to one family account through an Immich workflow.
