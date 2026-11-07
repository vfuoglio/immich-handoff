# Operations

Running it day to day: where to put the page, backups, upgrades, monitoring and removing it.

## Putting the page behind your reverse proxy

The page signs people in with their **Immich login**: their browser sends Immich's cookie to the page, and the app asks Immich who it belongs to. Browsers send a cookie to every port of the same hostname, so the one rule is: **serve the page on the same hostname as Immich.**

Two ways that work:

**1. Same hostname, different port.** Immich at `https://photos.example.com` and the page at `https://photos.example.com:8443`, or on a home network `http://server.local:2283` and `http://server.local:8080`. Tested: signing in to Immich is enough, nothing else to set up.

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

**Which one fits your proxy.** A proxy that routes only by hostname and port (many web interfaces do) fits option 1: give the page a second external port on the same hostname. Option 2 needs a proxy that can route by URL path, such as the nginx above.

**HTTPS.** Put the page behind HTTPS if it is reachable from outside your home network, the same as Immich.

## Backups

Only installs that let people join keep data: one file, `handoff.db`, in the `handoff-data` volume (its name starts with the folder name; see `docker volume ls`). It holds the services, the memberships and the saved API keys. Fixed-account installs store nothing.

Ask the running app for a backup:

```
docker compose exec immich-handoff /immich-handoff backup
```

The command **queues** the backup and returns at once. The app makes it at the next quiet moment between hand-overs, so a copy never catches a photo halfway through, and writes it to `/data/backups/handoff-<date>-<time>.db`. The app's log says `backup ready` when it is done, and the file also shows up in the recent events on the [administrator's page](admin.md). Asking again while one is waiting does not queue another.

If hand-overs never pause for **4 hours** (a very busy install, or a transfer that is stuck), the backup is made anyway so you are never left without one. That file is a consistent copy but may hold one hand-over caught halfway, so its name ends in `-forced` (`handoff-20261106-120000-forced.db`), and you are told: a warning in the log, a line in the recent events, and a message to `NOTIFY_URL` if you set it.

Useful variations:

- `backup --wait` stays, shows the progress, and exits when the backup is made, so a script can wait for it and read the result. Exit status **0**: made; **2**: made but forced (the file exists, see above); **1**: failed; **130**: interrupted with Ctrl-C (the backup is still queued in the app and will be made).
- `backup --list` shows the backups in the folder, and the one in progress.
- If the app is **not running**, the command copies at once and says so.

Take the file out of the volume (this works without a shell in the image), and keep it, together with a separate copy of `HANDOFF_STORE_KEY`, somewhere other than this machine:

```
docker compose cp immich-handoff:/data/backups/handoff-20261106-120000.db .
```

The file holds the saved API keys, still encrypted with `HANDOFF_STORE_KEY`, plus names and emails in the clear, so it is no more sensitive than the volume itself. Old backups are not deleted by the app; remove the ones you do not need from `/data/backups`. It does not contain your `.env`, your routes file or the store key: keep those yourself.

### Restoring

Stop the app first. A restore refuses to run while the app is running, because replacing the file under it would corrupt it.

```
docker compose stop immich-handoff
docker compose run --rm -v "$PWD/handoff-20261106-120000.db:/restore.db:ro" immich-handoff restore /restore.db
docker compose start immich-handoff
```

The restore checks the file before it touches anything: it must pass SQLite's own integrity check, open with the current `HANDOFF_STORE_KEY` (the wrong key is reported as such), and not come from a newer version of the app. If the data folder already has data it refuses unless you add `--force`, which keeps the old file next to it as `handoff.db.before-restore`.

### Moving to another machine

1. On the old machine, make a backup as above and copy it out, and note `HANDOFF_STORE_KEY`.
2. On the new machine, set up the app as in [Install](install.md) with the **same** `HANDOFF_STORE_KEY`. Do not start it yet.
3. Restore the file as above.
4. Set `PUBLIC_WEBHOOK_URL` for the new machine and start the app.

The people's API keys live in Immich and stay valid, so nobody has to join again. At startup the app repairs each sender's workflow so it points at the new address. If the Immich server itself changed, the keys no longer work: the app notices, pauses those connections and tells people (see [Troubleshooting](troubleshooting.md)).

