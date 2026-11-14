# Shared albums across servers

The conclusion of #141, and the design #131 is built from. **Not built yet.** The question was whether two people on different Immich servers can share one album that stays in sync, as real copies in each person's own library, and what that would need.

## Conclusion

**It can be done, safely and cheaply, because Immich already gives the two things a two-way sync needs: a photo's identity across servers (its checksum) and an answer to an echo (a duplicate upload).** The rest is the app's work: a small signed protocol between two Handoff apps, a ledger, and a timer.

## What was checked

Two real Immich 3.2.0 servers and keys made with each person's own account, driven the way the apps would drive them. The check stays in the suite as `TestSharedAlbumImmichRules` (bridge group), so a change in Immich is noticed.

| Question | Result |
|---|---|
| Can each app work with a key of its own server, with a modest set of permissions? | **Yes.** `user.read`, `asset.read`, `asset.download`, `asset.upload`, `album.read`, `album.create`, `albumAsset.create`, `albumAsset.delete` is accepted and does everything below. No `asset.view` (no pictures are needed), no `asset.delete`. |
| What does each permission buy? | Without `asset.read` the album cannot be listed (403); without `asset.download` an original cannot be read (403); without `albumAsset.create` a photo cannot be added to an album (403); without `album.read` the album itself cannot be fetched (403). `user.read` only says whose key it is (`/users/me`), and `album.create` and `asset.upload` only make albums and photos. |
| How does the app tell what is new? | An album listing (a metadata search by album) answers each photo's **checksum**, so what is new is told **without downloading anything**. A three-photo listing took about 8 ms: a timer every few minutes costs nothing. |
| Is the checksum the same on two servers? | **Yes.** It is the base64 SHA-1 of the uploaded bytes, so the same file has the same checksum on both servers, and the copy that arrives on the other side lists with the checksum of the original. It is a photo's identity across servers. |
| Can an original go from one server to the other without touching the disk? | **Yes.** The download body streams into the upload body through a pipe (as `UploadFile` already does); the photo is created (201) and keeps the file's own dates. |
| What happens if the copy is sent back (an echo)? | **200 `duplicate` with the first server's own asset id.** Nothing is created, and the id needed to put the photo in an album is known. An echo is harmless even without a ledger; the ledger only saves the download. |
| What if the other person put their copy in the trash and it is sent again? | **200 `duplicate` with the trashed copy's id, and it stays in the trash.** A re-send neither restores nor duplicates it. The app must then **not** add that id to the album (it would put a trashed photo in an album), and so respects the person's deleting. |
| Can the app take a photo out of an album? | Yes, with `albumAsset.delete`; the photo itself stays in the library. Seeing that someone removed a photo is a difference between two listings (Immich sends no event for album changes either way). |
| Can the key be limited to one album? | **No.** With `asset.read` it can list the whole library (the same finding as for bridges). The app, not the key, limits it, which is why the bigger two-way key of the one-app way is a trust decision. |

Not run: a third server (the group relay is app logic, reasoned below), very large videos, two owners on one server.

## Design

### Two apps (the normal way)
Each person has a Handoff next to their Immich. The apps never share an Immich key; each only touches its own Immich with a key made by its owner's own session (the same way a service's key is made).

- **A peer** is the other app: its address, a shared secret, the protocol version, a state. It is made by an **invitation**: the person who starts it makes a link; the other person opens it on their own app; the secret is exchanged in that step.
- **Requests are signed.** Every request carries `X-Handoff-Time`, `X-Handoff-Nonce` and `X-Handoff-Signature`, the HMAC-SHA256 of `method`, path, time, nonce and the SHA-256 of the body under the shared secret. A request more than a few minutes old, or with a nonce already seen, is refused. HTTPS only; the existing address policy (`bridge.Policy`) decides where an app may connect.
- **Endpoints** (a version in the path, `/peer/v1/...`): `hello` (versions and name), `have` (the sender lists checksums, the receiver answers which it lacks), `photo` (the sender streams one original with its name and date; the receiver stores it in its own Immich, puts it in its album and records it), `changed` (a nudge: "I found something new, look now") and `leave`.
- **Sending** is: every few minutes list my album, take the photos whose checksum is neither in the ledger of what arrived nor in what I already sent, ask `have`, stream what is missing. **Receiving** is: store, put in my album (never if the answer was a trashed duplicate), record the checksum in the ledger as **arrived**. The ledger is what stops a photo going back; Immich's duplicate answer is the second line.
- **Speed:** the timer plus the nudge gives minutes; **Sync now** forces a pass.
- **Resume:** a cut in the middle of a photo is retried from the start of that photo (an original is one request); very large videos can use the pieces idea of phone upload (`PUT` with an offset), decided in #144.

### History, deletions and settings
Each side chooses at joining whether to share what is in its copy already or only what it adds from now on (the ledger starts with the existing checksums marked *not to send* for "from now on"). The **deletion rule** is one setting agreed at joining: nothing travels (default), or removal from the shared album follows (the app diffs two listings, and applies the other side's removal only with `albumAsset.delete`; a large removal asks first; a removal is never repeated back). Deleting a photo never travels.

### Groups
Everyone syncs through the **creator's** server: members pair only with the creator, whose app relays. A photo carries its **origin** (the peer that first had it) in the ledger entry; the relay never sends a photo to its origin or to a peer whose `have` says it has it. If the creator's server is off, the others wait and nothing is lost. A member leaving ends their link only.

### The one-app way (a relative without Handoff)
One app drives both sides: its own Immich with its own key, the relative's with a key the relative made. **Receive-only** is the bridge's key (`user.read`, `tag.create`, `tag.asset`, `asset.upload`), plus `album.create` and `albumAsset.create` if the photos should land in an album. **Two-way** needs `asset.read`, `asset.download` and `album.read` on the relative's server as well; it can read the whole of their library, so the page says so, the checks judge a key by *no more than needed* (the rule of `bridge.Check`), and the relative can delete it any time. The same ledger and rules apply.

### What each side can see
Only what the shared album carries: the files with the dates and places inside them, and the sender's display name. No account details, no other albums, no addresses of residence.

## Answers to the open questions of the child issues
- **#142 protocol and versions:** as above; a version in the path, `hello` says what each app speaks, and an app refuses a protocol it does not know with a reason; an app that cannot be reached from outside (no public address) can still pair if the *other* side can reach it, never the reverse, and says so.
- **#144 receiving endpoint:** `photo` streams the body into `UploadFile`; large videos are bounded by a per-file size cap setting.
- **#145 the same photo on both sides at once:** both have the same checksum, so each side's `have` says it is there and nothing moves.
- **#146 deletions:** see above; a trashed duplicate is never put back in an album.
- **#147 key judgement:** by exact permission set; a key with more is refused with the reason.
- **#148 group size:** a creator's server relays up to 10 members at first.

## Risks, plainly
- Both people must run Handoff and reach each other over HTTPS (the one-app way is for relatives who will not).
- Two apps must stay protocol-compatible across releases (the version in the path and the `hello` exchange are for that).
- The two-way key of the one-app way can read a stranger's library, and Immich cannot narrow it.
- Large videos on slow connections make a sync long; a per-file cap and a quiet-hours option are the answer.
