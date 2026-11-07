# Changelog

## Unreleased
- **A setup check.** `docker compose exec immich-handoff /immich-handoff doctor` tests the setup the app runs with and says how to fix what fails: the secrets, `PUBLIC_WEBHOOK_URL` (set, a real address, not `localhost`), that Immich is reachable and new enough, that each stored key still works and has the permissions it needs (fewer for a sender who keeps originals), that Tags are on, that each sender's workflow exists and matches the settings, and how Immich's last workflow runs ended, which shows whether Immich really reaches the app. Keys made from now on also get the read-only permission `workflow.logs` for that last part; older keys work, and the check says how to add it. It only reads and changes nothing; it exits 1 when something failed (warnings do not). See [Troubleshooting](docs/troubleshooting.md#check-your-setup-first).
- **Release hygiene.** Every release image is now **signed** (cosign, keyless, tied to the release workflow) and carries a **software bill of materials** and build provenance, and the workflow checks both before it finishes; the release guide shows how to verify an image. The GitHub Actions are brought up to current versions (the old ones were on a deprecated runtime) and **pinned by commit hash**. Dependabot proposes weekly updates for the actions, the Go modules and the base images, into `develop`.
- Fix: **a large video no longer fails because moving it took more than two minutes.** The whole transfer of one file used to share a two-minute limit, so a big video on a slow connection was cut off and retried the same way forever. A transfer now has no total limit and is given up only after three minutes without any data moving.
- Release: a fix for an older version (1.1.1 after 1.2.0) no longer takes the `latest` image tag; `latest` goes only to the highest final version released so far. The `X.Y` tag still moves, so people on `:1.1` get the fix. How to patch an older version is in the release guide.
- **Hand over a whole album.** A sender can link one of their own albums to a service they joined from the page (**Hand over a whole album** on the service's card). Everything in it, now and added later, is handed over once, with no tag, and the key gets `album.read` through the person's own login. Immich has no album event, so albums are checked on a timer (`ALBUM_CHECK_MINUTES`, default 15). The "waiting" counter now counts down photo by photo, and a finished hand-over from an album adds a line to Recent events (a big one also notifies `NOTIFY_URL`). Adds two columns to the data file.
- **Arrivals can go into an album.** A service can name an album (in **More options** when creating it on the page, `album:` in a routes file, `ALBUM` for a plain install). Handed-over photos are put in that album of the receiver's account, made when the first photo arrives, instead of being tagged inbox. If the album cannot be used, the photo is tagged inbox as before and the problem is reported once. The key of a service with an album also gets `album.read`, `album.create` and `albumAsset.create`. This adds a column to the data file, so going back to an older version afterwards is not supported (as with any data change).
- **The page notices when Tags are off** in a person's Immich account (Immich has them off by default) and offers a **Turn on Tags** button, which changes that one setting with their own session. They still need to refresh Immich afterwards. The documentation says so in the places it matters.
- **Container health check.** The image now reports `healthy` or `unhealthy` in `docker ps`, using a built-in `immich-handoff --healthcheck` (the image has no shell or curl). The release workflow checks that both architectures declare it.
- **The image is also published on Docker Hub** as `vfuoglio/immich-handoff`, so `image: vfuoglio/immich-handoff:1.1` in a compose file is enough to pull it. The same tags stay available on `ghcr.io/vfuoglio/immich-handoff`.

## 1.1.0
- **Prebuilt images:** each release publishes a multi-architecture image (amd64, arm64) to `ghcr.io/vfuoglio/immich-handoff` and a GitHub Release. The release workflow runs the full test suite first and smoke-tests the image on both architectures. `immich-handoff --version` prints the version. See [Releasing](docs/releasing.md).
- **Tags must be switched on in Immich by every person who takes part, and Immich must be refreshed afterwards.** The documentation now says so everywhere it matters.
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
