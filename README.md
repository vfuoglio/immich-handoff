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

## Letting people join by themselves (optional)

By default a sender exists because an admin put their API key in `.env`. You can instead let people add themselves from the web page:

1. Set `HANDOFF_STORE_KEY` in `.env` to a long random secret (`openssl rand -hex 24`). It encrypts the API keys the app keeps; **keep a copy of it**, because without it the saved keys cannot be read and people would have to join again. (`HANDOFF_STORE_KEY_FILE` reads it from a Docker secret instead.)
2. Allow joining on the service: `ALLOW_JOIN=true` in `.env` for a plain install, or `allow_join: true` on a service in the routes file. Such a service may start with no senders at all.
3. `docker compose up -d`. The compose file keeps the saved memberships in the `handoff-data` volume; include that volume in your backups.

Anyone signed in to Immich then sees **Services you can send to** on the web page. **Join** asks them to confirm, then the app creates an API key named `handoff: <service>` in their own Immich account (with only the permissions a sender needs), creates their tag and workflow, and starts handing over the photos they tag. Nobody, including the person who joined, ever sees the key; it is stored encrypted. **Leave** deletes the key and the workflow again. A person can also end it any time by deleting that key in Immich (Account Settings -> API Keys).

If someone joins while Immich is still starting up, they are added straight away and the workflow is finished in the background once Immich is ready.

## Offering your own service (optional)

With `HANDOFF_STORE_KEY` set, anyone signed in to Immich can also **offer a service** from the web page: a name, the tag senders will use, who can **find** it (**everyone on this Immich server**, or **only people I send a link to**) and who may **send** (**people I approve** or **anyone on this Immich server**). The person offering is the receiver: the photos land in their account.

1. The owner presses **Create service**. The app makes a receiver key in the owner's account and keeps it encrypted.
2. Others see the service under **Services you can send to**. For a service that needs approval they press **Ask to join**; the owner sees the request and presses **Approve** or **Decline**.
3. After approval the sender presses **Activate**. (The app can only create a key in your account while you are signed in, which is why the sender has the last step.) For an open service, senders just press **Join**.
4. A sender may not use the same tag for two services.

**Invitation-only services** do not appear in anyone's list. The owner's card shows an **invitation link**; whoever opens it (signed in to Immich) lands on a page for exactly that service, with the same Join or Ask to join button. **New link** makes a fresh link and the old one stops working at once; **List for everyone** or **Make invitation-only** switch a service between the two. People already approved or joined are not affected by a new link. The link is only as private as the way you share it.

Neither side ever sees the other's key, or their own. Either side can end it: the sender with **Leave**, the owner with **Delete service**, or either by deleting the key named `handoff: ...` in Immich (Account Settings -> API Keys), which stops the hand-over. If an owner deletes a service, its senders see **Removed by its owner** and press **Clean up** to remove their own key and workflow.

**If a key is deleted in Immich** (by the sender, or by the owner), the app notices on the next tagged photo or at the daily check, pauses that connection and stops using it, tells both sides on the web page (and through `NOTIFY_URL` if set), and does not touch any photo: originals stay where they are until the connection works again. A sender whose key was deleted sees **Connection lost** and presses **Clean up**, then joins again; photos they tagged meanwhile are handed over once they have. An owner whose receiver key was deleted sees **Disconnected** and presses **Reconnect**, which makes a new receiver key; senders resume by themselves. The page also lists recent events (joined, left, asked, approved, connection lost, reconnected) for the services you take part in.

A plain `.env` install needs no service in the configuration for this: with only `HANDOFF_STORE_KEY` set and no keys, everything is created from the web page.

## The web page

Open `http://<host>:8080/` to see the services you take part in, how many photos are waiting (if you send) or sit in your inbox (if you receive), and the result of the last run. It updates by itself when a run finishes, and has a **Sync now** button. It works on a phone and follows your device's light or dark setting.

It uses your Immich login, so it only works when it is served on the **same hostname** as Immich (any port). The usual way is a reverse proxy that forwards a path such as `/handoff/` on your Immich address to port 8080; keep the trailing slash in the address. Only accounts that take part in a service are let in.

## Settings

Everything is set in `.env`. The keys and `WEBHOOK_SECRET` are required; the rest are optional.

| Setting | Default | Meaning |
|---|---|---|
| `IMMICH_SOURCE_API_KEYS` | | API keys of the sending accounts, comma separated (not needed with a routes file) |
| `IMMICH_DEST_API_KEY` | | API key of the family account (not needed with a routes file) |
| `ROUTES_FILE` | | Path of a routes file for several services (see above) |
| `HANDOFF_STORE_KEY` | | Switches on joining from the web page; encrypts the saved API keys |
| `ALLOW_JOIN` | `false` | Plain install: let people join the default service from the web page |
| `DATA_DIR` | `/data` | Where the saved memberships live |
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

- **One small container.** Without self-service joining nothing is stored: all state lives in Immich (the tags and the workflow) and the last-run summary is held in memory. With it, the app keeps one small SQLite file (`/data/handoff.db`) listing who joined what, with each API key encrypted (AES-256-GCM, key from `HANDOFF_STORE_KEY`). Whoever runs the container and holds that secret can read those keys, like the Immich admin can already read the photos.
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
