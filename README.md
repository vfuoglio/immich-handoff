# immich-handoff

**Hand photos from personal Immich accounts over to a shared family account, just by tagging them.**

Everyone's phone backs up to their own Immich account. One shared account holds the family library, with its albums and timeline in order. With immich-handoff, a family member tags a photo `share` in the Immich app and the photo moves to the shared account within seconds. Nothing else to do, no second app.

It can do more than one family account: several services, several receivers, people joining by themselves, services anyone can offer, invitation links, move or copy. Those are optional and described [further down](#going-further-optional). The basic install needs nothing but the steps below.

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

Each tagged release is also built as a ready-made image, `ghcr.io/vfuoglio/immich-handoff:<version>` (amd64 and arm64, so it runs on most NAS models). To use it instead of building, replace `build: .` in `docker-compose.yml` with `image: ghcr.io/vfuoglio/immich-handoff:<version>`.

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

## The web page

Open `http://<host>:8080/` to see the services you take part in, how many photos are waiting (if you send) or sit in your inbox (if you receive), and the result of the last run. It updates by itself when a run finishes, has a **Sync now** button, works on a phone and follows your device's light or dark setting.

It uses your Immich login, so it only works when it is served on the **same hostname** as Immich (any port). The usual way is a reverse proxy that forwards a path such as `/handoff/` on your Immich address to port 8080; keep the trailing slash in the address. Anyone signed in to Immich can open the page, but only people who take part in a service see its activity. Put it behind HTTPS if it is reachable from outside your home network.

To show small thumbnails of the photos waiting to be handed over, set `PUBLIC_IMMICH_URL` to the address of Immich as your browser sees it (for example `https://photos.example.com`).

## Going further (optional)

Everything in this part is optional. A plain install already does what the first sections describe.

### Several services and receivers

A plain install is one service: everyone's `share` photos go to one family account. To run more than one, or to send the same photo to several accounts, describe them in a routes file:

1. Copy `routes.example.yaml` to `routes.yaml` and edit it. It names your accounts (keys come from `.env` variables, so no secret is written in the file) and lists services. Each service has a **tag**, the **senders** who use it and the **receivers** who get the photos. A service can also set `mode: copy` to keep the senders' originals.
2. In `.env` set `ROUTES_FILE=/routes.yaml` and add the key variables the file refers to (for example `ALICE_KEY=...`).
3. In `docker-compose.yml` uncomment the `routes.yaml` volume line, then `docker compose up -d`.

With several receivers on one service, the original is removed only after **every** receiver holds a verified copy. If one receiver refuses (its trash holds that photo) or fails, the original stays and nothing is lost. An account that sends in one service and receives in another needs both permission lists from the table above. Each service gets its own workflow in the sender's account, named `immich-handoff: <service>`.

### Letting people join by themselves

By default a sender exists because an admin put their API key in `.env`. You can instead let people add themselves from the web page:

1. Set `HANDOFF_STORE_KEY` in `.env` to a long random secret (`openssl rand -hex 24`). It encrypts the API keys the app keeps; **keep a copy of it**, because without it the saved keys cannot be read and people would have to join again. (`HANDOFF_STORE_KEY_FILE` reads it from a Docker secret instead.)
2. Allow joining on the service: `ALLOW_JOIN=true` in `.env` for a plain install, or `allow_join: true` on a service in the routes file. Such a service may start with no senders at all.
3. `docker compose up -d`. The compose file keeps the saved memberships in the `handoff-data` volume; include that volume in your [backups](#backups-and-upgrades).

Anyone signed in to Immich then sees **Services you can send to** on the web page. **Join** asks them to confirm, then the app creates an API key named `handoff: <service>` in their own Immich account (with only the permissions a sender needs), creates their tag and workflow, and starts handing over the photos they tag. Nobody, including the person who joined, ever sees the key; it is stored encrypted. **Leave** deletes the key and the workflow again. A person can also end it any time by deleting that key in Immich (Account Settings -> API Keys).

If someone joins while Immich is still starting up, they are added straight away and the workflow is finished in the background once Immich is ready.

### Offering your own service

With `HANDOFF_STORE_KEY` set, anyone signed in to Immich can also **offer a service** from the web page. They choose a name, the tag senders will use, who can **find** it (everyone on this Immich server, or only people they send a link to) and who may **send** (people they approve, or anyone on this Immich server). The person offering is the receiver: the photos land in their account. With only `HANDOFF_STORE_KEY` set and no keys at all, everything is created from the web page.

1. The owner presses **Create service**. The app makes a receiver key in the owner's account and keeps it encrypted.
2. Others see the service under **Services you can send to**. For a service that needs approval they press **Ask to join**; the owner sees the request and presses **Approve** or **Decline**.
3. After approval the sender presses **Activate**. (The app can only create a key in your account while you are signed in, which is why the sender has the last step.) For an open service, senders just press **Join**.
4. A sender may not use the same tag for two services.

#### Move or copy

The owner chooses what happens to the senders' originals when the service is created.

- **Move** (the default) sends the original to the sender's trash once every receiver has a verified copy.
- **Copy** keeps it: the original loses the service tag and gets the tag `handed-over` (`SENT_TAG`) instead, so its owner can see it was shared and it is not sent twice. The key the app makes for such a sender has **no permission to delete anything**.

The page tells a sender which one applies before they join. A service's mode cannot be changed later. The create form also lets the owner choose the inbox tag and how many days an unfiled photo keeps it.

#### Invitation-only services

An invitation-only service does not appear in anyone's list. The owner's card shows an **invitation link**; whoever opens it (signed in to Immich) lands on a page for exactly that service, with the same Join or Ask to join button. **New link** makes a fresh link and the old one stops working at once. **List for everyone** and **Make invitation-only** switch a service between the two. People already approved or joined are not affected by a new link. The link is only as private as the way you share it.

#### Ending a connection, and deleted keys

Neither side ever sees the other's key, or their own. Either side can end it: the sender with **Leave**, the owner with **Delete service**. If an owner deletes a service, its senders see **Removed by its owner** and press **Clean up** to remove their own key and workflow.

Either side can also delete the key named `handoff: ...` in Immich (Account Settings -> API Keys). The app notices on the next tagged photo or at the daily check, pauses that connection and stops using it, tells both sides on the web page (and through `NOTIFY_URL` if set), and does not touch any photo: originals stay where they are until the connection works again.

- A **sender** whose key was deleted sees **Connection lost** and presses **Clean up**, then joins again. Photos they tagged meanwhile are handed over once they have.
- An **owner** whose receiver key was deleted sees **Disconnected** and presses **Reconnect**, which makes a new receiver key. Senders resume by themselves.

The page also lists recent events (joined, left, asked, approved, connection lost, reconnected) for the services you take part in.

## Settings

Everything is set in `.env`. The keys and `WEBHOOK_SECRET` are required; the rest are optional.

| Setting | Default | Meaning |
|---|---|---|
| `IMMICH_SOURCE_API_KEYS` | | API keys of the sending accounts, comma separated (not needed with a routes file) |
| `IMMICH_DEST_API_KEY` | | API key of the family account (not needed with a routes file) |
| `WEBHOOK_SECRET` | | Shared secret Immich sends with every event |
| `PUBLIC_WEBHOOK_URL` | `http://immich-handoff:8080/webhook/immich` | Address the Immich **server** uses to reach this app |
| `IMMICH_BASE_URL` | `http://immich-server:2283` | Address of Immich as seen from this container |
| `IMMICH_NETWORK` | `immich_default` | Docker network Immich runs on |
| `PUBLIC_IMMICH_URL` | | Address of Immich as your browser sees it; switches on thumbnails |
| `ROUTES_FILE` | | Path of a routes file for several services |
| `HANDOFF_STORE_KEY` | | Switches on joining and offering services from the web page; encrypts the saved API keys |
| `ALLOW_JOIN` | `false` | Plain install: let people join the default service from the web page |
| `DATA_DIR` | `/data` | Where the saved memberships live |
| `SHARE_TAG` | `share` | Tag people add to send a photo |
| `INBOX_TAG` | `inbox` | Tag on new photos in the receiving account |
| `REFUSED_TAG` | `share-duplicate` | Tag put on an original that could not be handed over |
| `SENT_TAG` | `handed-over` | Copy mode: tag put on an original after it was handed over |
| `INBOX_EXPIRY_DAYS` | `30` | Days before an unfiled photo loses the `inbox` tag |
| `HANDOFF_MODE` | `move` | Default for services from `.env` or the routes file: `copy` keeps the senders' originals |
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
| "the store key does not match this database" | `HANDOFF_STORE_KEY` was changed. Put the original back, or delete the `handoff-data` volume and let people join again. |
| `HTTP 401` or `HTTP 403` in the log | An API key is missing a permission from the table above. Create a new key with the full list. |
| No `workflow ready` line | Immich needs about a minute after it starts before workflows can be created; the app keeps retrying. If it never appears, check the key permissions and that Immich is 3.2.0 or newer. |
| Tagged a photo, nothing happens | In Immich open that account's workflow named `immich-handoff` and look at its runs. **No runs:** the tag was not `share`, or tags are not switched on for the account. **Failed runs:** Immich cannot reach `PUBLIC_WEBHOOK_URL`; it must be an address the Immich server container can reach. |
| Log says photos were found "by the daily sweep instead of by webhook" | Same as above: the notifications are not arriving. The photos still move, but only once a day. |
| `IMMICH_NETWORK` error on start | The network name is wrong. Run `docker network ls` and put Immich's network in `.env`. |
| A photo is tagged `share-duplicate` | See "Day to day" above. |
| The original is still in the sender's account after tagging | The hand-over failed or could not be verified, so the original was kept on purpose. The log has an `ERROR` line saying why; fix the cause, and the photo is retried at the next run. |
| A tag you just added is gone | Immich occasionally drops a tag that is added in the first moments after a photo was uploaded, while it is still processing the photo. This happens inside Immich (we saw about 1 in 50 in tests with this app switched off). Add the tag again. |
| The page says "Connection lost" or "Paused" | An API key named `handoff: ...` was deleted in Immich. Follow the button on the page (Clean up for a sender, Reconnect for an owner). |
| The page says "Sign in to Immich first" | Serve it on the same hostname as Immich (see [The web page](#the-web-page)). |
| "too many changes in a minute" | The page limits each person to 30 changes a minute. Wait a moment. |

## Security

- **What a sender's key can do.** An Immich key cannot be limited to one tag, so a sender's key can read, download and (in move mode) delete any photo in that account. The app only ever acts on photos tagged for a service, and the key is never shown to anyone. In copy mode the key has no delete permission at all.
- **What is stored.** With self-service on, one SQLite file in `/data` lists memberships; every API key in it is encrypted (AES-256-GCM, key from `HANDOFF_STORE_KEY`). Whoever runs the container and holds that secret can read those keys, in the same way the person running Immich can already read the photos. Without self-service nothing is stored.
- **Who can do what.** Immich events carry the shared `WEBHOOK_SECRET`. The web page accepts only people signed in to Immich; every change needs a header a web page on another site cannot send, and each person is limited to 30 changes a minute. Pages are served with a strict content security policy and cannot be framed.
- **No photo is removed without a copy.** The original is touched last, only after every receiver holds a verified copy, and never while a connection is broken.
- **Metrics.** `GET /metrics` (counters in Prometheus text format) needs the same secret in the `X-Webhook-Secret` header.

## Backups and upgrades

Only self-service installs keep data: the `handoff-data` volume (the volume name starts with the folder name; see `docker volume ls`). Back it up while the app is stopped or idle, and keep a copy of `HANDOFF_STORE_KEY` separately, because the saved keys cannot be read without it:

```
docker compose stop immich-handoff
docker run --rm -v immich-handoff_handoff-data:/data -v "$PWD":/backup busybox tar czf /backup/handoff-data.tgz -C /data .
docker compose start immich-handoff
```

To restore, put the archive back the same way (`tar xzf` into an empty volume) before starting the app.

To upgrade, back up as above, then `git pull && docker compose up -d --build` (or point `image:` at the new version). The data format upgrades itself on start. Going back to an older version after that is not supported; the older version refuses to open a newer database. `IMMICH_VERSION=<tag> test/run.sh` runs the whole test suite against a newer Immich before you upgrade Immich itself.

## Architecture

```
 Immich app ──tag "share"──►  Immich server
                                   │  workflow in the sender's account: "tag added" + "tag is share"
                                   ▼  POST /webhook/immich  (with the secret)
                            ┌───────────────┐
                            │ immich-handoff│── Immich API ──► sender's account    (find, download, trash or mark the original)
                            │   (one Go     │── Immich API ──► receiving account   (upload, tag inbox, tidy tags)
                            │   container)  │── /data/handoff.db (optional, encrypted keys)
                            └───────────────┘
 Browser ── Immich login cookie ──► web page (same hostname as Immich) ── Join / Offer / Approve / Leave
```

- **One small container.** Without self-service nothing is stored: all state lives in Immich (the tags and the workflows) and the last-run summary is held in memory.
- **Events only wake the app.** When Immich reports a tag, the app asks Immich which photos carry the service tag and handles those. A lost or repeated event therefore cannot lose or duplicate a photo. Bursts of events are combined into one run, and an announced photo whose tag is not yet visible through Immich's API is retried for a short while.
- **Safety net.** One run at startup and one a day (`SWEEP_INTERVAL_HOURS`) pick up anything missed while the app was down, check that stored keys still work, and do the `inbox` tidy-up.
- **Streaming copy.** A photo is streamed straight from the sender's account into the receiving account, never stored on the app's disk. The app computes its SHA-1 on the way and compares it with what Immich stored.
- **Tag changes are verified.** Immich can drop a tag changed right after another change to the same photo, so the app checks each change took effect, and adds a new tag before removing the old one.
- **Self-configuring.** At startup, and again at every daily run, the app makes sure each sender has the service's tag and the `immich-handoff` workflow, and repairs the workflow if you change the URL or secret. Immich reports healthy about half a minute before its workflow plugins load, so this is retried until it works.
- **Typed everywhere.** The Immich API client is generated from Immich's published OpenAPI description (kept in `api/`), and the web pages are type-checked templates, so an incompatible Immich change or a template mistake stops the build instead of failing quietly.

## Development

```
go test ./...        # unit tests, no Docker needed
test/run.sh          # end-to-end: starts a throwaway Immich 3.2.0, runs the real app against it, tears it down
```

`test/run.sh` creates users with the exact permissions listed above and checks the whole flow against a real Immich: workflow creation, a tag arriving through the real workflow, duplicates, trashed copies, the `inbox` tidy-up, one-to-many routes, joining and leaving, offering services with approval and invitation links, copy mode, and keys deleted on either side. `keep=1 test/run.sh` leaves Immich running afterwards, and `test/dev.sh` then starts the app against it. `E2E_PORT` and `E2E_APP_PORT` move the test ports if the defaults are taken. Generated code (`go generate ./...`) is committed; CI fails if it is stale. Notes on the Immich behaviour this project relies on are in [docs/immich-api-notes.md](docs/immich-api-notes.md).

## Background

immich-handoff is based on the idea of immich_family_sync.

## License

MIT, see [LICENSE](LICENSE).
