# Handing over to other places: a folder, WebDAV, S3 and SFTP

> *For contributors: this is a design note, not a user guide. The guides are in [the documentation index](../README.md).*

**Decided** in #196, after a reading of the code and fifteen questions to the maintainer. Photos can go to another account on the same Immich and to another Immich server (a bridge). People also ask for a NAS share, WebDAV (Nextcloud, ownCloud), S3-compatible storage and SFTP, and the WebDAV request in Immich (discussion 1687) asks for import as well as export. This note says what is built, what was found in the code that limits it, and how the build is split.

## Conclusion

**Go, in this order:** make the hand-over seam general (several destinations, a destination with no Immich receiver, ids that are not Immich ids); then a **folder** destination (which, through an rclone mount, also reaches most clouds); then **WebDAV**, **S3** and **SFTP**; then **import** from the same places into Immich. Named destinations are defined by the administrator; with a switch, owners may add network destinations of their own on the page.

## What was decided

| Question | Decision |
|---|---|
| Which destinations | A folder, WebDAV, S3-compatible and SFTP. Native Google Drive, OneDrive and Dropbox stay out: they need OAuth apps registered by the maintainer; use a folder with rclone. |
| How it relates to a service | Both: a destination is an **extra receiver** of a service (as a bridge is) **and** a service may be **destination-only** (no Immich receiver). Both in the first release. |
| What is kept | The original, an **XMP sidecar** (rating, location, description: what a bridge carries) and a **small log** record. |
| Layout | The owner chooses a **template**, such as `{service}/{sender}/{yyyy}/{mm}`. |
| Take-back | **Delete the file** at the destination within the 24 hours, and give the sender the original back. |
| A verified copy | **Size and checksum where cheap**; a move waits for it. |
| A name already there | Same name and checksum: skipped and counted as delivered; otherwise a **suffix** (`IMG_1234 (2).jpg`). |
| Live photos, stacks | Still and clip **side by side** with one base name; stacks go as separate files. |
| Who sets it up | **Both**: the administrator defines named destinations, and with a switch owners may add their own network destinations on the page. |
| Private addresses | Its **own switch**, `DESTINATIONS_ALLOW_PRIVATE`, not the bridge's. |
| A folder in the container | A documented bind mount and a named root; `doctor` writes and removes a test file. |
| Secrets | **Both**: environment or files for the administrator's, sealed in the data file for the owner's. |
| Limits | The service's cap, quiet hours and accept rules apply once; **plus** a per-destination daily cap and a per-destination pause. |
| Several destinations | Several allowed; **a failing one does not block the others** (see below for what that means for a move). |
| Direction | **Send and import together** in this build, import after send. |

## What the code limits (found in #196)

- **A service cannot run without an Immich receiver** (`runService` returns at once with none, `TakeBack` answers `ErrNotJoined`, the routes file demands a receiver) and **a stored service needs an owner with a key**. A destination-only service needs those relaxed.
- **`syncer.Service.Remote` is a single field** built only from the `bridges` table, and `takeBackFar`, `BridgeStatus` and `doctor` are hard-coded to `*bridge.Remote`. Several destinations need a list and an interface with take-back, status and diagnosis.
- **The ledger is keyed by Immich ids**: `handover_items.receiver_asset` is a UUID and `RemoteCopy.ID` is a `uuid.UUID`. A file needs a string reference (a path or key) and a destination id.
- **Delivery is stateless across passes**: Immich answers *duplicate* by checksum. A file destination must be idempotent itself (the checksum record below) because a retry after a partial failure would otherwise copy again.
- **`TransferWith` ties the download to an Immich upload.** `API.Original` is the reusable streaming download; a destination writes from that stream with a stall watchdog and a SHA-1. The sender's key already needs `asset.download`.
- **The image is `distroless/static`, runs as uid 65532, without CGO**: no `ssh`, `rsync` or `rclone`, so everything is pure Go. A mounted folder must be writable by that user. There is no S3, WebDAV, SSH or SFTP library in `go.mod` (`golang.org/x/crypto` and `pkg/sftp` would be new, and so would host-key pinning); the app already has hand-written IMAP and SMTP, so WebDAV (PUT, MKCOL, PROPFIND) and S3 (request signing) are written on the standard library over the hardened client of `bridge.Policy`, with its own switch.
- **Nothing in the people, albums or tags travels** (as with a bridge): only the original, rating, location and description.
- **S3 and WebDAV cannot append** to a log, and S3 has no rename or trash: take-back is a delete, and the log is one record per file (below).

