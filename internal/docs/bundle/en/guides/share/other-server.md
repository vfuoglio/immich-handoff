# Receive on another Immich server

A service can hand its photos to someone who runs an Immich of their own, on another server: this is a **bridge**. They need no login on your server and they do not install Handoff; the sender's app holds the key.

![The service card with the button to invite someone on another server](../../images/bridge-card.png)

## Connect them

A bridge is off until an administrator sets `BRIDGE_ENABLE=true` ([Settings](../../reference/settings.md)).

1. On the service's card, press **Invite someone on another server**. You get a link, good for a week and for one connection (a new link replaces the old one). Send it to them.
2. They open it. The page says who is asking, tells them to make a key in their own Immich with four permissions (`user.read`, `tag.create`, `tag.asset`, `asset.upload`; and, if they wish, `asset.delete` so a hand-over can be taken back) and asks for their server's address, the key and a name.
3. Nothing is kept until the address is allowed and the server and key pass the checks: the server answers as a supported Immich, and the key has those four permissions (and at most the optional fifth). A key that can do more, **for example one that can read their library, is refused** with the reason, and so is one that can do less. Their mistake is corrected on the same page.

From then on every photo tagged for the service goes to **you and to them**. It arrives in their library tagged `from/<the sender's name>`, and the original is touched only after both of you hold the copy.

## Good to know

- **What the other side cannot do.** The key can upload photos and put a tag on them, and that is all: it cannot read, search or download anything in their library. Photos arrive as separate photos (no stacks, no album). The app cannot read the copy back to compare its checksum, so a copy is trusted on Immich's answer to the upload.
- **Letting copies expire does not work across servers.** The app cannot see whether the receiver filed or kept a copy, and it would trash photos they wanted.
- **Taking a hand-over back, if they allow it.** With the optional `asset.delete` permission (the page says what it allows before they connect; changing it needs a new key and link), the sender's **Take back the last hand-over** also moves that hand-over's copies on their server to **their trash**, within the same 24 hours. The app reads nothing there, so it cannot tell whether the receiver has since filed a copy in an album: that copy goes to the trash too, and the receiver can put it back from there. Without the permission, the sender's own side is taken back as usual and the message says how many copies stayed on the other server. The service's card says which applies. If their server cannot be reached, nothing is taken back and the sender can try again.

## If something is off

Photos stay tagged in the senders' libraries and **nothing is handed over by the service until it works again, to you either**, because a photo is only removed once every receiver has it.

- **Cannot be reached.** The card says so, you are told once (`NOTIFY_URL`), and the app tries again after a minute, then two, four and so on up to an hour (`BRIDGE_RETRY_MINUTES`). When it answers, waiting photos go and you are told that too.
- **Key revoked.** Their key was deleted or revoked. The card says so, you are told once, and **the app does not try again** until they make a new key and you send a new link.
- **Remove** on the card ends the connection; waiting photos then go to you alone, and what already arrived there stays there.
- Run `immich-handoff doctor` to check the connection; it says what is wrong.

## What next

- Albums that fill on both servers: [Share albums across servers](shared-albums.md)
- What the key can and cannot do: [Security](../../reference/security.md#bridges-a-receiver-on-another-server)
