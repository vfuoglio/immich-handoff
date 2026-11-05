# Troubleshooting

Start with the app's log: `docker compose logs --tail 100`. Then find your symptom.

## Installing and starting

| Symptom | Likely cause and fix |
|---|---|
| The app stops right away mentioning `WEBHOOK_SECRET` or `HANDOFF_STORE_KEY` | The value is missing or shorter than 16 characters. Set a long random one in `.env` (`openssl rand -hex 24`). |
| "the store key does not match this database" | `HANDOFF_STORE_KEY` was changed. Put the original back, or delete the `handoff-data` volume and let people join again. |
| An error about `IMMICH_NETWORK` or a missing network | The network name is wrong. Run `docker network ls` and put Immich's network in `.env`. |
| `HTTP 401` or `HTTP 403` in the log | A fixed-account API key is missing a permission. See the table in [Fixed accounts](fixed-accounts.md) and create a new key with the full list. |
| No `workflow ready` line | Immich needs about a minute after it starts before workflows can be created; the app keeps retrying. If it never appears, check key permissions and that Immich is 3.2.0 or newer. |

## Photos are not handed over

| Symptom | Likely cause and fix |
|---|---|
| A person cannot add tags, or cannot see the inbox tag | **Tags** is switched off for their Immich account. Each person turns it on for themselves: Account Settings -> Features -> Tags, then reload the page or restart the app. Photos are still handed over without it; the person just cannot see or use the tags. |
| Tagged a photo, nothing happens | In Immich open that account's workflow named `immich-handoff` (or `immich-handoff: <service>`) and look at its runs. **No runs:** the tag was not the service's tag, or Tags are not switched on for the account. **Failed runs:** Immich cannot reach `PUBLIC_WEBHOOK_URL`; it must be an address the Immich *server* container can reach. |
| The log says photos were found "by the daily sweep instead of by webhook" | Same as above: Immich's notices are not arriving. The photos still move, but only once a day. |
| A tag you just added is gone | Immich occasionally drops a tag that is added in the first moments after a photo was uploaded, while it is still processing the photo. This happens inside Immich (about 1 in 50 in tests with this app switched off). Add the tag again. |
| A photo is tagged `share-duplicate` | The receiver already has that photo in their **trash**, so it was not handed over and the original was kept. Once they empty their trash, add the service's tag again. |
| The original is still in the sender's account after tagging | If the sender chose **keep my originals**, that is correct. Otherwise the hand-over failed or could not be verified and the original was kept on purpose; the log has an `ERROR` line saying why, and the photo is retried at the next run. |
| A photo seems to be handled twice | It is not: Immich's search can keep listing a photo for a while after its tag was removed, and the app skips what it handled in the last two minutes. |

## The page

| Symptom | Likely cause and fix |
|---|---|
| "Sign in to Immich first" | Sign in to Immich in the same browser. If you already have, the page is not on the same hostname as Immich; see [Operations](operations.md#putting-the-page-behind-your-reverse-proxy). |
| Counts never load, or the page looks unstyled | Usually a reverse proxy that changes the path. Use the tested configuration in [Operations](operations.md#putting-the-page-behind-your-reverse-proxy), and keep the trailing slash in the address. |
| No thumbnails | Set `PUBLIC_IMMICH_URL` to Immich's address as your browser sees it. |
| The page does not update by itself | A proxy is holding the live-update stream. Turn off its buffering for the page (`proxy_buffering off` in nginx). |
| "too many changes in a minute" | The page limits each person to 30 changes a minute. Wait a moment. |
| "Connection lost" or "Paused" | An API key named `handoff: ...` was deleted in Immich. Follow the button on the page: **Clean up** for a sender, **Reconnect** for an owner. |
| "Removed by its owner" | The owner deleted the service. Press **Clean up** to remove your key and workflow. |
