# immich-handoff

**Hand photos from personal Immich accounts over to a shared family account, just by tagging them.**

Everyone's phone backs up to their own Immich account. One shared account holds the family library, with its albums and timeline in order. With immich-handoff, a family member tags a photo `share` in the Immich app and the photo moves to the shared account within seconds. Nothing else to do, no second app.

## How it works

1. **Tag.** In the Immich app, select photos and add the tag `share`.
2. **Hand over.** Immich notifies immich-handoff. It copies each photo into the family account, checks that the copy is intact, tags it `inbox`, and moves the original to the sender's trash.
3. **File.** The family account's owner puts the photos into albums as usual. Filter by the `inbox` tag to see what is new.
4. **Tidy up.** The `inbox` tag removes itself once a photo is in an album, or after 30 days. Photos are never deleted by this step.

If anything goes wrong while copying, the original stays where it is and the problem is logged.

## Requirements

- **Immich 3.2.0 or newer.** Earlier versions cannot notify the app when a tag is added.
- **Docker** on the same machine (or Docker network) as Immich.
- **One Immich account for each person who sends photos, plus the shared family account.**
- Tags switched on for each sending account (Account Settings -> Features -> Tags).

## Install

### 1. Create API keys

In Immich, sign in to each account in turn and go to **Account Settings -> API Keys -> New API Key**. Create one key per account with these permissions, and copy each key as you create it.

| Account | Permissions |
|---|---|
| Each person who **sends** photos | `user.read` `tag.read` `tag.create` `tag.asset` `asset.read` `asset.download` `asset.delete` `workflow.read` `workflow.create` `workflow.update` |
| The shared **family** account | `user.read` `tag.read` `tag.create` `tag.asset` `asset.read` `asset.upload` |

### 2. Download and configure

```
git clone https://github.com/vfuoglio/immich-handoff.git
cd immich-handoff
cp .env.example .env
```

Open `.env` and fill in the required section at the top:

- `IMMICH_SOURCE_API_KEYS`: the sender keys, separated by commas.
- `IMMICH_DEST_API_KEY`: the family account's key.
- `WEBHOOK_SECRET`: any long random text (`openssl rand -hex 24` makes one).
- `IMMICH_NETWORK`: the name of the Docker network Immich is on. Run `docker network ls` and look for the one ending in `_default` that belongs to Immich. A standard Immich install uses `immich_default`, which is already the default.

The other values work as they are if Immich runs on that network under its standard service name, `immich-server`.

### 3. Start it

```
docker compose up -d --build
docker compose logs -f
```

Within about a minute you should see a `workflow ready` line for every sending account. That is all the setup there is: immich-handoff creates the `share` tag and the Immich workflow that notifies it, in each sending account, by itself.

### 4. Try it

Tag one photo `share` in the Immich app on a sending account. Within a few seconds the log shows `imported`, the photo appears in the family account with the `inbox` tag, and the original is in the sender's trash.

To rehearse without changing anything, set `DRY_RUN=true` in `.env` first (then `docker compose up -d`). The log says what would happen.

## Day to day

| What | How |
|---|---|
| Send a photo | Add the tag `share` in the Immich app. |
| See what arrived | In the family account, filter by the tag `inbox`. |
| A photo came back tagged `share-duplicate` | The family account already had that photo in its **trash**, so it was not imported and the original was kept. Empty the family trash and add `share` again to retry. |
| Photos already in the family library | A photo that is already there is not imported again, and the sender's original is removed. |

By default originals go to the sender's **trash**, not into oblivion, so a mistake can be undone for 30 days (Immich's trash period). Phone backups skip a photo that is in the trash. Once Immich empties the trash, a phone that still has the photo may back it up again, so delete the photo from the phone too.

## Several services and receivers (optional)

A plain install is one service: everyone's `share` photos go to one family account. To run more than one, or to send the same photo to several accounts, describe them in a routes file:

1. Copy `routes.example.yaml` to `routes.yaml` and edit it. It names your accounts (keys come from `.env` variables, so no secret is written in the file) and lists services. Each service has a **tag**, the **senders** who use it and the **receivers** who get the photos.
2. In `.env` set `ROUTES_FILE=/routes.yaml` and add the key variables the file refers to (for example `ALICE_KEY=...`).
3. In `docker-compose.yml` uncomment the `routes.yaml` volume line, then `docker compose up -d`.

With several receivers on one service, the original is removed only after **every** receiver holds a verified copy. If one receiver refuses (its trash holds that photo) or fails, the original stays and nothing is lost. An account that sends in one service and receives in another needs both permission lists from the table above. Each service gets its own workflow in the sender's account, named `immich-handoff: <service>`.

## Status page (optional)

`http://<host>:8080/` shows who is signed in, how many photos are waiting, and the result of the last run, plus a **Sync now** button. It uses your Immich login, so it only works when it is served on the **same hostname** as Immich. The usual way is a reverse proxy that forwards a path such as `/handoff/` on your Immich address to port 8080. Only the sending and family accounts are let in.

## Settings

Everything is set in `.env`. The keys and `WEBHOOK_SECRET` are required; the rest are optional.

