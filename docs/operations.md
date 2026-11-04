# Operations

Running it day to day: where to put the page, backups, upgrades, monitoring and removing it.

## Putting the page behind your reverse proxy

The page signs people in with their **Immich login**: their browser sends Immich's cookie to the page, and the app asks Immich who it belongs to. Browsers send a cookie to every port of the same hostname, so the one rule is: **serve the page on the same hostname as Immich.**

Two ways that work:

**1. Same hostname, different port.** Immich at `https://photos.example.com` and the page at `https://photos.example.com:8443`, or on a home network `http://nas.local:2283` and `http://nas.local:8080`. Tested: signing in to Immich is enough, nothing else to set up.

**2. A path on the same address.** The page at `https://photos.example.com/handoff/` with Immich at `/`. Tested with nginx (below): styling, live counts, thumbnails, the live-update stream, the Sync button and invitation links all work under the prefix. Keep the trailing slash in the address you share.

A working nginx configuration, tested against Immich 3.2.0:

```nginx
server {
  listen 80;
  client_max_body_size 0;

  # the handoff page under a path prefix
  location /handoff/ {
    proxy_pass http://immich-handoff:8080/;     # note the trailing slash: it strips /handoff/
    proxy_set_header Host $host;
    proxy_buffering off;                        # lets the live-update stream through
    proxy_read_timeout 1h;
  }

  # everything else is Immich
  location / {
    proxy_pass http://immich-server:2283;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header Upgrade $http_upgrade;     # Immich uses websockets
    proxy_set_header Connection "upgrade";
  }
}
```

With a path prefix, set `PUBLIC_IMMICH_URL` to the same address people use (for example `https://photos.example.com`) so thumbnails load from Immich.

**Synology DSM.** DSM's built-in reverse proxy routes by hostname and port rather than by URL path, which fits option 1 (a second external port on the same hostname). I have not tested DSM itself. For option 2 you would run a small proxy container such as the nginx above in front of both.

**HTTPS.** Put the page behind HTTPS if it is reachable from outside your home network, the same as Immich.

## Backups

Only installs that let people join keep data: the `handoff-data` volume (its name starts with the folder name; see `docker volume ls`). Back it up while the app is stopped or idle, and keep a copy of `HANDOFF_STORE_KEY` separately, because the saved keys cannot be read without it:

```
docker compose stop immich-handoff
docker run --rm -v immich-handoff_handoff-data:/data -v "$PWD":/backup busybox tar czf /backup/handoff-data.tgz -C /data .
docker compose start immich-handoff
```

To restore, extract the archive into an empty volume the same way (`tar xzf`) before starting the app. Installs with only fixed accounts store nothing.

## Upgrading

Back up as above, then:

```
git pull
docker compose up -d --build
```

The data format upgrades itself when the app starts. Going back to an older version afterwards is not supported: an older version refuses to open a newer database. Read [the changelog](../CHANGELOG.md) first for anything that changes behaviour.

To use a prebuilt image instead of building, replace `build: .` in `docker-compose.yml` with `image: ghcr.io/vfuoglio/immich-handoff:<version>`; the versions that exist are listed on the repository's Packages page. Images are built for amd64 and arm64.

Before you upgrade **Immich** itself, you can run this project's whole test suite against the new Immich: `IMMICH_VERSION=<tag> test/run.sh` (see [Architecture](architecture.md#testing)).

## Monitoring

- **Log:** `docker compose logs`. Each pass logs what it found and did; errors are marked `ERROR`.
- **Notifications:** set `NOTIFY_URL` to an ntfy-style address and the app posts a message when a hand-over fails, a photo is refused, or a connection breaks.
- **Health and metrics:** `GET /healthz` answers `ok`. `GET /metrics` returns counters in Prometheus text format (passes, photos by result, services, version) and needs the `X-Webhook-Secret` header with your secret.

## Removing it

1. Ask people to press **Leave** (and owners **Delete service**) on the page. That removes the API keys and workflows the app made in their Immich accounts.
2. `docker compose down -v` stops the app and deletes its data volume.

Anything left behind is easy to find in Immich: API keys and workflows whose names start with `handoff` or `immich-handoff`.