## Design

### The destination
One small interface, in its own package, that the folder, WebDAV, S3 and SFTP implement and that import also uses:

`Put(name, reader, size) → (ref, sha, err)` (written under a temporary name and moved into place where the store can, else written once with a verified size and checksum); `Stat(ref)` (size and, when the store gives one, a checksum); `Delete(ref)`; `List(prefix)`; `Open(ref)`; `Check()` (can it be reached and written to, for `doctor` and for *Ready*). `Remote` becomes an adapter over it: it streams the original (`API.Original`), names the file from the template, writes the sidecar and the record, checks the copy and returns a `RemoteCopy` whose `Ref` is a string.

### Names, sidecar, record
- **Template.** `{service}`, `{sender}`, `{yyyy}`, `{mm}`, `{dd}`, `{name}`; the owner sets it per destination; path pieces are cleaned (no `..`, no separators in a value, limited characters and length) and the result is checked to stay under the destination's root.
- **Sidecar.** `IMG_1234.jpg.xmp` from `immich.Sidecar(a)` (rating, location, description), the same details a bridge carries and under the same `CARRY_DETAILS` switch.
- **Record.** `IMG_1234.jpg.handoff.json`: sender, service, time, the source asset's checksum and the SHA-1 of what was written. It is also what makes delivery **idempotent**: before writing, a file with the name is looked at, and the same checksum means *already there*; a different one gets the suffix. A log in one file would need append, which S3 and WebDAV do not have.
- **Live photo.** The still and the clip are written with one base name (`IMG_1234.jpg`, `IMG_1234.mov`); Immich's own live-photo link is not reproduced.

### Verified, and the move
A destination is "holding" a photo only after `Stat` agrees with the bytes that were sent: size always, SHA-1 for a folder and SFTP (read back or taken from the stream written to a temporary name and checked after the move), the ETag or checksum header for S3 and WebDAV where the server gives one, and *no checksum from this server* said in the record when it does not. The existing order stays: every receiver and every destination first, then the original is kept (tag swapped) or moved.

### Several destinations, and a failing one
Each destination keeps its own state (ready, waiting, paused, failing since) and its own record per photo, so a failing destination **does not hold up the others**: they take the photo and it is recorded per destination. **What a move does is not the same:** the original is deleted only when **every** destination and receiver holds the photo (the promise that exists today). A failing destination therefore holds the *original*, still tagged, and says so, and the next pass delivers only to the destination that lacks it (checksum record). In copy mode the original stays and the destination catches up whenever it can. The owner can pause or remove a failing destination to let the move go on, and is told that the photos it never received are not there.

### Where it is set up
- **The administrator** names destinations in a file (`DESTINATIONS_FILE`, like `ROUTES_FILE`): a kind (`folder`, `webdav`, `s3`, `sftp`), an address or root, secrets as `${VAR}` or a file. A service owner picks one by name. This is also the only way a **static** (routes-file) service gets one.
- **Owners on the page**, only when the administrator switches it on (`DESTINATIONS_USER_ADD`): the owner types the address and credentials of a network destination on their service card; they are sealed with the store key (context `destination:<id>`) and never shown again. **A folder is never typed by a user**: it must be a root the administrator named, and the owner adds only the template under it.
- **Private addresses** (a NAS on the LAN) need `DESTINATIONS_ALLOW_PRIVATE`; the rest of the hardening is the bridge's (https unless private, no redirects, no proxy, the name resolved and checked, ports restricted). SFTP pins the host key on first use and shows the fingerprint to the person adding it; a changed key stops deliveries and says so.
- **`doctor`** checks each destination: reachable, writes and removes a test file, the folder is writable by the app's user, the S3 bucket exists, the SFTP host key matches.
- **A destination-only service** has an owner (an Immich user who made it) but no receiver key and no Immich receiver; its owner is not a receiver and the tag, limits and accept rules work as today.

