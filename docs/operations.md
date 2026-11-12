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

To let people find the page from inside Immich, add a button to Immich's header with this same proxy: [Make Handoff easy to find](find-handoff.md).

With a path prefix, set `PUBLIC_IMMICH_URL` to the same address people use (for example `https://photos.example.com`) so thumbnails load from Immich.

**Which one fits your proxy.** A proxy that routes only by hostname and port (many web interfaces do) fits option 1: give the page a second external port on the same hostname. Option 2 needs a proxy that can route by URL path, such as the nginx above.

**HTTPS.** Put the page behind HTTPS if it is reachable from outside your home network, the same as Immich.

## Backups

A backup is the data file, which also holds the [messages to the host](using.md#message-to-the-host) of every drop box (messages and voice notes). Files that wait for approval in a [drop box](using.md#approving-files-before-they-are-kept) are kept beside it in `dropbox-pending` and are not in the backup (they are deleted after two weeks anyway).

Only installs that let people join keep data: one file, `handoff.db`, in the `handoff-data` volume (its name starts with the folder name; see `docker volume ls`). It holds the services, the memberships and the saved API keys. Fixed-account installs store nothing.

Ask the running app for a backup:

```
docker compose exec immich-handoff /immich-handoff backup
```

The command **queues** the backup and returns at once. The app makes it at the next quiet moment between hand-overs, so a copy never catches a photo halfway through, and writes it to `/data/backups/handoff-<date>-<time>.db` (or to `BACKUP_DIR`, see [Automatic backups](#automatic-backups)). The app's log says `backup ready` when it is done, and the file also shows up in the recent events on the [administrator's page](admin.md). Asking again while one is waiting does not queue another.

If hand-overs never pause for **4 hours** (a very busy install, or a transfer that is stuck), the backup is made anyway so you are never left without one. That file is a consistent copy but may hold one hand-over caught halfway, so its name ends in `-forced` (`handoff-20261106-120000-forced.db`), and you are told: a warning in the log, a line in the recent events, and a message to `NOTIFY_URL` if you set it.

Useful variations:

- `backup --wait` stays, shows the progress, and exits when the backup is made, so a script can wait for it and read the result. Exit status **0**: made; **2**: made but forced (the file exists, see above); **1**: failed; **130**: interrupted with Ctrl-C (the backup is still queued in the app and will be made).
- `backup --list` shows the backups in the folder, and the one in progress.
- If the app is **not running**, the command copies at once and says so.

### Automatic backups

The app also makes a backup **on its own**, so there is one even if nobody ever runs the command. It is on by default whenever the app keeps data (`HANDOFF_STORE_KEY` is set).

- **How often:** every 24 hours (`BACKUP_EVERY`, for example `12h`; one hour at the least). A backup is made about half a minute after the app starts if there is none yet or the newest is more than that old; after a restart it waits until the newest is `BACKUP_EVERY` old, so restarting often does not make more.
- **How many:** the newest 7 (`BACKUP_KEEP`). Older automatic backups are removed. Their names start with `handoff-auto-`. Backups you asked for with the command, and files downloaded from the page, are never removed by the app.
- **Quiet moment:** an automatic backup waits for a quiet moment between hand-overs, and is made anyway after 4 hours (named `-forced`), exactly like the one the command asks for.
- **When it fails** (a full disk, a folder that cannot be written), you are told like for any failed backup: a line in the log, a line in the recent events and a message to `NOTIFY_URL`. The app tries again an hour later, and the [administrator's page](admin.md#what-it-shows) says that the last backup failed and why. `doctor` also checks the settings.
- **Turn it off** with `BACKUP_EVERY=off`. The command keeps working.

**Where they go.** By default into `/data/backups`, inside the same volume as the data. That protects you from a mistake or a damaged file, but **not from losing the volume**. Better, mount a second folder (another disk, or a network share) and point `BACKUP_DIR` at it. The command, `backup --list` and the page use that folder too:

```yaml
services:
  immich-handoff:
    volumes:
      - /mnt/another-disk/handoff-backups:/backups
    environment:
      BACKUP_DIR: /backups
```

The folder must be writable by the app. To restore from one of these files, follow [Restoring](#restoring) with that file.

### Download a backup from the administrator's page

An Immich administrator can download a backup from the [administrator's page](admin.md), without a command line. **It is off by default**, because the file it hands out holds every stored API key (still encrypted with `HANDOFF_STORE_KEY`) and everyone's name and email, and a button makes that file reachable by anyone who gets hold of an administrator's session. Turn it on only if you want that convenience and you trust whoever is an administrator in Immich.

To switch it on, add this to `.env` and restart the app:

```
HANDOFF_ADMIN_BACKUP_DOWNLOAD=true
```

Only the exact value `true` works. Then the administrator's page shows a **Download backup** button in its **Backups** section (until then the page says that the download is off and links here). What limits it:

- It is not a link: the page sends a POST request with the header that a page from another site cannot send, and only a signed-in Immich administrator is answered. Everyone else, and everyone when the switch is off, gets "not found".
- Pressing the button first asks in a dialog, which says what the file holds.
- Only one download is handed out every **10 minutes** for the whole app, whoever asks.
- Every download is written to the recent events with the administrator's name and email, and sent to `NOTIFY_URL` if you set it, so a download you did not make shows.
- The copy is made at once (it does not wait for a quiet moment: it is consistent anyway) in a private folder, and removed as soon as it has been sent. The answer is marked `no-store`, so no cache keeps it.
- It exists only where the app keeps data, so it needs `HANDOFF_STORE_KEY`.
- It cannot restore: restoring needs the app stopped, so it stays a [command](#restoring).

Keep the downloaded file, together with a separate copy of `HANDOFF_STORE_KEY`, somewhere other than this machine. `doctor` says whether the download is on. See also [Security and trust](security.md#backups).

Take the file out of the volume (this works without a shell in the image), and keep it, together with a separate copy of `HANDOFF_STORE_KEY`, somewhere other than this machine:

```
docker compose cp immich-handoff:/data/backups/handoff-20261106-120000.db .
```

The file holds the saved API keys, still encrypted with `HANDOFF_STORE_KEY`, plus names and emails in the clear, so it is no more sensitive than the volume itself. The app removes only the old *automatic* backups ([above](#automatic-backups)); remove the others you do not need yourself. It does not contain your `.env`, your routes file or the store key: keep those yourself.

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

**Following `main` with the nightly image.** If you want the newest changes every day, name `vfuoglio/immich-handoff:nightly` (or `ghcr.io/vfuoglio/immich-handoff:nightly`) in your compose file and run `docker compose pull && docker compose up -d` when you like. A new image is built each night at 09:00 UTC, only when `main` changed, after the same tests as a release. The last 14 dated ones (`20261107-nightly`) stay available, so you can pin one to go back. A nightly is not reviewed like a release and has no section of its own in the changelog (read the changes under `Unreleased` in [CHANGELOG.md](../CHANGELOG.md)). It can upgrade the data file, and an older version refuses to open a newer file, so **make a backup before you try it** (the app makes one every day on its own, see [Automatic backups](#automatic-backups)) and expect that going back to a release may mean restoring one.

**Moving to a version that has `RECEIVER_DELETE`** (the changelog says which): with the default `on`, the receiver keys of services made earlier gain `asset.delete` the next time their owner opens the page, so copies can expire and a hand-over can be taken back. Set `RECEIVER_DELETE=off` before the restart to keep keys as they are. The details and the warning are in the changelog and [Settings](settings.md).

The data format upgrades itself when the app starts. Going back to an older version afterwards is not supported: an older version refuses to open a newer database. Read [the changelog](../CHANGELOG.md) first for anything that changes behaviour.

The image comes from Docker Hub: `docker-compose.yml` names `vfuoglio/immich-handoff:1.1`, which follows the 1.1.x fixes. The same tags are also published to GitHub's registry, as `ghcr.io/vfuoglio/immich-handoff`, if you prefer that one or Docker Hub's pull limits get in your way. Checking out a new release brings a compose file that names the matching image tag; the versions that exist are listed on the repository's Packages page and under Releases, each with its changelog. Images are built for amd64 and arm64. To build from the source instead, add the build file: `docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build`.

**Building elsewhere and moving the image.** The build can be slow on a small device, so you can build on a faster machine and copy the result: `docker buildx build --platform linux/amd64 -t immich-handoff:<tag> --load .` (use `linux/arm64` for an ARM machine; `uname -m` on the machine that will run it shows which), then `docker save immich-handoff:<tag> | gzip > immich-handoff.tar.gz`, copy the file to the machine that will run it and run `gunzip -c immich-handoff.tar.gz | docker load` there. Use a new tag each time so you can roll back.

**An untagged image of a gigabyte or more after building.** The `Dockerfile` has two stages: one with the Go toolchain and the source, and the 15 MB image you run. Some Docker setups (the classic builder, `DOCKER_BUILDKIT=0`) keep the first stage as an image shown as `<none>:<none>`. It is only the leftover of the build and nothing uses it. Remove it with `docker image prune`, which deletes every untagged image on that machine, so check `docker images -f dangling=true` first. `docker builder prune` also clears the build cache.

Before you upgrade **Immich** itself, you can run this project's whole test suite against the new Immich: `IMMICH_VERSION=<tag> test/run.sh` (see [Architecture](architecture.md#testing)).

## Installing the page on a phone

People reach the page from their phone, next to the Immich app, so it can be added to the home screen and opened like an app (no address bar, the app's icon and name). The page carries what a browser needs: a web app manifest, icons in the sizes phones ask for (including one that a phone may crop to any shape), the theme colours, and an icon for the iPhone.

- **Android (Chrome and other Chromium browsers):** open the page, then the browser menu, **Install app** or **Add to Home screen**.
- **iPhone and iPad (Safari):** open the page, then **Share**, **Add to Home Screen**.

What it needs from you:

- **HTTPS.** Browsers only install a page served over HTTPS (or from `localhost`). Behind a reverse proxy this is the proxy's certificate, as for Immich itself.
- **The manifest and the icons must be reachable with the person's sign-in.** The page asks for the manifest with the browser's cookies (`crossorigin="use-credentials"`), because behind a login in front of the app (single sign-on at the proxy, for example) a request without cookies would be sent to the login page and the manifest would be ignored. Nothing special is needed otherwise: everything is under the page's own `static/` folder, so it also works behind a path prefix (the manifest's start address is relative).
- **Nothing is cached.** The page works only online, as it must (it shows what is happening now), and no signed-in page is ever kept by the page itself. The home-screen icon is just a shortcut. The one service worker the app has (`static/sw.js`) exists only to show push notifications when a person turned them on (see [Notifications on devices](#notifications-on-devices)); it has no fetch handler and no cache, and it is registered for the `static/` folder only, so it never sits between the page and the app.

I have checked the manifest, the icons and the headers with tests, but not installed it on a physical phone here; if the install option does not show up, the usual cause is the page not being served over HTTPS, and your browser's developer tools (Application, Manifest) say what it found wrong.

## Weekly digest

The weekly digest of a service ([Using it](using.md#weekly-digest)) is made by the app itself, on Monday at about 08:00 in the app's time zone (`TZ`; set it, or it is UTC in the container). It needs `HANDOFF_STORE_KEY` (the key it uses is the service's, kept in the data file) and Immich reachable; it makes no mail or notification of its own beyond one line to the owner. It looks at the app's record of hand-overs, which is kept for 8 days, so a digest that could not run for more than a day after Monday (the app was off) has fewer than seven days to look at. With `PUBLIC_IMMICH_URL` set the card shows the link as an address; without it, the key to put after your Immich address and `/share/`.

## Photos by email

A drop box can have an email address: guests mail photos to it, and they arrive like uploads ([Using it](using.md#more-ways-in)). The app does this by **reading a mailbox you give it** over IMAP; it runs no mail server and opens no port. It is off unless you set `MAIL_IN_HOST`.

1. **Make a mailbox** that accepts plus-addressing, which Gmail, Fastmail, Proton and most hosted and self-hosted mail systems do (`drop+anything@example.org` lands in `drop@example.org`). Use a mailbox that is only for this: **the app deletes the mail it handles** (or, with `MAIL_IN_AFTER=seen`, only marks it read).
2. **Set** `MAIL_IN_HOST`, `MAIL_IN_USER`, `MAIL_IN_PASSWORD` (or `MAIL_IN_PASSWORD_FILE`) and `MAIL_IN_ADDRESS=drop@example.org`; see [Settings](settings.md). For Gmail and some others you need an app password. `MAIL_IN_TLS=none` is allowed only for a mail server on the same machine.
3. **Check it:** `docker compose exec immich-handoff /immich-handoff doctor` logs in and opens the folder (it reads no mail) and tells you what a box's address will look like.
4. **Owners** then see **Give this box an email address** on each box (not on boxes with a password).

What it does with a mail: it reads one that was sent to an address of ours, takes the photos and videos attached (what an attachment really is is decided from its first bytes, not its name or type), and hands each to the box exactly as an upload: the box's limits, approval, filing and log apply. The text, other attachments, mail to an unknown address and mail from a sender who sends too much are dropped without a reply (a reply would go to forged addresses and make the app a source of junk). A mail bigger than `MAIL_IN_MAX_MB` is left unread. Mail that could not be stored because Immich was unreachable is tried at the next three looks, then let go. Mail is slow: a photo arrives within `MAIL_IN_EVERY` (a minute by default) plus the mail system's own delay.

## Notifications on devices

A person can allow notifications on the phone or computer they use: the **Notifications on this device** card on the Overview. The notification arrives with the page closed. Each device has its own choice, *everything* or *only what needs me*, and the words come in the language the page was in when the person turned it on. A person can see their devices, send a test to one and remove it. What is sent is the lines of their Activity list, one notification for what happens within about ten seconds, plus short summaries: **N photos arrived from Bob in Holiday** to those who receive, and **N photos were not handed over** to a sender whose photos the receiver has in the trash. Notifications never carry a photo, a file name or a thumbnail.

What it needs:

- **HTTPS.** Browsers only allow notifications on a page served over HTTPS (or from `localhost`). Without it the card says so.
- **A contact for the push services.** Set `PUBLIC_APP_URL` to the page's https address, or `PUSH_CONTACT` to a `mailto:` or https address. The push services (Google, Mozilla, Apple, Microsoft) can see it. With neither, notifications on devices are off and the log says why. `PUSH=off` switches them off.
- **People can join from the page** (`HANDOFF_STORE_KEY` set), since the devices are kept in the data file.
- **The app can reach the push services** (outbound https to `fcm.googleapis.com`, `*.push.services.mozilla.com`, `web.push.apple.com` / `*.push.apple.com`, `*.notify.windows.com`). Nothing else is ever called; an address from a browser that is not one of these is refused.

What is stored for each device: the address of the browser's push service and two keys that encrypt messages for that browser, sealed with the store key like every other secret, the language, the choice, a short name such as "Firefox on Linux", and dates. A device the push service says is gone, or that fails three times in a row, is forgotten. A device gets at most 30 notifications an hour. The key the app signs with (VAPID) is made at the first start and kept sealed in the data file; changing the store key makes devices unreadable, and people turn it on again.

**Browsers.** Chrome, Edge and Firefox on computers and Android should work. On an **iPhone or iPad** pushes work only for the page added to the Home Screen (iOS 16.4 or later), opened from there, and notification buttons are not shown. I have tested the encryption against the RFC's example and the rest with test servers, not with a real push service or a real iPhone: please report what you find.

## Letting guests reach a drop box

A [drop box](using.md#drop-boxes-photos-from-people-who-do-not-have-immich) is for people who have no Immich login, so its pages (`/drop/<link>`, the short `/d/<code>`, and what hangs off them, such as `/upload` and `/mine`; and, for a [wall](using.md#the-wall), `/wall/<link>`) must be reachable from the internet **without any sign-in in front of them**. The rest of the page keeps working only for people signed in to Immich; you do not open that.

- **Same hostname rule.** Guests only need the link, so the hostname does not matter to them, but the link is built from the address the owner has the page open on. Owners should open the page on the address guests can reach.
- **Behind single sign-on at the proxy** (Authelia, Authentik, oauth2-proxy and the like): add a rule that lets `/handoff/drop/`, `/handoff/d/` and `/handoff/wall/` (or `/drop/`, `/d/` and `/wall/` on the page's own address) through without the login. Leave everything else behind it.
- **Large files.** The nginx example above already has `client_max_body_size 0`. Add `proxy_request_buffering off;` to the `/handoff/` location so a big video streams to Immich instead of being stored by the proxy first, and keep `proxy_read_timeout` generous.
- **Guests' addresses.** Set `DROPBOX_CLIENT_IP_HEADER` (for nginx, `proxy_set_header X-Forwarded-For $remote_addr;` and `DROPBOX_CLIENT_IP_HEADER=X-Forwarded-For`) so the log and the per-guest limits can tell guests apart.
- **Limit what you expose.** If you do not want any open endpoint, do not make drop boxes: nothing is open until an owner makes one, and revoking every box leaves `/drop/` answering as if it never existed.

## Monitoring

- **Log:** `docker compose logs`. Each pass logs what it found and did; errors are marked `ERROR`.
- **Notifications:** the app can tell you when something needs attention, through ntfy, a Home Assistant webhook or any JSON endpoint. See [Notifications](#notifications).
- **Container health:** the image checks itself every 30 seconds, so `docker ps` and `docker compose ps` show `healthy` or `unhealthy`, and tools that read Docker's health status can show or alert on it. The check asks the app's own `/healthz` (on `LISTEN_ADDR`) and passes only on `ok`. The image has no shell or `curl`, so the check is built into the program: `docker compose exec immich-handoff /immich-handoff --healthcheck` runs it by hand and exits 0 when healthy. A service that should wait for it can use `depends_on: immich-handoff: condition: service_healthy`. It does not test the connection to Immich; the page and the log show that.
- **Health and metrics:** `GET /healthz` answers `ok`. `GET /metrics` returns counters in Prometheus text format (passes, photos by result, services, version) and needs the `X-Webhook-Secret` header with your secret.

## Notifications

The app tells whoever runs it when something needs attention. Every message has a **kind**, and each destination takes all kinds or only the ones you list:

| Kind | What it is about |
|---|---|
| `problem` | A pass that failed (photos the receiver refused are not a problem for you and are not sent), photos found only by the daily sweep, an album that cannot be read or filed into, a key that stopped working. |
| `backup` | A backup that failed, was forced, or was downloaded. |
| `bridge` | A receiver on another server that cannot be reached, whose key was revoked, or that is back. |
| `dropbox` | A drop box that paused itself, will close soon, or closed. |
| `info` | A daily limit reached, a big album hand-over, a person or service removed by an administrator, a new receiver on another server. |

**One destination, the simple way.** Set `NOTIFY_URL` to an ntfy topic (`https://ntfy.example.com/handoff`). Every kind goes there as the message text, with a title, a priority (problems are high), the kind as a tag and, if `PUBLIC_APP_URL` is set, a link that opens the page. Installs that already used `NOTIFY_URL` keep working.

**Several destinations.** Set `NOTIFY_FILE=/notify.yaml`, mount the file like the routes file (see `notify.example.yaml`), and list each place with a `name`, a `format` (`ntfy`, the default, or `json`), a `url`, an optional `auth` (sent as a bearer token) and the `kinds` it takes. Secrets are written as `${VARIABLE}` and read from the environment, so none lives in the file. `NOTIFY_URL` and the file can be used together.

```yaml
destinations:
  - name: phone                       # your ntfy app: everything
    url: https://ntfy.example.com/handoff
  - name: home-assistant              # automations: problems and backups only
    format: json
    url: http://homeassistant.local:8123/api/webhook/${HA_WEBHOOK_ID}
    kinds: [problem, backup]
```

**Email.** A destination with `format: email` sends the messages through your mail server:

```yaml
  - name: mail
    format: email
    host: smtp.example.com
    user: handoff@example.com
    password: ${SMTP_PASSWORD}
    from: Immich Handoff <handoff@example.com>
    to: [admin@example.com]
    kinds: [problem, bridge]
```

`tls` is `starttls` by default (port 587, the connection must be upgraded or nothing is sent), `tls` for a server that is encrypted from the first byte (port 465), or `none` (port 25) for a relay on a network you trust; a password is never sent with `none`, and the app refuses to start with that combination. Messages that arrive within `group` (two minutes unless you say otherwise; `0` sends one mail for each message) become **one mail**, with a line for each, and the subject names the most serious kind. Each mail has a plain-text part and a simple HTML part (a button that opens the page when `PUBLIC_APP_URL` is set); it never has pictures from elsewhere or tracking links, and the subject never names anyone.

**Email to the people taking part.** One mail destination can instead be the server the app uses to write to each person who asks for it (the **Email me** card, see [Using it](using.md#email-about-what-happens-to-you)):

```yaml
  - name: people
    format: email
    host: smtp.example.com
    user: handoff@example.com
    password: ${SMTP_PASSWORD}
    from: Immich Handoff <handoff@example.com>
    people: true
    # group: 5m          # how long what happens to one person waits for company; 1s at the least
```

Mark it with `people: true` and leave out `to` and `kinds`: it writes to nobody until a person turns it on, and it never carries your administrator messages. It needs **`PUBLIC_APP_URL`** (every mail links to the page and to the way to stop the emails) and `HANDOFF_STORE_KEY` (people turn it on from the page); the app refuses to start without them. A person gets the lines their page shows under Activity, in their language, in one mail per `group` and at most six mails an hour; they can stop it from any mail with one click. The address is the one Immich shows for their account and is kept in the data file (and so in backups) while mail is on, and forgotten when it is turned off. Only one destination can have `people: true`.

**The JSON format** posts `{"source":"immich-handoff","kind":"problem","title":"...","text":"...","link":"...","time":"2026-11-11T22:00:00Z"}`. In Home Assistant, create an automation with a **Webhook** trigger (turn off "Only accessible from the local network" only if the app is outside it), and use `{{ trigger.json.text }}` and `{{ trigger.json.kind }}` in the action, for example a `notify.mobile_app_<phone>` call, a light or a spoken message. The same format fits n8n, Node-RED and anything that accepts a JSON POST.

**Links in messages.** Mail and notifications to the people taking part carry one-tap buttons for the few things a person can answer (approving a request, resuming). They are built from `PUBLIC_APP_URL`; without it messages carry no buttons. See [Answering from a message](using.md#answering-from-a-message) and [Action links](security.md#action-links).

**Testing it.** Nothing tells you a destination works until something goes wrong, so test it: on the administrator's page, **Notifications** lists each destination (its name, format and the kinds it takes, never its address) and **Send a test notification** sends one message to each, whatever kinds it takes, and shows how each answered (delivered, or why not: "answered 401", "could not reach it"). A test makes one attempt and mail goes out at once. From the command line, `immich-handoff doctor` lists the destinations and checks the settings (it sends nothing), and `immich-handoff doctor --send-test` also sends the test. The server set aside for mail to the people taking part is not tested here; each person has a test button for their own mail.

**How often you are told.** A problem that lasts is told **once**, not on every pass: a hand-over service that keeps failing, or a backup that fails every hour, sends one message, a reminder ("Still not fixed after 1 day: ...") if it still lasts a day later, and a message when it ends ("Hand-overs for Holiday work again", "Backups work again"). The app remembers this in memory only, so after a restart a problem that still lasts is told once more. Photos that were refused because the receiver has them in the trash are not a problem for you and no longer send a message (the page shows them and the sender is told); a service that is paused is not announced as fixed. Messages that mark an event, such as a key that stopped working or a daily limit reached, were already sent once per event and still are.

**How delivery behaves.** Sending never slows a hand-over: messages are queued and sent in the background. A destination that answers with an error from its own side or cannot be reached is tried up to three times; one that refuses the message (a wrong token, a wrong topic) is logged once and not retried; a redirect counts as a failure, so fix the address. The log names the destination and the answer, never the address, because a topic or a webhook id is a secret. Messages name people, services and drop boxes, so send them only to a server you trust. Messages are in English.

## Checking the setup

`docker compose exec immich-handoff /immich-handoff doctor` tests the settings, the connection to Immich, every stored key and its permissions, and each sender's workflow, and says how to fix what fails. It is read-only, takes a few seconds, and exits 1 when anything failed, so it is safe to run on a live install and to use from a script or a scheduled job. What it checks, and how to read it, is in [Troubleshooting](troubleshooting.md#check-your-setup-first).

## Removing it

1. Ask people to press **Leave** (and owners **Delete service**) on the page. That removes the API keys and workflows the app made in their Immich accounts.
2. `docker compose down -v` stops the app and deletes its data volume.

Anything left behind is easy to find in Immich: API keys and workflows whose names start with `handoff` or `immich-handoff`.
