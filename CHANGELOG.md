# Changelog

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