### Limits
The service's daily cap, quiet hours and accept rules are decided once, before the photo goes anywhere. Each destination may also have its **own daily cap** (photos and megabytes) and be **paused alone**; a paused or capped destination counts as *waiting* for the move rule above.

### Take-back
Within 24 hours and for the last batch only (as today), take-back **deletes** the files (original, sidecar, record, clip) at each destination through `Delete(ref)`, then restores and untags the originals as it does now. It needs the destination to be reachable; if it is not, nothing is changed and the person is told, as for a bridge. The ledger gets a destination id and a string reference.

### Import
The same places, read: **a destination can be followed** the way an album shared with the person is followed. New files since the follow began (`List` by modified time, with the file's checksum record or hash) are **offered with pictures** and the person says *Add to my library* (or *Always*, after three, automatic for files that appear afterwards), and the app uploads them with the person's own key (`asset.upload`) and the sidecar. Immich's duplicate check by checksum prevents a second copy; the offer remembers what it has seen. A destination that also receives hand-overs is not offered its own deliveries back (the record names them). It is the receiving side of #187 with a different source, so it uses the rule engine's suggestion cards and the library-follow code.

## Built so far (#236)

- `internal/dest`: the `Destination` interface (`Put`, `Stat`, `Delete`, `List`, `Open`, `Check`; `Put` reads its reader to the end and says whether the file was already there), an in-memory implementation for tests, and `dest.Remote`, which streams the original from the sender's Immich with a stall watchdog and says a file is held only when the destination's own look at it (size and, when it can say, the SHA-1) agrees with the bytes sent.
- `syncer.Service.Remotes` is a list. A receiver that is not ready is left out of the pass and the others go on; a photo is finished only when none lacks it (the original stays tagged; in copy mode the service tag stays too), and a retry skips the receivers that already hold it (`RemoteLedger.Delivered`). **A bridge keeps its old behaviour**: it implements `syncer.Holder`, so while it is down the whole service waits, as before.
- The ledger gained a destination name and a string reference (`handover_items.dest`, `.ref`; what a bridge recorded before is kept under `bridge`). Take-back, the doctor and the bridge status go through interfaces (`TakeBacker`, `Diagnoser`) instead of asserting `*bridge.Remote`.
- A service can have **no Immich receiver** (`services.dest_only`): its owner makes no key, and take-back then restores the originals from the destinations. `Hub.Destinations` is the hook that gives a service its receivers; #237 fills it from the administrator's list and from the owners'. The owner's page does not show such a service yet (it finds a service through its Immich receiver): that is part of #237. The routes file still requires a receiver: a static service naming a destination comes with #237.

## Built so far (#237)

- **The administrator's file** (`DESTINATIONS_FILE`, `dest.ParseFile`): named destinations with a kind and settings; `${VAR}` and `x_file` for secrets; every problem reported, and the app does not start with one. `DESTINATIONS_USER_ADD` and `DESTINATIONS_ALLOW_PRIVATE` are read with it (`dest.SettingsFrom`). The four kinds are named but have no builder yet: a destination of a kind without one says so (`dest.ErrKindUnavailable`) on the owner's card and in `doctor`, and the file stays valid. Each kind registers its builder (`dest.Register`) in its own issue.
- **Bindings** (`service_destinations`): a service uses a destination by reference (`admin:<name>` or `own:<id>`) with a path template (`dest.Path`: cleaned placeholders, always under the root), its own daily limit (a log of what it received, `destination_log`), its own pause and its state. A person's own network destination is sealed (`destinations.secret_enc`, context `destination:<id>`), checked with `dest.CheckAddress`, and never a folder.
- **At run time** the binding is a `Remote` (`boundRemote`): a paused or capped destination is not ready (what it would receive waits while the others go on), a failing one is told once to the owner (activity, the open pages, the administrator's notification) and once more when it is back. A service of the routes file names destinations under `destinations:`, and may have no receivers.
- **The owner's card** has the destinations (status, template with an example, limits, pause, remove), the list to add from, and, when the administrator allows, a form for a network destination; a service can be made **destination-only** from the offer form.

## Built so far (#238)

- **`dest.Folder`** (kind `folder`, registered in `internal/dest/folder.go`): a file is written under a temporary name beside its place, checked (size, SHA-1), synced and moved into place, so it appears whole or not at all; a name already taken by a file with the same content is left alone (*already there*), by a different one gets `name (2).ext`; a name that would leave the root is refused; `Delete` removes the folders it leaves empty up to (never including) the root; `Check` says what to look at (not mounted, not a folder, not writable by the app's user) by writing and removing a test file.
- **`dest.Remote`** now writes, beside each photo, the **XMP sidecar** (`IMG.jpg.xmp`, rating, location and description, under `CARRY_DETAILS`) and a **record** (`IMG.jpg.handoff.json`: sender, service, time, the source asset and its checksum, the SHA-1 written, the size). A repeat keeps the first record. A live photo's clip is written after the still, with the still's final base name (so `IMG (2).HEIC` gets `IMG (2).MOV`). If anything after the photo fails, what the delivery created is removed again so a retry starts clean. Take-back deletes the photo, its sidecar and its record, and the clip. All of this is in the shared receiver, so WebDAV, S3 and SFTP get it with their `Destination`.
- `docker-compose.yml` has the commented mount and the `user:` override, and `destinations.example.yaml` an example file.

## Built so far (#239)

- **`dest.WebDAV`** (kind `webdav`; settings `url`, `user`, `password`): PUT, MKCOL, PROPFIND, MOVE, DELETE and GET on the standard library, Basic authentication, through the hardened client of bridges with `DESTINATIONS_ALLOW_PRIVATE` as its switch. A file is PUT under a temporary name in its own folder (made first, top down), its size read back with PROPFIND (and the SHA-1 when the server gives one, as Nextcloud and ownCloud can: `oc:checksums`), and MOVEd into place with `Overwrite: F`. A name already taken is the same file when the sizes match and the checksum (the server's, or a read of the file) does; otherwise the new file is suffixed. Large files are streamed with a known length, never held in memory. Folders a take-back leaves empty are **not** removed (a WebDAV delete of a folder deletes what is in it). Listing goes folder by folder (a server may refuse a listing of everything).
- **What the record says:** `verified` is `size and checksum` or, for a server that gives no checksum, `size only`.
- **Messages** say what is wrong in words (login refused, folder missing, not allowed, out of room, a redirect that is not followed) and never contain the password or the server's address.
- Resumable uploads are not used: a failed file is written again whole on the next pass (the temporary file is deleted first), which is the simple choice that works with every server.

## The test
- **Unit tests** for the interface against an in-memory store, the template cleaning (traversal, long and odd names), the suffix and idempotence rules, the checksum comparison, the per-destination state, and the cap and pause.
- **End to end** (a throwaway Immich with two people): a folder destination for a service (extra receiver and destination-only), a photo handed over arrives with sidecar and record, a repeat is skipped, a different photo with the same name is suffixed, a live photo, a move after a verified copy, a move held by a destination that is down, take-back deleting the files. WebDAV against a small in-test server (`golang.org/x/net/webdav`), S3 against an in-test S3 stub or MinIO in the stack, SFTP against an in-test server on the new dependency.

## Risks, plainly
- **A wrong template or a wrong root writes files where nobody wanted them.** Roots are the administrator's; paths are cleaned and checked to stay under the root; the owner sees an example path while typing the template.
- **Take-back is a permanent delete** at a destination (no trash), after only 24 hours: the confirmation says so.
- **A checksum is not always available** (some S3-compatible servers, WebDAV without a checksum): the guide and the record say what was checked.
- **A third party sees the photos** (a cloud, a NAS): the security notes say that the destination, not this app, decides who can read them.
- **Scope is large**: four destinations plus import. The split keeps each piece shippable on its own, with the folder first.
- **SFTP adds dependencies** (`golang.org/x/crypto/ssh`, `github.com/pkg/sftp`) and host-key handling.

## Go

Go in the order of the issues under #196: the general seam, destinations defined and configured, the folder, WebDAV, S3, SFTP, the guide and the end-to-end tests, and import (folder first, then the network kinds). The throwaway prototype named in the issue is not made; the folder destination is the first real piece.
