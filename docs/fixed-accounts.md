# Fixed accounts

The usual way to run immich-handoff is to let people join and offer services from the web page ([Install](install.md)). This page is for the other way: **you**, the admin, create the API keys and decide who sends to whom, with nothing stored by the app. It suits a single household where you want everything written down in one file. You can use it alongside self-service.

## One service, keys in `.env`

### 1. Create API keys

In Immich, sign in to each account in turn and go to **Account Settings -> API Keys -> New API Key**. Create one key per account with these permissions and copy each key as you create it.

| Account | Permissions |
|---|---|
| Each person who **sends** photos | `user.read` `tag.read` `tag.create` `tag.asset` `asset.read` `asset.download` `asset.delete` `workflow.read` `workflow.create` `workflow.update` |
| The **receiving** account | `user.read` `tag.read` `tag.create` `tag.asset` `asset.read` `asset.upload` |
| The **receiving** account, if the service puts arrivals in an album (`album`, see below) | the above, plus `album.read` `album.create` `albumAsset.create` |

The sender list includes `asset.delete` so the service can move originals. For a service that only copies, leave it out.

### 2. Put them in `.env`

```
IMMICH_SOURCE_API_KEYS=key_for_person_1,key_for_person_2
IMMICH_DEST_API_KEY=key_for_the_receiving_account
WEBHOOK_SECRET=...          # as in the install guide
```

(`HANDOFF_STORE_KEY` is only needed if you also let people join from the page.) Then `docker compose up -d`. Within about a minute the log shows `workflow ready` for every sender: the app creates the `share` tag and the Immich workflow that notifies it, in each sending account, by itself.

Tag a photo `share` on a sending account; it appears in the receiving account tagged `inbox`.

### Move or copy

For fixed accounts the **admin** chooses, once, for everyone: `HANDOFF_MODE=copy` (the default) keeps the senders' originals, and `HANDOFF_MODE=move` sends them to the sender's trash once the receiver has a verified copy. (People who join from the page choose for themselves.) If you used an earlier version, which moved originals, set `HANDOFF_MODE=move` to keep that behaviour.

To rehearse without changing anything, set `DRY_RUN=true`; the log says what would happen.

## Several services and receivers: the routes file

To run more than one service, or to send the same photo to several accounts, describe them in a routes file:

1. Copy `routes.example.yaml` to `routes.yaml` and edit it. It names your accounts (keys come from `.env` variables, so no secret is written in the file) and lists services. Each service has a **tag**, the **senders** who use it and the **receivers** who get the photos. A service can set `mode: copy` or `mode: move`.
2. In `.env` set `ROUTES_FILE=/routes.yaml` and add the key variables the file refers to (for example `ALICE_KEY=...`).
3. In `docker-compose.yml` uncomment the `routes.yaml` volume line, then `docker compose up -d`.

With several receivers on one service, the original is touched only after **every** receiver holds a verified copy. If one receiver refuses (its trash holds that photo) or fails, the original stays and nothing is lost. An account that sends in one service and receives in another needs both permission lists above. Each service gets its own workflow in the sender's account, named `immich-handoff: <service>`.

## Letting people join a fixed service

Set `HANDOFF_STORE_KEY`, and `ALLOW_JOIN=true` (or `allow_join: true` on a service in the routes file). People then see the service on the page and join it like any other. A service defined this way may start with no senders at all.
