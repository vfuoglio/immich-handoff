# Settings

Everything is set in `.env` (copy `.env.example`). Only the first three are required.

| Setting | Default | Meaning |
|---|---|---|
| `WEBHOOK_SECRET` | | **Required**, 16+ characters. Shared secret Immich sends with every event. |
| `HANDOFF_STORE_KEY` | | **Required** for joining and offering services from the page, 16+ characters. Encrypts the saved API keys. Keep a copy: without it they cannot be read. `HANDOFF_STORE_KEY_FILE` reads it from a Docker secret file instead. |
| `IMMICH_NETWORK` | `immich_default` | Docker network Immich runs on (used by `docker-compose.yml`). |
| `IMMICH_BASE_URL` | `http://immich-server:2283` | Address of Immich as seen from this container. |
| `PUBLIC_WEBHOOK_URL` | `http://immich-handoff:8080/webhook/immich` | Address the Immich **server** uses to reach this app (not your browser). |
| `PUBLIC_IMMICH_URL` | | Address of Immich as your browser sees it; switches on thumbnails of waiting photos, the link into a memory in an [On this day](../guides/show/memories.md) message, the links from the [album tree](../guides/show/album-tree.md) into Immich, and, for a [story](../guides/show/stories.md)'s preview in chats, the address people use to reach this app (`PUBLIC_APP_URL`). |
| `DATA_DIR` | `/data` | Where the saved memberships live (the `handoff-data` volume). |
| `BACKUP_EVERY` | `24h` | How often the app makes a backup of its data on its own: a time such as `12h` (one hour at the least), or `off`. See [Automatic backups](../admin/operations.md#automatic-backups). |
| `BACKUP_KEEP` | `7` | How many automatic backups to keep; older ones are removed. Backups you asked for are never removed. |
| `BACKUP_ENCRYPT` | `false` | Seal every backup file as a whole with the store key (names, emails and settings then are as private as the keys). `restore` opens both kinds. See [Encrypted backups](../admin/operations.md#encrypted-backups). |
| `BACKUP_DIR` | `/data/backups` | Where backups go. A second mounted folder is safer than one beside the data. |
| `HANDOFF_ADMIN_BACKUP_DOWNLOAD` | off | `true` adds a **Download backup** button to the administrator's page. Off by default: the file holds every stored key (encrypted) and everyone's name and email. Read [the limits](../admin/operations.md#download-a-backup-from-the-administrators-page) first. |
| `LISTEN_ADDR` | `:8080` | Where the app listens inside the container. The built-in health check uses it too. |
| `WEBHOOK_HEADER` | `X-Webhook-Secret` | The name of the header in which Immich's workflow sends `WEBHOOK_SECRET`. Change it only if a proxy in front of the app needs another name. |
| `NOTIFY_URL` | | ntfy topic URL that receives every message the app has for you (title, priority, tag and a link). See [Notifications](../admin/operations.md#notifications). |
| `FRAME_REFRESH` | `2m` | How often a frame ([Frames](../guides/show/frames-and-digest.md#frames-an-album-on-a-screen)) looks at its album again, from `5s` to `1h`. It is also how long the app keeps what it read, so it is how soon a photo added to or taken out of the album shows on the screen. |
| `SHARED_ALBUMS_ENABLE` | off | `true` lets people connect their Handoff with the Handoff of someone on another server, to share albums between the two libraries (see [Albums shared with other servers](../guides/share/shared-albums.md#pick-a-way)). Sharing with another Handoff (two people, or a group) needs `PUBLIC_APP_URL`, because the other app is given this app's address; without it only sharing with someone who has an Immich and no Handoff works, and the log says so. The addresses the app may connect to follow the same rules as bridges (`BRIDGE_ALLOW_PRIVATE` also applies). |
| `SHARED_ALBUMS_EVERY_MINUTES` | `3` | How often each shared album ([Albums shared with other servers](../guides/share/shared-albums.md#pick-a-way)) is looked at for new photos, from `0.1` to `1440` minutes. Photos reach the other server within about this time; **Sync now** looks at once. |
| `SHARED_ALBUMS_MAX_MB` | `2048` | The largest single file a shared album carries, in megabytes; a larger one is left out with a reason shown on the album's row, and a server also refuses what is larger than its own limit. |
| `RULES_CHECK_MINUTES` | `10` | How often each [rule](../guides/automate/rules.md#rules-photos-by-who-where-and-when) looks for new matching photos, from `0.1` to `1440` minutes. Rules need people to be able to join from the page (a data file). |
| `MEMORIES_CHECK_MINUTES` | `10` | How often the app looks at who is due their daily [On this day](../guides/show/memories.md) message, from `0.1` to `1440` minutes. Each person picks their own time of day; this only sets how finely the app notices it. |
| `RULES_SETTLE_MINUTES` | `5` | How long a photo must have been in Immich before a rule offers it (so that Immich has had time to find its faces), from `0.01` to `1440` minutes. A rule that sends by itself waits six times as long. This is the usual wait: each rule can choose its own, from 1 to 60 minutes, when it is made. |
| `PET_SIDECAR_URL` | off | The address of the optional [pet helper](../guides/automate/pets.md#the-optional-pet-helper) container (for example `http://pets:8090`). With it, a rule for a pet compares the animal in each photo with the animal in the examples instead of using Immich's search, which is much more accurate; without it, or while it is down, pet rules use Immich's own search. Set by whoever runs the app, never by a user of it. |
| `IMMICH_BUTTON_ACTIONS` | on | `false` keeps only the link in Immich's header and takes away the **Hand over...** and **Add to my library** buttons that [the same script](../admin/find-handoff.md#hand-over-from-inside-immich) adds to Immich's photo viewer, selection bar and menus. |
| `PHONE_UPLOAD_WAITING_MB` | `4096` | The most one person may have waiting at a time when they [send photos from a phone](../guides/collect/phone.md#send-photos-from-a-phone). It counts what each unfinished file will weigh once whole, so a person is refused when they start a file, not half way. Needs the data folder. |
| `PHONE_UPLOAD_FILE_MB` | `2048` | The largest single file a phone may send. |
| `PHONE_UPLOAD_KEEP_HOURS` | `48` | How long the pieces of a file that was never finished are kept before they are deleted (1 to 720). |
| `MAIL_IN_HOST` | | A mail server (IMAP) the app reads, so a drop box can have an address that guests mail photos to. Off unless set. See [Photos by email](../admin/operations.md#photos-by-email). Needs `HANDOFF_STORE_KEY`. |
| `MAIL_IN_PORT` | `993` (`143` for starttls) | The mail server's port. |
| `MAIL_IN_TLS` | `tls` | `tls` (encrypted from the first byte), `starttls` (the upgrade must succeed or nothing is sent) or `none` (only for a mail server on this machine). |
| `MAIL_IN_USER`, `MAIL_IN_PASSWORD` | | The mailbox's login. The password may instead be in a file named by `MAIL_IN_PASSWORD_FILE` (a Docker secret). |
| `MAIL_IN_ADDRESS` | | The mailbox's own address, such as `drop@example.org`, without a plus sign. A box's address is `drop+<code>@example.org`, so the mailbox must accept plus-addressing. |
| `MAIL_IN_FOLDER` | `INBOX` | The folder that is read. |
| `MAIL_IN_AFTER` | `delete` | What happens to a mail once it is handled: `delete` removes it, `seen` keeps it, marked read. |
| `MAIL_IN_EVERY` | `1m` | How often the mailbox is looked at, from `5s` to `1h`. |
| `MAIL_IN_MAX_MB` | `25` | The largest mail that is read; a bigger one is left unread and dropped. |
| `NOTIFY_FILE` | | A YAML file listing more destinations (ntfy or a JSON webhook such as Home Assistant's), each taking only the kinds of message you choose. See [Notifications](../admin/operations.md#notifications). |
| `PUSH` | `on` | `off` switches off notifications on people's own devices. They are otherwise on whenever people can join from the page and there is a contact for the push services (below). See [Notifications on devices](../admin/operations.md#notifications-on-devices). |
| `PUSH_CONTACT` | | A `mailto:` address or an `https:` address the push services (Google, Mozilla, Apple, Microsoft) can reach whoever runs the app at. If empty, `PUBLIC_APP_URL` is used when it is an https address; with neither, notifications on devices stay off. The push services can see it, so do not use a personal address unless you want to. |
| `PUBLIC_APP_URL` | | This app as your browser reaches it (for example `https://handoff.example.com`). When set, each message carries a link to the right page. Required when `NOTIFY_FILE` has a destination with `people: true` (mail to the people taking part). |
| `ALBUM_CHECK_MINUTES` | `15` | How often albums that senders linked to a service are looked at for photos to hand over. Immich sends no event when a photo is added to an album, so this is a timer. `0` leaves it to the passes that other events start. See [handing over an album](../guides/share/household.md#more-ways-to-hand-over). |
| `SWEEP_INTERVAL_HOURS` | `24` | How often the safety-net check runs (catch-up, key and workflow checks, inbox tidy-up). |
| `CLEANUP_INTERVAL_MINUTES` | `15` | How often photos filed in an album or past their expiry lose the inbox tag. `0` leaves that to the daily check. See [why a timer](../start/the-page.md#when-the-inbox-tag-goes-away). |
| `LOG_LEVEL` | `info` | `debug` also logs incoming events. |
| `DRY_RUN` | `false` | Log what would happen and change nothing. |
| `THEME` | `twilight` | The colour scheme the pages start in: `twilight` (the indigo, magenta and orange of the app's icon) or `immich` (calm blues on near-neutral greys, close to Immich's own look). Everyone can pick the other one from the palette button in the bar; their choice is kept in their own browser and wins over this setting. An unknown value is ignored with a warning in the log. |
| `THEME_MODE` | `auto` | `auto` follows each browser's own light or dark setting; `light` or `dark` makes the pages start in that variant. Both colour schemes have both variants, and the half-moon button in the bar lets each person decide for themselves. |

## Fixed accounts only

These apply to services you define yourself ([Fixed accounts](../admin/fixed-accounts.md)). Services people create on the page choose their own inbox tag and expiry in the form, and each sender chooses move or copy.

| Setting | Default | Meaning |
|---|---|---|
| `IMMICH_SOURCE_API_KEYS` | | API keys of the sending accounts, comma separated. |
| `IMMICH_DEST_API_KEY` | | API key of the receiving account. |
| `ROUTES_FILE` | | Path of a routes file for several services. |
| `DESTINATIONS_FILE` | | Path of a file that names the [destinations](../contributing/design/destinations-design.md) services can hand photos to besides an Immich account: `destinations:` with, for each name, a `kind` (`folder`, `webdav`, `s3` or `sftp`) and its settings (`root` for a folder, `url`, `user` and `password` for WebDAV, and so on). A value can be `${VAR}`, and a setting `x_file: /path` is replaced by `x` with the content of that file, so secrets can come from Docker secrets. A folder's `root` is a path inside the container: mount the share there (see `docker-compose.yml`) so that the app's user (65532, or the one you set with `user:`) may write in it; `doctor` writes and removes a test file to check. See `destinations.example.yaml`. A destination may also be opened for **importing** with `import: true`: people who may use it can then follow it and add its files to their own library (see [Destinations](../guides/share/destinations.md#follow-a-destination)); leave it out for a place that holds other people's files. A service owner picks one by name; a service of the routes file names them under `destinations:` with an optional `template`. The app does not start with a file that has a problem. |
| `DESTINATIONS_USER_ADD` | off | `true` lets a person who owns a service enter a **network** destination of their own (WebDAV, S3, SFTP) on its card; the password or key is kept encrypted with `HANDOFF_STORE_KEY` and never shown again. A folder is never typed by a person: it must be one named in `DESTINATIONS_FILE`. |
| `DESTINATIONS_ALLOW_PRIVATE` | off | `true` lets destinations on a private network (a NAS at 192.168.x.x) be reached; without it only public addresses are allowed, with the same checks as for [bridges](../guides/share/other-server.md) (no redirects, no proxy, the name resolved and checked). It does not change `BRIDGE_ALLOW_PRIVATE`. |
| `ALLOW_JOIN` | `false` | Let people join the single service from the page. |
| `ALBUM` | | Name of an album, in the receiving account, that handed-over photos are put in instead of being tagged inbox. Only for the single service of a plain `.env` install; services in a routes file use `album:`. The receiving key needs `album.read`, `album.create` and `albumAsset.create`. |
| `SHARE_TAG` | `share` | Tag people add to send a photo. |
| `INBOX_TAG` | `inbox` | Tag on new photos in the receiving account. Services created on the page add `/<name>` to it. |
| `INBOX_EXPIRY_DAYS` | `30` | Days before an unfiled photo loses the inbox tag. |
| `REFUSED_TAG` | `share-duplicate` | Tag put on an original that could not be handed over. |
| `SENT_TAG` | `handed-over` | Copy mode: tag put on an original after it was copied. |
| `HANDOFF_MODE` | `copy` | `copy` keeps the senders' originals; `move` sends them to the sender's trash. |
| `CARRY_DETAILS` | `on` | `off` stops the app from carrying what Immich keeps only in its database: the archived state and a photo's description, rating and location. With `off` an archived photo arrives in the receiver's timeline and the other details are not sent. See [what is carried over](what-is-carried-over.md). |
| `TZ` | `UTC` | The time zone of quiet hours and of the dates in tags (for example `Europe/Rome`). |
| `QUIET_HOURS` | | Hand photos over only between these times, `HH:MM-HH:MM` (for example `01:00-06:00`; `22:00-06:00` crosses midnight). Outside them photos wait, still tagged, and go when the hours begin. A service that sets its own hours keeps them. See [Quiet hours](../guides/automate/controls.md#quiet-hours-and-what-a-service-accepts). |
| `QUIET_LARGE_MB` | | With `QUIET_HOURS`: only files larger than this many megabytes wait for the hours; smaller ones go at once. Empty or `0`: every file waits. |
| `ACCEPT_MIN_RATING`, `ACCEPT_PHOTOS_ONLY`, `ACCEPT_ORIENTATION`, `ACCEPT_MAX_MB` | | What the single service of a plain `.env` install takes in: at least this rating (1 to 5), `true` for no videos, `landscape` or `portrait`, and the largest file in megabytes. Services made on the page set them there, and services in a routes file under `accept:`. See [Quiet hours and what a service accepts](../guides/automate/controls.md#quiet-hours-and-what-a-service-accepts). |
| `RECEIVER_DELETE` | `on` | `off` stops receiver keys from being able to move photos to their owner's trash. On (the default), keys of services made on the page have `asset.delete`, which lets copies expire (a service's lifetime) and lets a sender take their last hand-over back. **Changing it:** keys are brought in line when their owner next opens the page after the app restarts (the permission is added, or taken off again), because only the owner's own login can change a key. See [Security](security.md#what-a-key-can-do). |
| `DROPBOX_CLIENT_IP_HEADER` | | The header in which your reverse proxy puts a guest's real address for [drop boxes](../guides/collect/events.md#make-a-drop-box), for example `X-Forwarded-For` or `X-Real-IP`. The last address in it is used. Without it every guest seems to come from the proxy, and the per-guest limits count them all as one (the limits of each box still hold). Set it only if your proxy sets the header itself and drops one sent by a client. |
| `BRIDGE_ENABLE` | `false` | `true` lets the owner of a service hand photos over to someone on **another Immich server** (a bridge). Off, nothing about bridges appears and the app never connects to an address it was given. See [Security](security.md). |
| `BRIDGE_ALLOW_PRIVATE` | `false` | Only with `BRIDGE_ENABLE`. By default the app connects to another server only over `https://` and only when its name resolves to public addresses (never loopback, private, link-local, unique-local, the cloud metadata address, or a redirect). `true` also accepts private addresses and plain `http://`, for people who run both servers themselves behind a VPN. Leave it off otherwise: it lets an address someone typed make the app reach into your own network. |
| `BRIDGE_RETRY_MINUTES` | `1` | Only with `BRIDGE_ENABLE`. How long a receiver on another server is left alone after it could not be reached; the pause doubles with every failure in a row, up to an hour. Photos wait, still tagged, meanwhile. |
| `DELETE_FORCE` | `false` | `true` deletes moved originals permanently instead of trashing them. |
