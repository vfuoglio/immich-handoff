# How it works inside

For the curious, and for anyone changing the code.

![Tag, notify, arrive](images/flow.svg)

```
 Immich app ──tag──►  Immich server
                           │  a workflow in the sender's account: "tag added" + "tag is <service tag>"
                           ▼  POST /webhook/immich  (with the secret)
                    ┌───────────────┐
                    │ immich-handoff│── Immich API ──► sender's account    (find, download, mark or trash the original)
                    │ (one Go       │── Immich API ──► receiving account   (upload, tag inbox, tidy tags)
                    │  container)   │── /data/handoff.db (encrypted keys, memberships, activity)
                    └───────────────┘
 Browser ── Immich login cookie ──► the page (same hostname as Immich) ── join / offer / approve / leave
```

## The ideas that hold it together

- **Events only wake the app.** When Immich reports a tag, the app asks Immich which photos carry the service's tag and handles those, using the sender's own key. A lost or repeated event cannot lose or duplicate a photo. Bursts are combined into one run, and an announced photo whose tag is not yet visible through Immich's API is retried for a short while.
- **Events do the work; two timers cover what events cannot.** Immich sends no event when a photo is added to an album, so a short timer (`CLEANUP_INTERVAL_MINUTES`, default 15) only removes inbox tags from filed or expired photos. It does nothing else, and a pass in progress makes it skip a round.
- **A third timer, for linked albums.** A sender can link an album to a service so everything in it is handed over (stored on their membership; their key gets `album.read`). Immich has no album event either, so `ALBUM_CHECK_MINUTES` (default 15) starts a pass when any album is linked, and every other pass includes the albums too. A photo in an album is "pending" if the sender owns it, it is not trashed and it has neither the service's sent nor refused tag; those two tags are the only memory, so nothing about handled photos is stored.
- **A safety net, not polling.** One run at startup and one a day (`SWEEP_INTERVAL_HOURS`) pick up anything missed while the app was down, check that stored keys still work, and tidy the inbox tags. A pass also runs as soon as workflows exist after a delayed setup.
- **Streaming copy.** A photo is streamed straight from the sender's account into the receiver's, never stored on the app's disk. The app computes its SHA-1 on the way and compares it with what Immich stored.
- **The original is touched last,** only after every receiver holds a verified copy, and never while a connection is broken or a service has no healthy receiver. In move mode it goes to the trash; in copy mode it gets the sent tag and loses the service tag.
- **The sender's choice lives with the sender.** Each membership stores move or copy, and the sender's key is created with or without delete permission to match.
- **Tag changes are verified.** Immich can drop a tag changed right after another change to the same photo, so the app checks each change took effect and adds a new tag before removing the old one. Photos handled in the last two minutes are skipped, because Immich's search lags behind tag removals.
- **Self-configuring.** At startup and every daily run the app makes sure each sender has the service's tag and workflow, and repairs the workflow if the URL or secret changes. Immich reports healthy about half a minute before its workflow plugins load, so this retries until it works.
- **Keys are made by the person they belong to.** Joining and offering use the person's own Immich session to create a named, restricted API key, so the app never handles anyone's password.
- **Typed everywhere.** The Immich API client is generated from Immich's published OpenAPI description (kept in `api/`), and the web pages are type-checked templates, so an incompatible Immich change or a template mistake stops the build instead of failing quietly.

## The pieces

