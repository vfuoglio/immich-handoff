# Gallery, wall, best-of and what comes after the event

For a drop box that is also an event: let guests look at the photos and heart them, show them on a screen as they arrive, pick the best, and send everything on afterwards.

<p align="center"><img src="../../images/dropbox-gallery.png" alt="The gallery guests see under the form: a grid of the photos so far, with hearts and names" width="520"> <img src="../../images/dropbox-wall.png" alt="The wall: one photo full screen on a TV" width="520"></p>

The box is the event; there is no second thing to set up. It has dates (**Opens at** and **Closes at**, which you can **extend**, or **Pause** to close the link at once), a link, a short link and a printable card, an optional password, files that wait for approval, co-hosts and a moderator link: see [Collect photos from guests](events.md). This page adds what guests do and what you can do with what they leave.

## Save time with templates

**Templates and copies** on a box's row, and **Start from a template** under the list, help when you hold the same kind of event again. A template keeps a box's settings (message, limits, filing, approval, the look, how many days it stays open), not its name, link, password, cover photo, guests or co-hosts. **Save template** keeps the settings of a box under a name; **Start from a template** makes a new box from one, with the name you give and, if you want, when it opens and closes; **Make a copy** does the same from a box directly. Wedding, Birthday and School trip are there for everyone (School trip is photos only, approved by you, and shrunk to 4000 pixels); yours are private, at most twenty. A built-in template that needs approval makes a box without it on an install with no place to keep waiting files.

## The gallery

Switch on **Guests may look at the photos and heart them** and the guest page shows, under the form, **The photos so far**: a grid of every photo the box has kept, newest first, which fills in by itself every few seconds (and, in a box that approves files, as you approve them). Only photos in your library under the box are in it: a file waiting for approval, a rejected one and one a guest took back never are.

- **Hearts.** Each photo has a **heart** (one a guest, tapped again to take it back, with the count shown to everybody) and opens large when tapped.
- **Downloads.** With **and download them** each photo also has **Download**, which saves the original under the name it has in Immich.
- **Names.** With **and see who sent each photo** the page asks guests for **Your name (shown with your photos)** (remembered in their browser, at most 40 letters, no markup) and shows it under each photo they sent; without it no name is kept at all.
- **No account.** The page gives each browser a secret in a cookie, of which only a hash is kept, so hearts can be found again.
- **The pictures** are the ones Immich makes for its own screens, not your originals, and come through the app, which uses the box's key. For that the key gets `asset.view` (and `asset.download` if you allow downloads), added with your own sign-in when you switch the gallery on and taken off when you switch it off (the wall needs the first too, so it stays while a wall runs).
- **A gallery is for the people who have the link,** so give the box a password if the photos are private: **Upload** leaves the gallery open to anyone with the link, **The whole page** keeps it behind the password.
- **After the event.** The box's link still shows the gallery, with your thanks and no way to send, until what guests left goes (see [What is kept afterwards](#what-is-kept-afterwards)); guests can still heart photos and, if you allowed it, download them one by one or with **Download all as a ZIP** (the originals, made while they download; one at a time for a guest, three at a time for a box). A paused or revoked box shows nothing.

## The wall

A **wall** shows a box's photos full screen on a TV or a projector, one after another, as they arrive. Start it on the box's row under **Wall**; the screen opens the link it gives, with **no sign-in**, so nobody's session is left on a shared device. The wall has a link and a QR code of its own that can be renewed (**New wall link**) or ended (**Stop the wall**) without touching the box or its other links.

