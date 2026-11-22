---
title: "Cast to a TV"
audience: contributor
---

# Cast to a TV

**Built** in 1.4.0 (#269). Immich users ask for casting to a TV or a Chromecast (about 330 votes between the requests); Immich itself has no casting from a browser.

## What it does

A TV opens `/tv`, gets a short code, and shows it. The signed-in person types the code on their page and chooses a source; the TV then shows those photos and follows the person's remote (pause, next, previous, a new source, stop).

## How

- **Waiting TV.** `POST /tv/new` makes the TV's secret (128 bits, 32 bytes of hex twice) and a 6-character code (from `shortAlphabet`, no look-alike letters, chosen without modulo bias). Both are kept **in memory only** (`castHub`), for 10 minutes, at most 500 at once and 20 new codes per address per hour, so the public route cannot fill the database and a restart only makes TVs ask again.
- **Claim.** `ClaimCast` takes the code, the source and how to play. Wrong codes count per person (five in ten minutes silence them for ten) and across everyone (100 in ten minutes make every claim wait). The source is checked against the person's own Immich with their own session: an album they own or that is shared with them, a person Immich has named, a day, or everything new. The key is made (or reused) and the source is read once to be sure it has photos; only then is the code used up and the session written (`cast_sessions`, with the TV's secret as its id).
- **The TV's secret.** It is the TV's only credential. Unknown, ended and expired sessions are answered alike (404 or `ended`). It is carried in the query string of the TV's own requests (a TV cannot send headers with an image), with `Referrer-Policy: no-referrer`, `no-store` and `noindex`.
- **The photos.** `castList` reads a source with the person's cast key: photos only (no videos), not trashed or archived, newest first, at most 500; an album with `AlbumAssets`, a person or everything new with `SearchUpTo` (one search that stops after about 500, not a whole library), a memory with `MemoriesFor`. It is kept for `FRAME_REFRESH` per source, so several TVs on one source ask Immich once. The TV is given ids `hmac(secret, asset)[:16]`, never Immich ids, and a photo is served only if its id is in the list; pictures are the previews, kept in the shared LRU the frames use.
- **The key.** One per person, `handoff: cast`: `asset.view` and `asset.read`, plus `memory.read` when they cast a memory (added with their session). It is sealed in `cast_keys` (vault context `cast:`), kept while any TV is showing, and deleted with the person's own session: when they stop the last TV, or the next time they open their page after the last one ended (the app has no session of theirs at the moment a TV times out).
- **Remote control.** The state is a small record the TV polls every 3 seconds: paused, hold seconds, effect, a counter and the last command. A command (next, previous, a new source) bumps the counter; the TV carries it out once when the counter changes. Polling works on every browser and needs no sign-in on the TV; the app's event stream needs one.
- **Two pages.** `tv-boot.js` is ES5: it tests for `Promise`, `fetch` and `String.prototype.replaceAll` (browsers from about 2021) and loads `tv.js` (ES2020, the frame engine's effects) or `tv-legacy.js` (ES5, XHR, fade only). If `tv.js` does not say it started within five seconds, the boot script loads the legacy one. `/tv/legacy` forces the legacy page. A test checks that the legacy bundles contain no arrow, optional chaining, `async`, class or template syntax.
- **Housekeeping.** Sessions end when the TV has not asked for 30 minutes or 24 hours have passed (`CastMaintenance`, in the rules' round and when the person opens their page).

## Not done

- A TV session is not tied to the TV: whoever holds the secret (which only the TV page has) can ask for the photos until it ends.
- The code is 30 bits. That is acceptable only because it is short-lived, single-use, needs a signed-in Immich account to try, and is limited per person and in total.
- No casting to an Apple TV or a Chromecast receiver app; both need a native receiver.