An offline alternative, if you prefer archiving the volume: stop the app and `tar` the volume's contents (`docker run --rm -v immich-handoff_handoff-data:/data -v "$PWD":/backup busybox tar czf /backup/handoff-data.tgz -C /data .`), and extract it into an empty volume to restore.

## Upgrading

Make a backup ([how](#backups)), read the changelog for the release you are moving to, then switch to it. A copy cloned at a release is not on `main`, so `git pull` does nothing; check out the new release instead (`v1.2.0` here is an example, see the [releases page](https://github.com/vfuoglio/immich-handoff/releases) for the real one):

```
git fetch --tags
git checkout v1.2.0
docker compose pull
docker compose up -d
```

Stay on releases rather than `main`: `main` can be unstable and can describe features the published image does not have yet.

The data format upgrades itself when the app starts. Going back to an older version afterwards is not supported: an older version refuses to open a newer database. Read [the changelog](../CHANGELOG.md) first for anything that changes behaviour.

The image comes from Docker Hub: `docker-compose.yml` names `vfuoglio/immich-handoff:1.1`, which follows the 1.1.x fixes. The same tags are also published to GitHub's registry, as `ghcr.io/vfuoglio/immich-handoff`, if you prefer that one or Docker Hub's pull limits get in your way. Checking out a new release brings a compose file that names the matching image tag; the versions that exist are listed on the repository's Packages page and under Releases, each with its changelog. Images are built for amd64 and arm64. To build from the source instead, add the build file: `docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build`.

**Building elsewhere and moving the image.** The build can be slow on a small device, so you can build on a faster machine and copy the result: `docker buildx build --platform linux/amd64 -t immich-handoff:<tag> --load .` (use `linux/arm64` for an ARM machine; `uname -m` on the machine that will run it shows which), then `docker save immich-handoff:<tag> | gzip > immich-handoff.tar.gz`, copy the file to the machine that will run it and run `gunzip -c immich-handoff.tar.gz | docker load` there. Use a new tag each time so you can roll back.

**An untagged image of a gigabyte or more after building.** The `Dockerfile` has two stages: one with the Go toolchain and the source, and the 15 MB image you run. Some Docker setups (the classic builder, `DOCKER_BUILDKIT=0`) keep the first stage as an image shown as `<none>:<none>`. It is only the leftover of the build and nothing uses it. Remove it with `docker image prune`, which deletes every untagged image on that machine, so check `docker images -f dangling=true` first. `docker builder prune` also clears the build cache.

Before you upgrade **Immich** itself, you can run this project's whole test suite against the new Immich: `IMMICH_VERSION=<tag> test/run.sh` (see [Architecture](architecture.md#testing)).

## Monitoring

- **Log:** `docker compose logs`. Each pass logs what it found and did; errors are marked `ERROR`.
- **Notifications:** set `NOTIFY_URL` to an ntfy-style address and the app posts a message when a hand-over fails, a photo is refused, or a connection breaks.
- **Container health:** the image checks itself every 30 seconds, so `docker ps` and `docker compose ps` show `healthy` or `unhealthy`, and tools that read Docker's health status can show or alert on it. The check asks the app's own `/healthz` (on `LISTEN_ADDR`) and passes only on `ok`. The image has no shell or `curl`, so the check is built into the program: `docker compose exec immich-handoff /immich-handoff --healthcheck` runs it by hand and exits 0 when healthy. A service that should wait for it can use `depends_on: immich-handoff: condition: service_healthy`. It does not test the connection to Immich; the page and the log show that.
- **Health and metrics:** `GET /healthz` answers `ok`. `GET /metrics` returns counters in Prometheus text format (passes, photos by result, services, version) and needs the `X-Webhook-Secret` header with your secret.

## Checking the setup

`docker compose exec immich-handoff /immich-handoff doctor` tests the settings, the connection to Immich, every stored key and its permissions, and each sender's workflow, and says how to fix what fails. It is read-only, takes a few seconds, and exits 1 when anything failed, so it is safe to run on a live install and to use from a script or a scheduled job. What it checks, and how to read it, is in [Troubleshooting](troubleshooting.md#check-your-setup-first).

## Removing it

1. Ask people to press **Leave** (and owners **Delete service**) on the page. That removes the API keys and workflows the app made in their Immich accounts.
2. `docker compose down -v` stops the app and deletes its data volume.

Anything left behind is easy to find in Immich: API keys and workflows whose names start with `handoff` or `immich-handoff`.
