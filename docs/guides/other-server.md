# Receive on another Immich server

**Who it is for:** Owners whose receiver lives on a different server.

**What you get:** A service whose photos go to an Immich on another server, with the sender's app holding the key.

**What you see:** The photos arrive tagged `from/<sender>` on the other server.

## Receiving on another Immich server

Someone you share with may run an Immich of their own. A service can hand its photos to them as well: this is a **bridge**. It is off until an administrator sets `BRIDGE_ENABLE=true` ([Settings](../settings.md)).

1. On the service's card, press **Invite someone on another server**. You get a link, good for a week and for one connection; a new link replaces the old one. Send it to them.
2. They open it. **They need no login on your server.** The page says who is asking, tells them to make a key in their own Immich with four permissions (`user.read`, `tag.create`, `tag.asset`, `asset.upload`; and, if they wish, `asset.delete` so that a hand-over can be taken back, see below) and asks for their server's address, the key and a name.
3. Nothing is kept until the address is allowed and the server and key pass the checks: the server answers as a supported Immich, and the key has those four permissions (and at most the optional fifth). A key that can do more, **for example one that can read their library, is refused** with the reason, and so is one that can do less. Their mistake is corrected on the same page.

From then on every photo tagged for the service goes to **you and to them**. It arrives in their library with the tag `from/<the sender's name>`. The original is touched only after both of you hold the copy.

**What the other side cannot do.** The key can upload photos and put a tag on them, and that is all: it cannot read, search or download anything in their library. Photos arrive as separate photos (no stacks, no album), and **letting copies expire does not work across servers**: the app cannot see whether the receiver filed or kept a copy, and it would trash photos they wanted. The app cannot read the copy back to compare its checksum, so a copy is trusted on Immich's answer to the upload.

**Taking a hand-over back, if they allow it.** The receiver may add a fifth, optional permission, `asset.delete`, to their key (the page says what it allows before they connect; a new key and link are needed to change it). The sender's **Take back the last hand-over** then also moves the copies that last hand-over left on their server to **their trash** (never deleted for good, and only within the same 24 hours). The app remembers the ids it was given and reads nothing there, so it cannot tell whether the receiver has since filed a copy in an album: that copy goes to the trash as well, and the receiver can put it back from there. Without the permission, or once the receiver has removed it from the key (it is checked again at every pass and when a hand-over is taken back), the sender's own side is taken back as usual and the message says how many copies stayed on the other server. The service's card says which of the two applies. If their server cannot be reached, nothing is taken back and the sender can try again.

**When their server is away or the key is revoked.** Photos stay tagged in the senders' libraries, and **nothing is handed over by the service until it works again, to you either**, because a photo is only removed once every receiver has it.

- If their server cannot be reached, the card says **Cannot be reached**, you are told once (`NOTIFY_URL`), and the app tries again after a minute, then two, four and so on up to an hour (`BRIDGE_RETRY_MINUTES`). When it answers, waiting photos go and you are told that too.
- If their key was deleted or revoked, the card says **Key revoked**, you are told once, and **the app does not try again** until they make a new key and you send a new link.
- **Remove** on the card ends the connection; waiting photos then go to you alone, and what already arrived there stays there.

`immich-handoff doctor` checks the connection and says what is wrong. See [Security](../security.md#bridges-a-receiver-on-another-server).
