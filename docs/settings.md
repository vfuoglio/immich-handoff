# Settings

Everything is set in `.env` (copy `.env.example`). Only the first three are required.

| Setting | Default | Meaning |
|---|---|---|
| `WEBHOOK_SECRET` | | **Required**, 16+ characters. Shared secret Immich sends with every event. |
| `HANDOFF_STORE_KEY` | | **Required** for joining and offering services from the page, 16+ characters. Encrypts the saved API keys. Keep a copy: without it they cannot be read. `HANDOFF_STORE_KEY_FILE` reads it from a Docker secret file instead. |
| `IMMICH_NETWORK` | `immich_default` | Docker network Immich runs on (used by `docker-compose.yml`). |
| `IMMICH_BASE_URL` | `http://immich-server:2283` | Address of Immich as seen from this container. |
| `PUBLIC_WEBHOOK_URL` | `http://immich-handoff:8080/webhook/immich` | Address the Immich **server** uses to reach this app (not your browser). |
| `PUBLIC_IMMICH_URL` | | Address of Immich as your browser sees it; switches on thumbnails of waiting photos. |
| `DATA_DIR` | `/data` | Where the saved memberships live (the `handoff-data` volume). |
| `BACKUP_EVERY` | `24h` | How often the app makes a backup of its data on its own: a time such as `12h` (one hour at the least), or `off`. See [Automatic backups](operations.md#automatic-backups). |
| `BACKUP_KEEP` | `7` | How many automatic backups to keep; older ones are removed. Backups you asked for are never removed. |
| `BACKUP_DIR` | `/data/backups` | Where backups go. A second mounted folder is safer than one beside the data. |
| `HANDOFF_ADMIN_BACKUP_DOWNLOAD` | off | `true` adds a **Download backup** button to the administrator's page. Off by default: the file holds every stored key (encrypted) and everyone's name and email. Read [the limits](operations.md#download-a-backup-from-the-administrators-page) first. |
| `LISTEN_ADDR` | `:8080` | Where the app listens inside the container. The built-in health check uses it too. |
| `NOTIFY_URL` | | ntfy-style URL that receives a message when something needs attention. |
| `ALBUM_CHECK_MINUTES` | `15` | How often albums that senders linked to a service are looked at for photos to hand over. Immich sends no event when a photo is added to an album, so this is a timer. `0` leaves it to the passes that other events start. See [handing over an album](using.md#handing-over-a-whole-album). |
| `SWEEP_INTERVAL_HOURS` | `24` | How often the safety-net check runs (catch-up, key and workflow checks, inbox tidy-up). |
| `CLEANUP_INTERVAL_MINUTES` | `15` | How often photos filed in an album or past their expiry lose the inbox tag. `0` leaves that to the daily check. See [why a timer](using.md#when-the-inbox-tag-goes-away). |
| `LOG_LEVEL` | `info` | `debug` also logs incoming events. |
| `DRY_RUN` | `false` | Log what would happen and change nothing. |

## Fixed accounts only

These apply to services you define yourself ([Fixed accounts](fixed-accounts.md)). Services people create on the page choose their own inbox tag and expiry in the form, and each sender chooses move or copy.

| Setting | Default | Meaning |
|---|---|---|
| `IMMICH_SOURCE_API_KEYS` | | API keys of the sending accounts, comma separated. |
| `IMMICH_DEST_API_KEY` | | API key of the receiving account. |
| `ROUTES_FILE` | | Path of a routes file for several services. |
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
| `QUIET_HOURS` | | Hand photos over only between these times, `HH:MM-HH:MM` (for example `01:00-06:00`; `22:00-06:00` crosses midnight). Outside them photos wait, still tagged, and go when the hours begin. A service that sets its own hours keeps them. See [Quiet hours](using.md#quiet-hours-and-what-a-service-accepts). |
| `QUIET_LARGE_MB` | | With `QUIET_HOURS`: only files larger than this many megabytes wait for the hours; smaller ones go at once. Empty or `0`: every file waits. |
| `ACCEPT_MIN_RATING`, `ACCEPT_PHOTOS_ONLY`, `ACCEPT_ORIENTATION`, `ACCEPT_MAX_MB` | | What the single service of a plain `.env` install takes in: at least this rating (1 to 5), `true` for no videos, `landscape` or `portrait`, and the largest file in megabytes. Services made on the page set them there, and services in a routes file under `accept:`. See [Quiet hours and what a service accepts](using.md#quiet-hours-and-what-a-service-accepts). |
| `RECEIVER_DELETE` | `on` | `off` stops receiver keys from being able to move photos to their owner's trash. On (the default), keys of services made on the page have `asset.delete`, which lets copies expire (a service's lifetime) and lets a sender take their last hand-over back. **Changing it:** keys are brought in line when their owner next opens the page after the app restarts (the permission is added, or taken off again), because only the owner's own login can change a key. See [Security](security.md#what-a-key-can-do). |
| `DELETE_FORCE` | `false` | `true` deletes moved originals permanently instead of trashing them. |
