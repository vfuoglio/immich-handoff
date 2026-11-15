# Share albums across servers

**Who it is for:** Two people on different Immich servers.

**What you get:** Photos both ways between two libraries, with groups of up to ten, and a way that works when the other person has no Handoff.

**What you see:** The same album filling on both sides, with a row on each page showing how it is going.

## Albums shared with other servers

For people whose Immich is on **another server** than yours (a relative who runs their own, say), Handoff can keep an album the same in both libraries: what either of you adds appears at the other, as a real copy in each library. Each person runs a Handoff next to their own Immich and the two apps talk to each other; **each app only ever touches its own Immich**, and no Immich key goes to the other server. The administrator switches it on with `SHARED_ALBUMS_ENABLE` (see [Settings](../settings.md)); it also needs `PUBLIC_APP_URL`, and the two apps must be able to reach each other over https.

There are three ways, each described below: **two Handoffs** (the normal way: [connecting](#connecting-with-someone), then [agreeing on an album](#agreeing-on-an-album)); **a group** of several servers, through the person who made the album ([more than two people](#more-than-two-people-a-group-through-the-creator)); and **one app only**, for a relative who has an Immich and will not run Handoff ([sharing with someone who has no Handoff](#sharing-with-someone-who-has-no-handoff), which does not need `PUBLIC_APP_URL`). Whatever the way, nothing leaves your library except the photos of the albums you agree to share, and ending the sharing is always one button.

### Connecting with someone

1. **Invite someone** (Services tab, *Albums shared with other servers*) makes an **invitation link**, with a QR code. Give it to the other person; it works once, for a week.
2. The other person opens **their own Handoff**, chooses **Join with an invitation link**, pastes it and presses **Look at the invitation**. The app asks the inviter's app who it is from and says so (name and address); **Accept the invitation** connects the two.
3. Opening the link in a browser only explains what it is for and joins nothing.

From then on each app shows the connection with its state (**Connected**, **Paused**, **Not reachable**, **Ended**). Either person can **Pause sharing** (the other app shows it) or **End connection**, which tells the other app; photos already in either library stay where they are. A connection is not made unless the inviter's app can call the other app back, so an app that cannot be reached from the internet cannot join.

### Agreeing on an album

On a connection, **Share an album with {name}** proposes one: you give it a name (what it is called between the two of you), choose **which of your albums** (one you have, or a new one) and choose two things:

- **What do you share at the start?** *Only what I add from now on* (the photos already in the album stay yours alone) or *everything in the album now, and what I add*. Each side makes this choice for itself when it joins.
- **When someone takes a photo out of the album:** *nothing happens on the other side* (the default; each of you keeps what you have) or *it is taken out of the other album too* (the photo itself stays in the library). Deleting a photo never travels. This one is the proposer's to set and the other person agrees to it by accepting.

The other person sees the proposal on their own page, with what you share and what removing does, chooses their own album (or a new one) and what they share, and **Accepts** or **Declines**. Either side can **End sharing of this album** at any time (the other app is told; photos already copied stay), and ending the connection ends all its albums.

For each shared album the app makes a **key in your own Immich** (named "handoff: shared album ..."), through your own sign-in, which can read the album and the originals in it, put photos in it, make it and take photos out of it, and nothing that deletes a photo or shares anything (`user.read`, `asset.read`, `asset.download`, `asset.upload`, `album.read`, `album.create`, `albumAsset.create`, `albumAsset.delete`). Immich cannot limit a key to one album, so the app does; the key is deleted when you end the sharing. What you choose to share at the start is remembered by the app (as a list of what the album held), so "only what I add from now on" really sends nothing that was there.

### How the photos move

Each app looks at its own copy of the album **every few minutes** (`SHARED_ALBUMS_EVERY_MINUTES`, three by default; Immich sends no event when a photo is added to an album) and sends what is **new** to the other app, which stores it in its own Immich and puts it in its own album. Both ways is the same thing done on both sides, so a photo added on either side appears on the other within a few minutes. **Sync now** on the album's row looks at once.

- **Nothing comes back.** What arrived is remembered, by the photo's checksum (the same file has the same checksum on both servers), as arrived and is never sent back. If the same photo is already in the other person's library it is not stored again, only put in the album; if both of you have the same photo it is not copied at all.
- **Their deleting is respected.** A photo the other person has put in their trash is left in the trash and is not put in the album, whether or not it is sent again.
- **Originals, whole.** Photos and videos are streamed from one app to the other as the original file, without being kept on either server's disk, with their name and the date they were taken. A file larger than `SHARED_ALBUMS_MAX_MB` (2048 by default) is left out, with a reason on the album's row, and is not tried again.
- **Taking a photo out.** With the rule *it is taken out of the other album too*, a photo you take out of the shared album is taken out of the other person's album within a few minutes (and it is not sent back). Only the album changes: the photo stays in both libraries. A photo you **delete** or put in the trash never travels, and neither does a photo the other side took out under the rule *nothing*. If you take out a lot at once (more than five photos, or a quarter of the album), nothing is told until you answer on the album's row: *Yes, take them out there too* or *No, leave them there*. A photo you put back in the album is shared again.
- **Pausing and problems.** While either side has paused the connection nothing moves. The row shows when the album was last looked at, how many photos went each way and, if the last look found a problem (the other app could not be reached, a file was too large), what it was; the next look tries again.
- **What you are told.** In your Activity (and on your devices or by email, if you asked for those): when photos arrive from the other person (one line for a whole pass: \"3 photos arrived from Anna in Summer\"), when someone proposes an album, answers, or ends the sharing, and **once** when sharing stops working (the other app cannot be reached, a file was too large) and once when it works again, not at every look. The Overview's *Needs you* shows a proposal waiting for your answer, a connection that is not reachable, a sharing the other person ended and an album whose last look found a problem. `immich-handoff doctor` checks the settings (`PUBLIC_APP_URL` set and https, a warning only if it is missing because the one-app way does not need it, the two numbers).
- **What is sent is only the photos of that album** that are new on your side: what arrived, and, if you chose "only what I add from now on", what was there when you began, are never sent.

### More than two people: a group through the creator

An album can be shared with **several people**, up to ten, with no extra setting: the person who made it (the creator) shares **the same album** with each of them, one connection each. The creator's app passes the photos on: what one member adds arrives in the creator's album and is sent from there to every other member, and the other way round. Members never connect to each other.

- **Nothing loops and nothing is copied twice.** Each connection remembers, by checksum, what arrived over it and never sends it back; a member who already has a photo (even one they added at the same moment as someone else) does not get a second copy.
- **A member sees the others' names, nothing more.** Under their album's row it says *Also in this album, through {creator}: ...*, and the proposal they accept says that other people may join. The creator's row says whom the album is passed between.
- **A member leaving** ends only their own connection; the others carry on, and the creator's page shows *Ended by {name}* until they remove it. The photos already copied stay everywhere.
- **If the creator's server is off,** the members' photos wait and are sent when it is back (the row says the other app could not be reached); nothing is lost, but nothing moves between members in the meantime.
- **The limit** is ten people per album (a proposal beyond that says so). With the rule *taken out of the other album too*, a photo the creator's app takes out because one member did is taken out for the other members that agreed to it, within a few minutes.

### Sharing with someone who has no Handoff

For a relative or a friend who has an Immich and will never run Handoff, **Share an album with someone who has no Handoff** (on the Services tab, under *Albums shared with other servers*) lets this one app work on both sides. You need the address of their Immich and a **key they make on their own Immich** (Account settings, API keys) and give to you; give them the exact permissions below, and tell them they can delete the key at any time, which ends the sharing at once. The page shows the same list.

- **To only send them the album:** `user.read`, `asset.upload`, `album.create`, `albumAsset.create`. The key can put photos into their library and make an album; it cannot read, search or delete anything. Your album's photos are copied into a new album on their Immich named like the shared album; nothing comes back.
- **To share both ways:** those four plus `asset.read`, `asset.download`, `album.read`. Photos they add to that album come to yours, and what you add goes to theirs, never echoing back. If they already have an album with that name, it is used and what is in it comes to you; otherwise a new one is made. **Immich cannot limit a key to one album, so this key can read their whole library**; the app only ever asks for the one album and copies only what is in it. That is a trust decision for them, which is why the page says it plainly.
- **A key with any other permission is refused**, and so is one with fewer (or with only some of the reading permissions): the rule is exactly what is needed, as for the bridges. The app checks that the key still works every ten minutes and says on the connection if their Immich stops accepting it.
- **What is different from two Handoffs:** taking a photo out of the album does not travel (the small key cannot see what is in the album, and the bigger one is kept as small as it can be), there is no proposal to answer (they agreed by giving the key), and the other person is not told by an app when photos arrive. **Their** deleting is respected as far as the key can see it: with the two-way key a photo in their trash is not put in their album. Everything else (what you share at the start, the checksum ledger so nothing is sent twice, the size limit, **Sync now**, the row's problems and the activity lines) works as above. This needs no `PUBLIC_APP_URL`, because nothing calls your app.
