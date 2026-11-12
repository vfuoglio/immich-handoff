# Troubleshooting

Start with the setup check, then the app's log (`docker compose logs --tail 100`), then find your symptom below.

## Check your setup first

```
docker compose exec immich-handoff /immich-handoff doctor
```

It reads the settings the app is running with and checks each one against Immich. Every line is `ok`, `warn` (works, but worth a look) or `FAIL` (something will not work), and a failure says what to change. It covers:

- the secrets: `WEBHOOK_SECRET` and `HANDOFF_STORE_KEY` are long enough;
- `PUBLIC_WEBHOOK_URL`: set, a real address, not `localhost` (which for Immich's server means Immich itself), and ending in `/webhook/immich`;
- Immich: reachable at `IMMICH_BASE_URL` and version 3.2.0 or newer;
- notifications: where messages go (names and kinds only), and whether the settings are ones the app starts with; `doctor --send-test` also sends a test message to each destination and says how each answered;
- the webhook address: the check calls it the way Immich would and tells you whether it reaches this app and accepts the secret;
- every key, from the configuration and from people who joined: still accepted, and holding every permission it needs;
- whether **Tags** is switched on in each person's account;
- each sender's workflow: present, enabled, and pointing at the current address and secret;
- how Immich's most recent runs of that workflow ended. This is the proof that Immich can reach the app: a failed run means Immich could not deliver the notice. A workflow that has never run is fine; tag a photo and run the check again.

It only reads. It never changes Immich, the data file or a photo, and it exits with status 1 when anything failed (warnings do not count), so it can be used in a script. One thing it cannot do is send Immich a test message on demand, because Immich has no such call; the workflow runs stand in for it.


## Installing and starting

| Symptom | Likely cause and fix |
|---|---|
| The app stops right away mentioning `WEBHOOK_SECRET` or `HANDOFF_STORE_KEY` | The value is missing or shorter than 16 characters. Set a long random one in `.env` (`openssl rand -hex 24`). |
| The app stops with `the app is running and using this data folder` | Another copy of the app (an old container that was not stopped, or a second install pointing at the same volume) holds the data. Stop it first: two copies on one data file would corrupt it. |
| `backup` says the app is using the data but did not answer | The command could not reach the app at `LISTEN_ADDR`, or `WEBHOOK_SECRET` differs. Run it inside the app's container (`docker compose exec immich-handoff /immich-handoff backup`). |
| A backup stays queued for a long time | Hand-overs are running without a pause, and the backup waits for a quiet moment; after 4 hours it is made anyway and named `-forced`. See [Backups](operations.md#backups). |
| A notification never arrives | `immich-handoff doctor --send-test` (or **Send a test notification** on the administrator's page) sends one to each destination and says how each answered: `answered 401` is a wrong token or topic, `could not reach it` a wrong address or a network that blocks it. For one person's mail or device, use the test button on their own card. See [Notifications](operations.md#notifications). |
| The card for notifications on this device says it needs HTTPS, or the iPhone needs the Home Screen | Browsers allow notifications only on a page served over HTTPS (or `localhost`), and on an iPhone only for the page added to the Home Screen and opened from there. Notifications on devices also need `PUBLIC_APP_URL` (https) or `PUSH_CONTACT`. See [Notifications on devices](operations.md#notifications-on-devices). |
| A frame shows nothing, or a photo does not appear | The frame looks at its album every `FRAME_REFRESH` (two minutes); archived photos, videos and trashed photos are never shown. A frame that ended (its date passed) answers "not found" and waits in the owner's list to be removed. See [Frames](using.md#frames-an-album-on-a-screen). |
| Photos tagged in an event pool do not reach someone | Only people who joined **before** the photo was handed over receive it, a photo never goes back to its sender, and an ended pool hands nothing over. The pool's row says whether it ended. See [Event pool](using.md#event-pool-everybody-gets-everybodys-photos). |
| A box has no **Give this box an email address** button | The person who runs the app has not set `MAIL_IN_HOST` (see [Photos by email](operations.md#photos-by-email)), or the box has a password, which a mail cannot give. |
| Mailed photos do not arrive | Run `doctor`: it logs in to the mailbox and says what is wrong. Then check that the address is the box's exactly (`drop+code@...`), that the mailbox accepts plus-addresses, that the photos are attached (not pasted into the text) and larger than 8 KB, that the mail is under `MAIL_IN_MAX_MB`, and the box's log (open, not paused, not full). Mail takes up to `MAIL_IN_EVERY` to be noticed. |
| The administrator's page says that the last backup failed | The reason follows. Usually the backup folder (`/data/backups`, or `BACKUP_DIR`) is full, missing or not writable by the app. The app tries again an hour later, and `NOTIFY_URL` was told. See [Automatic backups](operations.md#automatic-backups). |
| There is no **Download backup** button on the administrator's page | It is off by default. See [Download a backup from the administrator's page](operations.md#download-a-backup-from-the-administrators-page). Only the exact value `true` switches it on, and the app needs `HANDOFF_STORE_KEY`; `doctor` says which. |
| "the store key does not match this database" | `HANDOFF_STORE_KEY` was changed. Put the original back, or delete the `handoff-data` volume and let people join again. |
| An error about `IMMICH_NETWORK` or a missing network | The network name is wrong. Run `docker network ls` and put Immich's network in `.env`. |
| `HTTP 401` or `HTTP 403` in the log | A fixed-account API key is missing a permission. See the table in [Fixed accounts](fixed-accounts.md) and create a new key with the full list. |
| No `workflow ready` line | Immich needs about a minute after it starts before workflows can be created; the app keeps retrying. If it never appears, check key permissions and that Immich is 3.2.0 or newer. |

## Photos are not handed over

| Symptom | Likely cause and fix |
|---|---|
| A big video is not handed over and the log says `transfer given up: no data moved for too long` | No data moved for three minutes, between the two Immich servers or on the way to one of them: a stalled connection, or an Immich that stopped answering. A transfer that keeps moving is never cut off, however long it takes. The original is kept and the next pass tries again. Check that both Immich servers are reachable from the app and not overloaded. |
| Photos in a linked album are not handed over | They are looked at every 15 minutes (`ALBUM_CHECK_MINUTES`), so wait that long. A photo is skipped if it is not yours (a shared album), is in the trash, or already carries the `handed-over` or `share-duplicate` tag (remove that tag to hand it over again). If the log says `cannot read the linked album`, the album was deleted or the key lost `album.read`: link the album again from the service's card. |
| A service has an album but photos arrive tagged inbox | The album could not be used, so the photo was tagged instead. The log says `could not put the copy in the album` and a notification is sent once. The usual cause is a receiving key without `album.read`, `album.create` and `albumAsset.create`. For a routes file or `.env`, give the key those permissions. A service made on the page gets a key that has them, so there someone changed the key's permissions in Immich: give them back, or delete the service and create it again. |
| A person cannot add tags, or cannot see the inbox tag | **Tags** is switched off for their Immich account. The page shows a **Turn on Tags** notice with a button for it; they can also do it in Immich: Account Settings -> Features -> Tags. Switching it on is not enough: they must also refresh the Immich tab or window (on the phone, close and reopen the app). If Tags is already on and the options are still missing, this refresh is the usual cause. Photos are still handed over without it; the person just cannot see or use the tags. |
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
