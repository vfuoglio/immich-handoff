# Hand photos over to a folder, a cloud drive or a server

**Who it is for:** Someone who wants the photos they tag, or the photos that arrive in a service, to also (or only) land somewhere that is not an Immich account: a NAS share, a Nextcloud, an S3 bucket, a server over SFTP. The administrator who runs the app names the places; the owner of a service chooses which of them it uses.

**What you get:** The same hand-over as always (tag a photo, press **Hand over...**, a rule sends it), with one more place the photo goes. The photo is written as a file with its details beside it, checked, and only then is the original moved or marked.

## What a destination is

A **destination** is a place that is not an Immich account. There are four kinds:

| Kind | For | What the administrator gives |
|---|---|---|
| **folder** | A NAS share, an SMB or NFS mount, or a cloud drive reached with [rclone](https://rclone.org) (Google Drive, OneDrive, Dropbox, Backblaze and many more: the app then never holds the cloud's login) | A path inside the container |
| **webdav** | Nextcloud, ownCloud and other WebDAV servers | An https address, a user name and a password (an app password for Nextcloud and ownCloud) |
| **s3** | Amazon S3, MinIO, Backblaze B2, Wasabi, Cloudflare R2, Garage | An endpoint (none for Amazon), a bucket, an access key and a secret key |
| **sftp** | Any SSH server | A host, a user, a password or a private key, and the server's key |

A service can send to the people who joined it (**an Immich receiver**, as always), to **one or more destinations**, or **only to destinations** (make the service with **Only to destinations**: nothing is copied into your own library). A photo is handed over to every place the service has, and **the original is moved or marked only when every one of them holds the photo**.

## Setting it up

**The administrator** names the destinations in a file and points `DESTINATIONS_FILE` at it (see `destinations.example.yaml`):

```yaml
destinations:
  nas:
    kind: folder
    root: /destinations/nas
  cloud:
    kind: webdav
    url: https://cloud.example.org/remote.php/dav/files/me/Photos
    user: me
    password_file: /run/secrets/webdav_password
```

- A value can be `${VAR}` (from the environment), and a setting called `x_file` reads `x` from a file, so secrets can be Docker secrets.
- **A folder is a path inside the container.** Mount the share there in `docker-compose.yml` (`- /mnt/nas/photos:/destinations/nas`). The app runs as the unprivileged user **65532**, which must be allowed to write in it; if the share belongs to a NAS user, run the container as that user (`user: "1000:1000"`) instead. `immich-handoff doctor` writes and removes a test file and says what is wrong.
- **An SFTP server is trusted only by its key.** Leave `host_key` out the first time, run `doctor`, which shows the server's key (`SHA256:...`); check that it is your server's (`ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on it) and write it as `host_key`. If the key ever changes the app stops sending and says so.
- Addresses on your own network (a NAS at 192.168.x.x) need `DESTINATIONS_ALLOW_PRIVATE=true`; without it only public addresses are reached, and only over https. It is not the switch for bridges.
- With `DESTINATIONS_USER_ADD=true` a service owner may also **enter a network destination of their own** (WebDAV, S3 or SFTP) on the service's card; its password or key is kept encrypted and never shown again, and for SFTP the owner presses **Look at the SFTP server's key** and compares it before saving. A person can never type a folder.

**The owner of a service** opens its card on the Services tab, **Destinations**, picks one and chooses **where files go**. A service from the routes file names them under `destinations:` (with an optional `template`), and may have no receivers at all.

## Where files go

The **template** says where under the destination's root a photo goes. The default is `{service}/{sender}/{yyyy}/{mm}`, which gives `family/Bob/2026/03/IMG_1234.jpg`. You can use `{service}`, `{sender}`, `{yyyy}`, `{mm}`, `{dd}` and `{name}`; the file's own name is added unless the template has `{name}` (`{yyyy}-{mm}-{dd}_{name}`). The card shows an example while you choose. A sender's or a file's name can never make a path leave the root (no `..`, no separators, no characters a file system refuses).

## What is written

Beside each photo, in the same folder:

- the **photo** itself, byte for byte, as Immich has it;
- an **XMP sidecar** (`IMG_1234.jpg.xmp`) with the rating, the location and the description, which Nextcloud, digiKam and others read (under the same switch as for other hand-overs, `CARRY_DETAILS`);
- a **record** (`IMG_1234.jpg.handoff.json`) with who sent it, the service, when, the source photo and its checksum, the checksum of what was written and **how the copy was checked**;
- for a **live photo**, the clip with the photo's base name (`IMG_1234.HEIC` and `IMG_1234.MOV`).

Stacks (a RAW and its JPEG) go as separate files. People, albums and tags do not travel.

**A name already there:** the same file (same content) is left alone and counts as delivered; a different file with the same name is written as `IMG_1234 (2).jpg`. Nothing is ever overwritten.

## What "the copy is checked" means

A photo counts as held only when the destination's own look at it agrees with the bytes that were sent. How much it can say depends on the kind, and the record says which it was:

- **folder** and **SFTP**: the file is written under a temporary name, **read back and compared by size and SHA-1**, then moved into place;
- **S3**: the size and the MD5 the store returns for the upload (or for each part of a large one). The ETag of a finished multipart upload is *not* a hash of the file, and is not used as one. A store whose ETag is not an MD5 (an encrypted object) is checked by size only;
- **WebDAV**: the size, and the checksum when the server gives one (Nextcloud and ownCloud can); many servers do not, and then the record says `size only`.

## When a destination is down

A destination that does not answer, is paused or has reached its own daily limit does **not** hold up the others: the other receivers and destinations take the photo now. But the **original stays, still tagged**, until every place holds it, and the next pass delivers only to the one that lacks it (the owner is told once when it stops answering and once when it is back). In copy mode the service tag stays too, so it can catch up. The owner can **pause** a destination, give it its **own daily limit** (photos and megabytes, besides the service's), or **remove** it, which lets the waiting photos go on without it. What a destination already received stays there.

## Taking a hand-over back

A sender can take back their last hand-over within 24 hours, as before. For a destination that means the files (photo, sidecar, record, clip) are **deleted**: there is no trash on a folder, a bucket or a server. The confirmation says so. If a destination cannot be reached, nothing is changed and the sender is told.

## Importing: following a destination

A destination can also be **followed**, the way an album shared with you is: on your page, under *Add to my library*, **Follow a destination**. The photos and videos that **appear** in it (a folder your phone syncs to, a bucket someone fills) are offered to you with pictures, **Add to my library** / **Not these** / **Always**, and after three *Always* the rule can add them by itself. Adding streams the file into **your own Immich with your own key** (the same key as for following albums, which also needs the permission to upload; you can delete it in Immich), with its XMP sidecar when there is one beside it, and Immich refuses a duplicate by checksum, so a file is never added twice.

- **Only reads.** Nothing in the destination is changed, renamed or deleted, ever.
- **From now on, or everything:** *Only what appears from now on* remembers what is there now and never offers it; *What is there now, and what appears* offers it all (up to 200 files a look).
- **A folder in it:** give a sub-folder to follow only that (a rule never reads outside it). A place with more than 200,000 files is too much to list every time it is looked at: the app says so, and you follow a folder inside it instead.
- **How it looks:** the app lists the destination every few minutes (a folder at a time on WebDAV, a page of a thousand at a time on S3, a walk of the folders on SFTP), tells new files by their name, and uses the time a file was last changed to leave out one that is still being written. A download that moves no data for three minutes is given up and tried again at the next look; nothing is lost.
- **What is offered:** photos and videos (JPEG, PNG, HEIC, RAW, MP4, MOV and so on). Sidecars, the records this app writes and files still being written are not. **A file with a record beside it is a hand-over of this app, and is not offered back.**
- **Pictures:** JPEG, PNG, GIF and WebP files are shown scaled; a video, a HEIC or a RAW shows its name and size instead.
- **Where the files go:** in your library tagged with where they came from (`from/<destination>`), plain, or in an album.
- **What you may follow:** the destinations you entered yourself (your own storage), and the administrator's only if the administrator opened them for it with `import: true` in `destinations.yaml`. Open one only if it holds nobody else's files: anyone who may use it can then read all of it (or the folder they choose) into their own library. WebDAV, S3 and SFTP are followed the same way as a folder.

## Worth knowing

- **A destination is outside Immich.** Who can read the files, and what happens to them, is up to the folder, the cloud or the server, not this app. Give a destination only to people you trust with the photos.
- **Each place holds a copy**, so the photos take room in every place (and the quota of a cloud applies to it).
- **Speed:** a photo is handed over when the app next looks (the tag's workflow wakes it at once; otherwise within the sweep).
- **Several destinations** per service are allowed (up to 8).
- The details of the choices are in the [design note](../../contributing/design/destinations-design.md) and the security side in [Security](../../reference/security.md#destinations).