| Folder | What it does |
|---|---|
| `cmd/immich-handoff` | Starts everything from the environment. |
| `internal/syncer` | The engine: passes, hand-overs, tag cleanup, workflow setup. |
| `internal/hub` | People and services: joining, offering, approval, invitations, key health, the administrator's overview and removals. |
| `internal/bridge` | The receiver on another Immich server: the client that refuses addresses it may not use, the checks of a server and its key, and the thin hand-over that needs only upload and tag. See [bridges-design.md](bridges-design.md). |
| `internal/doctor` | The setup check behind `immich-handoff doctor` and the admin page's **Check now**. |
| `internal/store`, `internal/vault` | The SQLite file and the encryption of keys in it. |
| `internal/immich` | The generated client plus a small wrapper. |
| `internal/web` | The page: templates, handlers, styles, live updates. Its scripts are TypeScript in `internal/web/ts`, bundled into `static/app.js` (see below). |
| `internal/config` | Environment and routes file. |
| `internal/hub/people.go` | Mail to the people who asked: the audience of a line of the activity log (same rule as the Activity list), the per-person wait and hourly cap, the mail in the person's language, and the signed unsubscribe link. |
| `internal/hub/dropgallery.go`, `internal/web/gallery.go`, `ts/gallery.ts` | The guests' gallery of a drop box (an event): the box a link shows as a gallery and for how long (`GalleryBox`: not paused or revoked, open or inside its retention), the feed of photos by their place in the box's list with hearts and names (`GalleryFeed`, `dropbox_assets.guest_name`, `dropbox_hearts` keyed by a hash of the guest's cookie), pictures through the wall's cache (`GalleryImage`), originals streamed with at most eight at once (`GalleryOriginal`), and the page script that fills the grid, polls for new photos and gives hearts. |
| `internal/mailin` | Reading a mailbox for photos mailed to a drop box: a small IMAP client (TLS or STARTTLS, login, search unseen, size, fetch, mark, expunge; standard library only), the reading of a message (the box code from the address it was delivered to, the files in its parts, with limits on depth, parts and bytes), and `mailtest`, a fake IMAP server the tests use. `internal/hub/mailin.go` hands each file to the box as an upload. |
| `internal/hub` pools | An event pool is a service whose members' accounts are in both the sender and the receiver list (`Hub.Reload`); the syncer leaves out a photo's own sender when it hands over (`ingest`), tags each copy `from/<sender>` (`handOne`, name from `immich.FromTag`), and does nothing once `Service.Ended`. |
| `internal/push` | Web Push with the standard library only: message encryption (RFC 8291, tested against the RFC's own example), the signed token that identifies the app to a push service (VAPID, RFC 8292), the list of push services that may be called, and the request. |
| `internal/hub/push.go` | Notifications on devices: who hears a line (the same rule as the activity list), the ten-second gathering, the devices a person has, arrival summaries. The service worker is `ts/sw.ts`; the card's script is `ts/push.ts`. |
| `internal/notify` | The one way the app tells whoever runs it: a queue, a background sender, the ntfy, JSON and email formats (SMTP over TLS or STARTTLS, grouped into one mail per window), retries, and the destinations from `NOTIFY_URL` and `NOTIFY_FILE`. The rest of the app holds a `notify.Func` and sends a kind and a text. |
| `test/` | The end-to-end suite and the throwaway Immich it runs against. |
| `tools/typecheck` | Type-checks the page's TypeScript. Used by CI only. |
| `tools/screenshots` | Makes the pictures in `docs/images` from a real running demo. |

## Testing

```
go test ./...        # unit tests, no Docker needed
test/run.sh          # end-to-end: starts a throwaway Immich 3.2.0, runs the real app against it, tears it down
```

`test/run.sh` runs the whole suite one test after another (about five minutes); `test/run.sh browser`, `core`, `hub` or `bridge` runs one of four groups of about the same length ([`test/groups.sh`](../test/groups.sh)), which is what CI does, all four at the same time on separate machines, each with its own Immich, so a pull request waits for the longest group (about two minutes) and not for the sum. The `bridge` group starts a second Immich of its own (another compose project, on `E2E_FAR_PORT`, default `E2E_PORT` + 5) to be the receiver's server, stops it to lose the connection and deletes its key to revoke it. `.github/scripts/e2e_groups_test.sh` fails when a test is in no group or in two, so a new test cannot be left out of CI. `test/run-parallel.sh` runs the groups together on one machine; it is only faster if the machine has CPU to spare (five Immich stacks and a browser are heavy). `test/run.sh` creates users with the exact API-key permissions the docs list and checks the whole flow against a real Immich: workflow creation, a tag arriving through the real workflow, duplicates, trashed copies, the inbox tidy-up, one-to-many routes, joining and leaving, offering services with approval and invitation links, move and copy, tagging the way the phone app does, and keys deleted on either side. `keep=1 test/run.sh` leaves Immich running afterwards, and `test/dev.sh` starts the app against it. `IMMICH_VERSION=<tag> test/run.sh` tests another Immich release. `E2E_PORT` and `E2E_APP_PORT` move the test ports if the defaults are taken.

The end-to-end tests that need something besides Immich start it themselves: `internal/mailin/mailtest` is a small IMAP server (used by the unit tests, the hub tests and `TestPhotosByEmail`), `TestMail*` and `TestPush*` use an in-process SMTP sink and a made-up push endpoint (nothing is ever sent to a real mail or push service from a test), and `TestBridge` starts a second Immich. Each feature has an end-to-end test of its own: `TestMailToPeople` (email to people and action links), `TestPushDevices`, `TestWeeklyDigest`, `TestFrame` (in headless Chrome), `TestPhotosByEmail` and `TestEventPool` (three real accounts). A throwaway test against a real Chrome and Google's push service was used once to check that a notification with buttons arrives; it is not part of the suite, since it needs the internet.

`TestBrowser` (`test/e2e/browser_test.go`) is part of that run and drives the real page in headless Chrome or Chromium (chromedp, no Node) against the real app and Immich, as a person would: the bar with the icon, tabs, search, theme and sound; the fonts and the palette in light and dark; the drifting light and its stop for reduced motion; cards that tilt toward a mouse and not for a touch screen; the tabs and the address; rows that open in place; the command palette; the question dialog; approving a request; the live scene following a real hand-over (and staying still for reduced motion); no sideways scroll on a phone, a tablet and a desktop; the language menu; and that an idle page makes no requests. It holds the page to the agreed design as well as to working. It finds the browser on its own (`CHROME=/path` names one); without a browser it is skipped on a developer's machine and fails in CI, where one is installed. It needs no browser to build or run the app.

Generated code (`go generate ./...`: the Immich client, the templates and the page's script bundle) is committed; CI fails if it is stale.

### Live events

An open page listens to `GET /events` (server-sent events). Besides the old bare `run` signal (a pass finished, so the page refreshes its numbers), the stream carries typed JSON events, `event: live`, of what is happening. `internal/live` makes and routes them; `internal/web/ts/live.ts` has the matching TypeScript types and a parser that ignores anything malformed, and `app.ts` re-sends each one as a `handoff:live` event on the page for whatever listens (the live scene, later).

| `kind` | When | Other fields |
|---|---|---|
| `started` | a photo is being handed over | `service`, `sender`, `count` |
| `handed` | it was handed over | `reason: "duplicate"` when the receiver already had it |
| `refused` | the receiver has it in their trash; the original was kept | |
| `failed` | it could not be handed over; the original was kept and it is retried | |
| `held` | photos wait for the daily limit, still tagged | `reason: "limit"`, `count` waiting, `until` when they can go |
| `limit` | a sender reached the daily limit | `count`, `until` |
| `paused`, `resumed` | a service (`reason: "service"`) or a sender (`reason: "sender"`) paused or resumed | `sender` for a sender |
| `lost` | a connection was cut: a sender's key (`reason: "sender"`) or a service's receiver key (`reason: "service"`) | |
| `restored` | an owner reconnected a service | |
| `resync` | some events were dropped for a slow page: ask for the current state again | |

Every event has `at`. **Who hears what:** an event goes to the sender it is about, the owner of the service and whoever receives from it, and to an Immich administrator (who hears everything); never to anyone else, so a sender never hears another sender's photos. **What is in an event:** no photo, no file name, no id, no key, no email; only the service, the sender's name and a count. The routing (who may hear) is kept on the server and is not part of the event. **Bursts:** the per-photo kinds (`started`, `handed`, `refused`, `failed`) are gathered for 300 ms and sent as one event with a `count`, so a big upload makes a few events, not hundreds. A pause is told when it happens (`paused`/`resumed`), not once per pass, because a paused service touches nothing; a page that opens later gets the current state from the page itself, not from the stream.

The live scene on the Overview (`internal/web/ts/scene.ts`) is built from these events and from nothing else. What a lane shows is a small pure function of the events it has seen (`applyEvent`, `laneState`, `stateText`: a lost connection beats a pause, which beats the limit), and the sentence in the log is `describe`; both are unit-tested with Node (`npm test` in `tools/typecheck`). The server renders each lane in the state it knows (`GET /fragments/scene`, also used to catch up when the page missed events), the script keeps it current. Tokens are made by the script only for a real event, capped per lane and in total, and skipped under reduced motion or in a hidden tab; the scene starts no timer and no animation while idle.

### The page's look

`internal/web/static/app.css` is a small design system: colour, type, spacing and shape are CSS tokens at the top (two colour schemes, **twilight** (the palette of the app icon, the default) and **immich**, each in light and dark: a scheme is one block of custom properties that gives each name a light and a dark value with `light-dark()`, so the components are written once; `<html data-palette>` picks the scheme, from `THEME` on the server or the person's own choice, and the variant follows the browser unless `THEME_MODE` or the person chose one with the icons in the bar), and the components use only those tokens. The fonts (Bricolage Grotesque, Figtree and JetBrains Mono, latin and latin-ext, plus Onest and JetBrains Mono for Cyrillic) are variable woff2 files in `static/fonts` with their licences beside them (SIL Open Font License), served by the app itself: the Content-Security-Policy allows fonts from the app only, and nothing is ever requested from a font service. No page may use an inline style or script (the policy blocks them), and a test checks this. The light/dark choice is a script of its own (`static/theme.js`) loaded first and without `defer`, so a saved choice applies before the first paint; the scheme and the variant are kept in the browser's `localStorage` (`handoff-palette`, `handoff-theme`), never on the server, and win over the administrator's setting.

Questions (`hx-confirm`) are asked in a native `<dialog>` that every page carries, not the browser's own pop-up: `app.ts` catches htmx's `htmx:confirm` event, shows the question in the dialog and lets the request go ahead only on OK. A button names the dialog's title and button with `data-confirm-title` and `data-confirm-label`, and marks a destructive action with `data-danger`; a test checks every question has them.

The light behind the page is three glows in CSS: two drift slowly for ever in opposite directions (moved with `transform` only, which the graphics card does without redrawing, so it costs next to nothing, and the browser stops it in a hidden tab; reduced motion turns it off), and the effects (`effects.ts`: the light that leans toward the pointer and flares on arrival, cards that tilt), the rolling digits (`odometer.ts`: any number marked `data-odo="<key>"`, the key remembering what it showed before a refresh replaced it), the arrival chime (`sound.ts`, made with the browser's own audio, no sound file) and the command palette (`palette.ts`) are separate small modules started from `app.ts`. They are event-driven (a pointer move, a live event), so nothing runs while the page is idle, and they are skipped for reduced motion; tilt only on a fine pointer. The palette builds its list from the page each time it opens, so it offers exactly the buttons the person has and runs them by pressing them. The effects set CSS variables through the browser's style API, which the Content-Security-Policy allows (it only forbids `style` attributes in the markup). The wall (`wall.ts`) and the frame (`frame.ts`) share one engine, `stage.ts`: the choice of transition (fade, zoom, slide, rise, as classes keyed on the stage's `data-fx`) and of backdrop (a glow that follows the colour scheme, or black, on the page's `data-bg`), the controls that fade away, a kinder `data-lite` mode where the hardware asks for it, the ring and the longer stay for a new arrival, and the glide of tiles in the grid. Everything there moves with `transform` and `opacity` alone, and a test checks it. The three views and their tabs (`tabs.ts`, which also tells the stylesheet where the gradient under the showing tab goes) are chosen by the address's `#fragment`; the tabs are a pill in the middle of the bar on a wide screen and a bar at the bottom of a phone (outside the header, whose blur would otherwise make `fixed` mean fixed to the header); `theme.js`, loaded first, marks the page before it paints so a view never flashes.

### Languages

The pages come in English, German, French, Spanish, Italian, Portuguese and Russian; only what a person reads on a page is translated. The log, the setup check, `NOTIFY_URL` messages and all documentation stay English, always. English is the source: the English sentence is the message's own key, with `{name}` placeholders, so the code stays readable and a message nobody has translated shows in English instead of nothing. Each language is one file, `internal/i18n/lang/<code>.json` (a sentence, or for a counted message an object of plural forms: `one` and `other`, and `few` and `many` in Russian). Templates use `t(ctx, "...")`, `h(...)` for a sentence with markup (values are escaped, the message's own tags are kept) and `n(...)` for counts; the scripts get theirs from a JSON block in the page (`clientMessages` in `internal/web/helpers.go`, read by `ts/i18n.ts`). Text that is stored (events, notes) is saved as the English sentence plus its values (`i18n.Pack`) and turned into words when it is read, so it follows the reader's language. The language comes from the `handoff-lang` cookie, else `Accept-Language`, else English.

To add or fix a language, edit its JSON file and run `go test ./internal/i18n ./internal/web`: tests check that every language has every message, with the same placeholders and markup and the plural forms it needs, and that the scripts' messages match what the page carries. To add a language, add its code to `All` and `Names` in `internal/i18n/i18n.go` (its plural rule, month and weekday names live there too), then a file with every message. When a sentence is added or changed in a template, the same sentence must be added to every language file.

### The page's scripts

They are written in TypeScript (`internal/web/ts/*.ts`) and bundled by esbuild into `internal/web/static/app.js`, which is committed and embedded like the templates. esbuild is a pinned Go tool in `go.mod`, so `go generate ./...` rebuilds the bundle with nothing else installed, and building, running and testing the program never needs Node. esbuild strips types without checking them, so the types are checked separately: `cd tools/typecheck && npm ci && npm run check` runs `tsc --noEmit` in strict mode, and CI does the same in its `types` job. That job is the only place Node is used. Edit the `.ts` files, never `app.js`; the `.map` file next to it lets the browser's developer tools show the original source. The strict Content-Security-Policy still applies: scripts load from the app itself, never inline. Notes on the Immich behaviour this project relies on are in [immich-api-notes.md](immich-api-notes.md).

## Documentation images

`tools/screenshots` makes the pictures from a real running demo, so they can be remade whenever the page changes. See its README.

## Releasing

The steps, with a checklist, are in [Releasing](releasing.md). In short: pushing a version tag such as `v1.2.3` runs the tests, builds and smoke-tests the image for amd64 and arm64, publishes it to GitHub's container registry and creates a GitHub Release from the changelog.