| Setting | Default | Meaning |
|---|---|---|
| `IMMICH_SOURCE_API_KEYS` | | API keys of the sending accounts, comma separated (not needed with a routes file) |
| `IMMICH_DEST_API_KEY` | | API key of the family account (not needed with a routes file) |
| `ROUTES_FILE` | | Path of a routes file for several services (see above) |
| `WEBHOOK_SECRET` | | Shared secret Immich sends with every event |
| `PUBLIC_WEBHOOK_URL` | `http://immich-handoff:8080/webhook/immich` | Address the Immich **server** uses to reach this app |
| `IMMICH_BASE_URL` | `http://immich-server:2283` | Address of Immich as seen from this container |
| `IMMICH_NETWORK` | `immich_default` | Docker network Immich runs on |
| `SHARE_TAG` | `share` | Tag people add to send a photo |
| `INBOX_TAG` | `inbox` | Tag on new photos in the family account |
| `REFUSED_TAG` | `share-duplicate` | Tag put on an original that could not be handed over |
| `INBOX_EXPIRY_DAYS` | `30` | Days before an unfiled photo loses the `inbox` tag |
| `DELETE_FORCE` | `false` | `true` deletes originals permanently instead of trashing them |
| `NOTIFY_URL` | | ntfy-style URL that gets a message when something needs attention |
| `SWEEP_INTERVAL_HOURS` | `24` | How often the safety-net check runs |
| `DRY_RUN` | `false` | Log what would happen and change nothing |
| `LOG_LEVEL` | `info` | `debug` also logs incoming events |

## Troubleshooting

Start with the app's log: `docker compose logs --tail 100`. Then find your symptom.

| Symptom | Likely cause and fix |
|---|---|
| The app stops right away with "are required" | `.env` is missing a required value (keys or `WEBHOOK_SECRET`). |
| `HTTP 401` or `HTTP 403` in the log | An API key is missing a permission from the table above. Create a new key with the full list. |
| No `workflow ready` line | Immich needs about a minute after it starts before workflows can be created; the app keeps retrying. If it never appears, check the key permissions and that Immich is 3.2.0 or newer. |
| Tagged a photo, nothing happens | In Immich open that account's workflow named `immich-handoff` and look at its runs. **No runs:** the tag was not `share`, or tags are not switched on for the account. **Failed runs:** Immich cannot reach `PUBLIC_WEBHOOK_URL`; it must be an address the Immich server container can reach. |
| Log says photos were found "by the daily sweep instead of by webhook" | Same as above: the notifications are not arriving. The photos still move, but only once a day. |
| `IMMICH_NETWORK` error on start | The network name is wrong. Run `docker network ls` and put Immich's network in `.env`. |
| A photo is tagged `share-duplicate` | See "Day to day" above. |
| The original is still in the sender's account after tagging | The hand-over failed or could not be verified, so the original was kept on purpose. The log has an `ERROR` line saying why; fix the cause, and the photo is retried at the next run. |
| A tag you just added is gone | Immich occasionally drops a tag that is added in the first moments after a photo was uploaded, while it is still processing the photo. This happens inside Immich (we saw about 1 in 50 in tests with this app switched off). Add the tag again. |
| Status page says "Sign in to Immich first" | Serve it on the same hostname as Immich (see the status page section). |

## Architecture

```
 Immich app ──tag "share"──►  Immich server
                                   │  workflow: "tag added" + "tag is share"
                                   ▼  POST /webhook/immich  (with the secret)
                            ┌───────────────┐
                            │ immich-handoff│── Immich API ──► sender's account  (find, download, trash original)
                            │   (one Go     │── Immich API ──► family account    (upload, tag inbox, tidy tags)
                            │   container)  │
                            └───────────────┘
```

- **One small container, no database, nothing written to disk.** All state lives in Immich (the tags and the workflow). The last-run summary is held in memory.
- **Events only wake the app.** When Immich reports a tag, the app asks Immich which photos carry `share` and handles those. A lost or repeated event therefore cannot lose or duplicate a photo. Bursts of events are combined into one run.
- **Safety net.** One run at startup and one a day (`SWEEP_INTERVAL_HOURS`) pick up anything missed while the app was down, and do the `inbox` tidy-up.
- **Streaming copy.** A photo is streamed straight from the sender's account into the family account, never stored on the app's disk. The app computes its SHA-1 on the way and compares it with what Immich stored.
- **The original is removed last,** and only after the copy exists, is not in the family trash, matches the checksum, and carries the `inbox` tag.
- **Self-configuring.** At startup, and again at every daily run, the app makes sure each sending account has the `share` tag and the `immich-handoff` workflow, and repairs the workflow if you change the URL or secret.
- **Typed Immich client.** The Immich API client is generated from Immich's published OpenAPI description (kept in `api/`), so an incompatible Immich change stops the build instead of failing quietly.

## Development

```
go test ./...        # unit tests, no Docker needed
test/run.sh          # end-to-end: starts a throwaway Immich 3.2.0, runs the real app against it, tears it down
```

`test/run.sh` creates three users with the exact permissions listed above and checks the whole flow: workflow creation, a tag arriving through the real Immich workflow, duplicates, a photo whose copy is in the family trash, and the `inbox` tidy-up. `keep=1 test/run.sh` leaves Immich running afterwards, and `test/dev.sh` then starts the app against it. `IMMICH_VERSION=<tag> test/run.sh` tests another Immich release.

## Background

immich-handoff is based on the idea of immich_family_sync.

## License

MIT, see [LICENSE](LICENSE).