- **How it looks.** Choose with the buttons that appear when you move the mouse (or touch the screen): **Single photo** (new photos first), **Collage** (the newest nine, a little tilted) or **Grid** (the newest twelve). For single photos choose how one gives way to the next, **Fade** (the default), **Zoom**, **Slide** or **Rise**, and what is behind them, a faint **Glow** (the default) or plain **Black**; **Full screen** is there too. The address can carry the choices (`?mode=grid&fx=zoom&bg=black`), and a screen remembers what was chosen on it.
- **A new photo** is shown at once and stays a little longer, with a soft ring round it; in the grid and the collage the new one springs in and the others glide aside. Everything moves slowly and nothing flashes; a screen with little processor or memory (or that asks to save data) drops the zoom and the drifting glow by itself, and a browser that asks for reduced motion gets only a short dissolve. With no photo yet the screen shows a slowly breathing ring.
- **Captions.** A photo with a prompt (a [shot list](#a-shot-list)) shows its name, and when the box shows names so is who sent it ("The cake · by Ann"). **Guest-book messages** you mark **Show on the wall** appear now and then; none do until you choose them.
- **What it shows.** Only photos the box has put in the library: files waiting, rejected and taken back never show. For the pictures the box's key gets **one more permission** (to view photos), added with your own sign-in when you start the wall and taken off when you stop it. A wall keeps working after the box has closed, until you stop it. A video shows as the still Immich makes of it.

## A best-of

Under **Best-of** you can ask for a **suggested best-of set** of a box's photos. The app looks at each photo (through the small picture Immich made of it) and leaves out:

- photos that are **too dark** or **too bright**,
- photos that are **blurred** (little fine detail),
- and, of what is left, all but the sharpest of any shots of **the same moment**: pictures that look alike and were taken within an hour of each other.

What is kept goes into an album of yours called "*box name* best of". **Nothing is deleted or changed**, and the box's own photos stay where they are. **How much to leave out** is **relaxed**, **normal** or **strict**; running it again with another setting **corrects the album** (what is no longer kept leaves it, and anything you put in it yourself stays). **Why N photos were left out** lists each photo left out with its reason ("too dark, brightness 9 of 255", "the same moment as a better photo"), so you can overrule it by adding the photo to the album in Immich. It runs in the background and the row says how far it is (reload the page to see). Only the owner can ask for one, because it reads the photos with the owner's own sign-in. It looks at the photos' content only: no face recognition and no model, so it cannot tell a good moment from a boring one, only a technically poor shot from a good one. Photos Immich keeps only as WebP previews cannot be looked at and are kept. It can be run at any time.

**Guests' hearts and your picks.** Where the box has a [gallery](#the-gallery):

- **Loved by guests.** **Make the album of loved photos** (under **Loved by guests**) puts the photos that have a heart, and the ones you pinned, in an album of yours called "*box name* loved by guests": the pinned first, then the most hearts, at most a hundred, never a photo you hid. Run it again to correct the album (what is no longer loved leaves it, what you added yourself stays).
- **Pin and hide.** On the [moderator page](events.md#a-moderator-link-for-a-helper-with-no-account) (make a moderator link for yourself if you have none), **The photos kept** shows every photo the box kept, newest first, with its hearts and who sent it, and two buttons on each: **Pin** (always in a best-of, whatever its brightness or sharpness, and first in the loved album) and **Hide** (guests no longer see it in the gallery, on the wall or in their ZIP, can no longer heart or download it, and a best-of leaves it out, saying "hidden by the host"). A hidden photo stays in your library and in your own ZIP, and the moderator still sees it, dimmed, with a button to show it again. This section appears only where the box's key may look at pictures, which it may while the box has a gallery or a wall.

## A shot list

Give a box a **shot list** (one prompt per line, at most twenty, each up to 60 characters, no slash or comma: "The first dance", "The oldest guest", "The best hat") and the guest page asks "What is the photo for?" with the list to choose from, and shows it as a checklist with a tick on each prompt that already has a photo, so guests can see what is still missing. Tick **Every photo must say which one it is for** to make the choice compulsory; a photo with none is refused.

With a list, a photo for a prompt is tagged `inbox/<box>/<prompt>` as well as `inbox/<box>`, so **Immich lists the photos grouped by prompt** (open the tag in Immich). Each prompt on your page shows how many photos came for it. Edit the list later under **Shot list and message to the host** (the owner and co-hosts who manage): a prompt that stays keeps its photos and its count, a new one starts at none, and photos already tagged keep their tags. Each guest chooses a prompt for the files they add at that moment, so one visit can cover several prompts. A prompt is shown as written; it is not translated per guest.

## Message to the host

Switch on **Message to the host** and the guest page has a form: a name (required), a few words (up to 1000 characters) and, on a browser that can record, a **voice message** of up to 90 seconds. A message needs words or a voice note, or both.

**Messages and voice notes are not photos, so they are not put in Immich: they are kept in this app's own data file** (and so in its backups), and only the owner and the co-hosts who manage the box can read and play them, on the box's row under **Messages to the host**. A viewer co-host cannot. The owner can remove a message, or **download the messages** as a zip with every message, oldest first, and each voice note as a file. A box holds at most 500 messages, a voice note is at most 2 MB, and the file's first bytes must say it is audio (WebM, Ogg, MP4/M4A, WAV or MP3). Without a script the form still sends the words. Recording needs the browser's permission to use the microphone, and HTTPS.

## Guests taking back what they sent

Switch on **Guests may take back what they sent, while it is still in the inbox** when you make a box. The guest's browser then keeps a secret (a cookie, `HttpOnly`; the server stores only a hash of it), the page links to **your uploads**, and each of a guest's files has **Take back**. A file that still waits for your approval is deleted. A photo already in your library goes to **your trash** (never deleted for good) if it still carries the box's inbox tag; **once you have taken that tag off or filed the photo, it is yours**, and the guest is told to ask you. A guest sees only what their own browser sent, and only in that browser.

For this the box's key also gets `asset.read` and `asset.delete`, so it is the one kind of box whose key can move a photo to the trash. It is off unless you choose it, and it cannot be switched on later for a box that exists (make a copy instead). Taken-back files are counted in the summary and the log.

## Share the photos back

When the event is over, **Share the photos back with the guests** (the owner only, on the box's row) gives them something in return. It puts what the box received (every photo under its inbox tag) into an album of yours, the box's own album if it has one, otherwise one named after the box, and makes a **link in Immich's own shared-link page**, view-only, that anyone who has it can open **without an account**, to look at the photos and download them one by one or all at once. You choose how many days it stays open (1 to 365), an optional password, and a thank-you message that Immich shows with the album. It cannot upload or change anything. Copy the link, a ready-written message or its QR code and send them to the guests. The app does it with **your own session**, so the box's key gets no new permission. Press the button again later to add what came since: the new link replaces the old one. **Stop sharing** ends the link; the album and the photos stay in your library.

Two things to know. **Immich only allows downloads on a link that also shows the photos' details** (dates, places, cameras), so this link does: guests who open it can see those details of every photo, not only their own. And the box's own link, once the box has closed, **leads to the photos** while a shared link is open (a guest who opens their old link sees your thanks and a **See the photos** button); a closed box that shared nothing still looks like a link that does not exist. The gallery and a shared link are independent: end either without touching the other.

## Download everything as a ZIP

On the box's row, **Download every photo as a ZIP** (the owner only) makes a ZIP of the **originals** of every photo the box kept, while you download it, so nothing is stored on the server and a large event can take a while. It is made with your own sign-in, so the box's key gets no new permission, and photos you have since deleted from your library are left out. A name that two photos share gets a number (`IMG_1 (2).jpg`). One ZIP of a box is made at a time.

## Handing the photos on

When the event is over the whole set can go through the hand-over that already exists, to a service you send to: someone else's library, a print service. Under **Hand the photos on** (the owner) choose the **service**, one you have joined as a sender, and then **Hand everything on now** or, once you have made a best-of, **Hand the best-of on now**.

The app **tags the photos with the service's tag in your own library**, which is what hands a photo over, so all the usual rules apply: the service's daily limit, quiet hours and accept rules, the receiver's copy that expires, and the sender's **Take back the last hand-over**. If you chose to keep your originals when you joined the service they stay; **if you chose to move them, they go to your trash once handed over** (the list shows which each service does). Tick **Hand everything on by itself when the box closes** and it happens once, when the box closes, with everything (never the best-of: that waits for you to look at it); the box's closing summary and its log say how many photos went and where, or why they could not. Each time is kept in the list under the box, with when, what, where and by whom.

## What is kept afterwards

What guests leave with the box (the gallery, and the names and hearts in it) goes on the day the retention ends (**Keep what guests leave for**, see [the settings](events.md#what-you-can-set)); the box's own log is not touched by it. The box shows the date on its row under **The event: what is kept afterwards**. On that day (the app checks every few minutes) the names given with photos and every heart are deleted, once, and the log says so; the photos, which are in your library, are not touched, and the gallery closes on the same day. With **0** days nothing goes until you revoke the box, which deletes them at once. The closing summary (the log and `NOTIFY_URL`) also counts the hearts given.

## Good to know

- **Your own look.** The colour, cover photo, logo and title guests see (set under [The look of the guest page](events.md#the-look-of-the-guest-page)) are carried by the guest page, the gallery after the event and the wall; the installed app takes the title and the colour. Guests never see the app's name or any credit line. A template saves redoing them for a client's event (colour and language are in a template; title, logo and cover photo are per event).
- **Your album.** What arrives is tagged `inbox/<name of the box>`, and with **Album** (see [the settings](events.md#what-you-can-set)) it is also put in an album of yours, made when the first photo arrives. That is your album of the event.
- **Protection and the log** are described in [Collect photos from guests](events.md#good-to-know); more on what a link allows is in [Security](../../reference/security.md#drop-boxes).

## If something is off

- **The wall shows nothing.** No photo has been kept yet, or files are still waiting for approval. A paused or revoked box shows nothing.
- **Guests cannot see the gallery.** It is off by default: tick **Guests may look at the photos and heart them** (a box that already exists can change it under **The event: what is kept afterwards**). A paused or revoked box shows nothing.
- **The best-of left out a photo I like.** **Why N photos were left out** says why; add it to the album in Immich, or **Pin** it.
- **A guest asks to take back a photo.** Only if you switched on taking back when you made the box; otherwise they ask you.

## What next

- Hand the photos on, or make a recap: [Stories](../show/stories.md)
- Show them on the big screen: [Cast to a TV](../show/cast-to-tv.md)
- Deliver a shoot to a client: [client rooms](client-rooms.md)
