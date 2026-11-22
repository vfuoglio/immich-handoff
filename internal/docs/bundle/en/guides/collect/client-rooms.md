# Deliver a shoot to a client

Send your client a link to a room. They look at your album through pictures marked with your name, choose their favourites and leave notes, and you release the originals when you decide, after you have been paid, say.

<p align="center"><img src="../../images/room-client.png" alt="The client's page on a phone: the photos marked with the photographer's name, two chosen" width="260"> <img src="../../images/room-large.png" alt="One photo larger, with a note to the photographer" width="260"></p>

A client room is a kind of its own next to drop boxes. The client needs no account; the link, and a PIN if you set one, is all they have.

## Make a room

1. On the **Services** tab, in **Client rooms**, press **Make a client room**.
2. **Name it** (your name for it) and give an optional **title** for the client. Choose the **album**.
3. **How the pictures are marked:** text repeated across the picture (your name if you give no text), your logo in the bottom right corner (JPEG, PNG or WebP up to 300 KB), or both.
4. **Who chooses and how many photos at most**, whether clients may write **notes**, whether the room goes through several **rounds**, whether the originals are **released by themselves**, an optional **PIN** (4 to 40 characters) and how long the link works.
5. **Share the link** (it has a QR code). With a PIN the client needs both.

![A room's row on the Services tab: the link, who has chosen what, the buttons](../../images/room-row.png)

The photos are the album's: photos you add appear in the room within a couple of minutes, and photos you take out disappear. Photos and videos are shown in the order they were taken; archived and trashed ones never are. A room shows at most 2000 photos, and you can have 20 rooms. **Pause the room** to make the link show nothing for a while; **End room** deletes the link and the key at once.

## What your client sees

The link opens a page with the room's title and the marked pictures as a grid, numbered, in the order they were taken (a video has a small play mark and shows its marked poster picture). A tap opens a picture larger, and the arrow keys, the buttons or a swipe move to the next. Nothing on the page says whose Immich it is, which album it is or where the originals are. The page is in the client's language (the six the app speaks), works on a phone first and in both colour schemes, is never indexed or cached, and sends no referrer.

- **The PIN.** With a PIN the page shows nothing, not even the title, until it is given; it is then remembered in that browser for a week. Five wrong tries from one connection in ten minutes silence it for ten minutes more. While the room is paused the page says it is closed for now; once it has ended, or for a link that never existed, the answer is the same "not found".
- **The pictures are marked.** The client never sees a plain picture: the app fetches Immich's preview (about 1440 pixels on the long side), draws your mark on it and sends that. The text is repeated over the whole picture, so it cannot be cropped out. A picture that cannot be marked is not shown at all, never shown plain. A video is shown as its marked poster; the video itself comes with the release.

## Choosing, notes and rounds

- **Choosing.** Each photo has a heart; the client presses it to choose and again to take it back, and a counter says "12 of 30 chosen" (or "12 photos chosen" with no limit). The most photos is yours to set (1 to 5000, or none); beyond it the client is told, in words, to take one away first. **Send my choice to the photographer** finishes the choice: it is locked, shown read-only, and you are told through your notification settings. A client who sends fewer than the limit is asked first whether to go ahead. Without the script every press is a form that comes back to the page, so it works everywhere.
- **Who chooses.** **Everyone with the link, together**: one choice for the room (a couple choosing together), and the name each person gave is kept with each pick. **Each person on their own**: every visitor has a choice of their own and sends it on their own. **One named client**: the first visitor to give a name is the one who chooses; the others see the photos and the choice but cannot change it. A visitor is told apart by a secret their browser keeps (hashed with the room's link, never shown to you or anyone); the name is what they typed.
- **Notes.** Per room: **None**, **a short note to me on each photo**, or **notes, and I can answer each one**. A client writes a note in the large view of a photo ("brighten this one", "crop here"; plain text, up to 280 characters, up to 200 for one choice) and a small pencil marks the photo. Notes can be written until the choice is sent, then they are locked with it. You see each note under its photo on the review page; a photo that has only a note and was not chosen is marked **only a note**. Where replies are on, a form under the note sends your answer, which the client sees in the large view of that photo.
- **Rounds.** A room is made for **one round** or for **several**. In a room for several, once clients have sent their choices, the review page has **Start round 2**, with the most photos to choose in that round. In the new round each client sees **only the photos they chose in the round before**, the photos keep their numbers, and the choice starts empty with the new limit. Earlier rounds stay listed on the review page, and notes do not carry over. A room has at most five rounds, and **what is delivered is the last round's choice**; once you have released the originals the room cannot start another round (take the release back first).

## Look at the choice, and release the originals

- **What you see.** The room's row lists each selection (the name the client gave, how many photos, **Sent** with the time or **Still choosing**), and **Let them change it** gives a sent choice back to its client. When a client sends a choice you get a line in your Activity ("Chiara finished choosing in Rossi wedding: 28 photos"), a **Choice sent** card under *Needs you*, and, if you turned them on, a notification on your device or an email.
- **Look at the choice and release the originals** on the row opens a page with each selection: the chosen photos as the client saw them (marked), numbered as in the room, with their **file names** (and a box with them all, one on each line, for finding the photos in your own library).
- **Release the originals** is yours to press, with how long the downloads work (7, 30 or 90 days, or as long as the room works). Each client who sent a choice then sees **Your photos are ready** with a ZIP of the originals of *their own* photos (and a download on each), made while they download. **Take the release back** removes the permission and stops all downloads. A room gives out at most three ZIPs at a time and a client one.
- **Release by itself.** **Release the originals as soon as the client sends their choice** skips your step; the app still gives out nothing before a choice is sent.

![The review page: the chosen photos with their file names, a note, and the release](../../images/room-review.png)

## Good to know

- **The key cannot download.** The room has a key of its own in your Immich account that can only look at pictures and list an album's photos. It has no permission to download an original: that is added by your own session when you release the photos, and never before.
- **The key can look at any picture in your account** (Immich cannot limit a key to one album), so the app is what limits it; see [Security](../../reference/security.md#drop-boxes) for what is checked and tested.
- **A marked picture can still be photographed off a screen.** The mark makes a copy worth little, not impossible.
- **A client has no account.** The room cannot tell two people apart except by the browser they use, so a client who uses another browser starts a new choice (in a room where everyone chooses together they join the same one).

## If something is off

- **The client sees "not found".** The room has ended or been paused, or the link is wrong. With a PIN, check the PIN too.
- **A photo I added is not there.** It appears within a couple of minutes; an archived or trashed photo never does.
- **A client wants to change a sent choice.** Press **Let them change it** on the room's row.
- **I released too early.** **Take the release back** and every download stops.

## What next

- Collect photos from guests at an event: [drop boxes](events.md)
- Get a message when a choice arrives: [Be told](../show/notifications.md)
