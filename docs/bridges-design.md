# Bridges: handing photos over to another Immich server

The conclusion of #17, and the design #77 is built from. **Built** in 1.3.0 (first slice: the sender's app holds the key; one remote receiver per service, in addition to the owner's own). How to use it: [Using it](using.md#receiving-on-another-immich-server). What it can and cannot do: [Security](security.md#bridges-a-receiver-on-another-server). Where the build differs from the plan below: the tag on the other side is `from/<sender>`; a copy there is trusted on Immich's answer to the upload, because the key cannot read it back to compare checksums; the pause before a retry is `BRIDGE_RETRY_MINUTES`; and a service with a bridge does not run at all while the bridge is down or switched off, for the owner either, since an original is removed only once every receiver has its copy.

## Conclusion

**It is worth doing, with a smaller key than a same-server receiver has and a strict rule about where the app may connect.** A receiver on another server can be a destination like any other, provided that:

1. the key the app holds on the other server can **upload and tag, and nothing else** (no reading, no deleting);
2. the app only connects to the address in an invitation after checking it is a public address (private addresses only when an administrator turns that on);
3. the receiver can cut the connection at any time by deleting the key, and no photo is lost when they do.

If an owner does not want this risk, nothing changes for them: bridges are off unless an administrator turns them on.

## What was checked

Two real Immich 3.2.0 servers were started side by side and the receiver's side was driven only with an API key, the way the app would.

| Question | Result |
|---|---|
| Can a key that only uploads and tags do the whole hand-over? | **Yes.** `user.read`, `tag.create`, `tag.asset`, `asset.upload` was enough to upload (201), make a tag with `PUT /tags` (upsert, which handles `from/alice`-style nested tags), and put it on the photo (200). |
| Can that key read anything? | **No.** Reading an asset, searching, downloading the original, listing albums, listing API keys, people and server storage all returned 403. |
| Does the usual receiver key (`asset.read`, `tag.read`, `stack.create`) read the whole library? | **Yes.** With `asset.read` a search with no filter listed every photo. On the same server that is the receiver's own key held by an app they chose; across servers it would give a stranger's app a read key to a library. That is why the bridge key drops it. |
| What does a duplicate upload tell the app? | `200 {"status":"duplicate","id":…}`: the app learns the photo is already there without reading anything else. This is what makes a retry safe without `asset.read`. |
| What does a revoked key do? | Every call answers `401 Invalid API key` at once, including an upload in progress. Nothing was changed in the library. |
| What can the key not do to itself? | It cannot list, make or change keys (403). |
| What does it leak? | `GET /users` answers 200 to any `user.read` key: the **names and emails of everyone on the receiver's server**. The bridge key must never be used to call it, and the app must not keep what it returns. `user.read` is needed for `/users/me` (to learn the key's owner). |
| Can the app tell the other server's version? | Yes, without a key: `GET /server/version` answers `{major, minor, patch}`. |

What this does not cover: waiting photos after a revoked key (designed below, not run end to end) and redirects (the app is not written yet).

## Design

### The key
The bridge key has exactly `user.read`, `tag.create`, `tag.asset`, `asset.upload`. Consequences, all fine:
- No `tag.read` and no `asset.read`: the app cannot list the receiver's tags or photos, and so cannot show them or copy them elsewhere.
- No `asset.delete`: **taking back and expiring a copy do not work across servers.** The invitation page says so before the receiver accepts. (They can be added later as an opt-in; this is the biggest trade-off of the design.)
- No stacks, no album: photos arrive as separate photos with the tag. Both are conveniences.
- The receiver makes the key in their own Immich, so they see exactly what it can do and can delete it.

### Invitations (discovery and consent)
- The service owner creates a **bridge invitation**: a link on their app, like the existing invitations. It carries the service name and the sender's display name, nothing else.
- The receiver opens it on their **own** copy of the app, or on a page of the sender's app, and enters their Immich address and a key they made following the page. Nothing is accepted until the app has run the checks below.
- The receiver sees who is asking and what the key can do. The sender sees only the receiver's chosen display name and whether the connection works; never their address of residence, email or library.
- The same receiver rules as today still apply (daily limits, quiet hours, approval). If the receiver's own app is not involved, those rules live in the owner's app and are kept as settings on the invitation.

### Checks when a bridge is added and on every pass
1. The address is `https://` (or `http://` only with the private-address option).
2. It resolves to public addresses. Loopback, link-local, private ranges, the cloud metadata address and unique-local IPv6 are refused, **after** resolution and again on the actual connection (so a name that changes cannot slip through).
3. Redirects are not followed. A server that redirects is treated as down and the doctor says where it points.
4. `GET /server/version` answers and the major version is one the client is built for.
5. `GET /api-keys/me` returns exactly the four permissions, no more. A key with extra permissions is refused with the reason ("this key can read your library; make a smaller one"), because the app should never hold power it does not need.

### Unreachable server or revoked key
- Photos stay tagged in the sender's library, exactly as for a paused service. Nothing is sent, nothing deleted.
- A `401` marks the bridge **revoked** (no retries; the owner and the receiver are told once). A timeout or a 5xx marks it **unreachable** and is retried with backoff.
- The doctor and the Overview show the state and the reason, by the same wording as the other health checks.
- Photos already uploaded are safe on the other side; the log keeps the hand-over record.

### Sender side
The app streams the original to the receiver's server with its checksum, the existing hand-over engine. The sender's key and the original-touched-last rule are unchanged. Streaming means a large video never lands on disk. Duplicate answers count as success.

### Versions
The generated client is pinned to one Immich API. The four calls the bridge uses (`/server/version`, `/api-keys/me`, `POST /assets`, `PUT /tags`, `PUT /tags/{id}/assets`) have been stable across versions; the bridge refuses a major version it was not tested with, and shows both versions on the page. A test against the oldest and newest supported Immich covers this.

### What the receiver can see
Only what the sender's service puts on the photo: the original filename and metadata that are already in the file, and the tag name `from/<sender>`. No account details are sent.

## Open for #77
- Whether to offer the optional `asset.delete` for take-back, and how to word what it allows.
- A second app instance on the receiver's side (two-way) versus the sender's app holding the key. The first slice is one-way.
- The private-address option: an administrator setting (`BRIDGE_ALLOW_PRIVATE`, off by default), for people who run both servers themselves behind a VPN.

## Not done by this note
No code. #77 builds it, and it needs an end-to-end test with two Immich servers (this spike used the same setup by hand).
