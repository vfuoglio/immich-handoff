# Security and trust

What you are agreeing to when you run this, or join a service. The short version: an app that can act on your photos has to be trusted, so it is built to ask for as little as possible, show people what it does, and make leaving easy.

## What a key can do

An Immich API key cannot be limited to one tag or album. So a sender's key can read and download any photo in that account, and, if the sender chose **move**, delete any photo too. The app only ever acts on photos tagged for a service, and the key is never shown to anyone. If the sender chose **keep my originals** (the default), the key is created **without any permission to delete**.

A sender who links an album to a service ([Using it](using.md#handing-over-a-whole-album)) gives their own key one more permission, `album.read`, added with their own login. It lets the app list the photos in that album; it cannot change, share or delete albums. Photos that other people own are never handed over.

The receiver's key can upload photos, read the receiver's library and add tags. By default (`RECEIVER_DELETE=on`) it can also **move photos to the receiver's trash** (`asset.delete`), which is what makes copies that expire and a sender's "take back the last hand-over" possible. The app uses it for nothing else: it moves to the trash only copies it handed over itself, and only those that were never filed in an album, and it never deletes for good (the receiver's trash keeps them for as long as Immich does). An administrator who does not want a key that can do that sets `RECEIVER_DELETE=off`: keys are then made without it, and a key that has it loses it when its owner next opens the page (see [Settings](settings.md)). If the service puts arrivals in an album, the key can also find, create and add to albums (`album.read`, `album.create`, `albumAsset.create`); it still cannot rename, share, empty or delete an album, and the app only looks at albums the receiver owns, never at ones shared with them.

Each key is named `handoff: <service>` in the owner's own Immich settings, so people can see exactly what exists and delete it whenever they like. Deleting it ends the connection at once: Immich refuses the key, the app notices and pauses, and no photo is touched.

## Who decides what

- **The sender** decides what happens to their own originals, when they join. The owner has no say over that.
- **The owner** decides who may send, and whether the service is listed or invitation-only.
- **Nobody** sees anyone's key, including their own.

## What is stored

- Without self-service ([Fixed accounts](fixed-accounts.md)) nothing is stored: all state lives in Immich as tags and workflows.
- With self-service, one SQLite file in the `handoff-data` volume lists who joined what. Every API key in it is encrypted with AES-256-GCM using `HANDOFF_STORE_KEY`. The app refuses to start if that key does not match the database.
- **Whoever runs the container and holds the store key can read those keys**, in the same way the person running Immich can already read the photos. This is the trust you place in the person who hosts it.
- The activity log keeps the last 500 events (who joined, left, asked, was approved, lost a connection), in the same file.

## What other people can see

Anyone signed in to Immich can open the page and sees the services that are **listed**, with the owner's name (not email). An owner sees the name and email of someone asking to send, and the names of their senders. Invitation-only services appear only to people who have the link; the link is only as private as how you share it, and **New link** revokes the old one.

An Immich **administrator** can also open the [administrator's page](admin.md), which lists every service, owner and member with their emails and the health of each connection, and can remove a service or a member. It never shows keys or photos, and removing something cannot revoke a key (Immich lets only its owner delete it; [why](admin.md#why-removal-cannot-delete-the-keys)). Who is an administrator is decided by Immich, for the signed-in person, each time.

## Protections in the app

- Immich's events carry a shared secret (`WEBHOOK_SECRET`); the app refuses one that is missing or shorter than 16 characters.
- The page accepts only people signed in to Immich. Every change needs a header that a page on another website cannot send, and each person is limited to 30 changes a minute. The one action that takes data away, the optional backup download, is off unless you switch it on and is limited further (see [Backups](#backups)).
- Pages are served with a strict content security policy, cannot be placed in a frame, and only load scripts and styles from the app itself.
- The metrics page needs the same secret.
- A photo is only removed after every receiver holds a verified copy (checked by checksum), and never while a connection is broken or a service has no healthy receiver.

## Backups

- A backup is a copy of the data file: every stored API key (encrypted with `HANDOFF_STORE_KEY`, which is **not** in the file) and names and emails in the clear. Treat it like the volume itself, and keep the store key separately.
- The app makes automatic backups into `/data/backups` by default. That is beside the data it protects, so point `BACKUP_DIR` at a second folder if you want to survive losing the volume.
- **The administrator's page can hand a backup to a browser, but only if you switch that on** (`HANDOFF_ADMIN_BACKUP_DOWNLOAD=true`). It is off by default because a stolen administrator session could then take a copy of the names and emails and the encrypted keys. When on, it needs a POST with the header a page on another website cannot send, a signed-in Immich administrator, and a confirmation in the page; it is limited to one download every 10 minutes for the whole app; each one is written to the recent events and sent to `NOTIFY_URL`; the copy is made in a private folder, removed as soon as it is sent, and marked `no-store`. The encrypted keys are useful only to someone who also has `HANDOFF_STORE_KEY`, which the page never shows. Details: [Download a backup from the administrator's page](operations.md#download-a-backup-from-the-administrators-page).

## The image you run

The published image is built by GitHub Actions from a tag of this repository, for amd64 and arm64, on a base with no shell or package manager (distroless). Each release is **signed** (cosign, keyless, tied to the release workflow), carries a software bill of materials and build provenance, and is checked by CI before it is published. [Releasing](releasing.md#verifying-an-image) shows how to verify an image yourself. The workflows pin every action by commit hash, and Dependabot proposes updates to the actions, the Go modules and the base images each week.

## Reporting a problem

If you find a security problem, please open a private security advisory on the GitHub repository rather than a public issue.
