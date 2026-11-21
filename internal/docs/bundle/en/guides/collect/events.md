# Collect photos from guests who have no Immich

**Who it is for:** Hosts of a wedding, party, club night or school event.

**What you get:** A link, QR code or email address that only *receives*. Guests need a browser, nothing else.

**What you see:** The photos in your library, tagged `inbox/<name of the box>`; the counts and log on the page.

## In a few steps

For a wedding, a birthday, a club night. You run Immich and have [installed Handoff](../../admin/install.md). Your guests need only a phone and a browser.

1. **Make the box.** Open the page, go to **Services**, then **Make a drop box**. Name it after the event, write a short message for guests, and set when it opens and closes (for example Saturday 14:00 to Sunday 12:00).
2. **Choose what guests may do.** Tick **I look at every file before it is kept** if you want to approve photos first, and **Guests may look at the photos and heart them** for a gallery. Leave the rest as it is; every setting is [explained here](events.md#drop-boxes-photos-from-people-who-do-not-have-immich).
3. **Share the link.** Copy the link or message, or show the **QR code** on a card or a screen. Guests open it, choose photos and send them. They never see your library.
4. **Watch them arrive.** Photos land in your library tagged `inbox/<name of the box>`. Start a [wall](events-extras.md#the-wall) on a TV for the room to see them come in.
5. **Afterwards.** Make a [best-of](events-extras.md#a-best-of) of the photos guests loved, download everything as a ZIP, or [hand the photos on](events-extras.md#handing-the-photos-on) to someone's library.

**What you see:** the photos in your Immich, the counts and the log on the page, and, if you chose to approve, a card on the Overview waiting for your answer.

**Worth knowing:** the same photo sent twice is stored once; a file is checked by its content, not its name; a password can protect the upload or the whole page. See [Security](../../reference/security.md#drop-boxes) for what a guest can and cannot do.

## Drop boxes: photos from people who do not have Immich

A drop box is a link for people without an Immich account: relatives at a wedding, guests at an event. You make one on the **Services** tab, under **Make a drop box**, and share its link, a message with it, or its QR code. Guests open it in any browser, choose photos and send them. What arrives goes into **your** library, tagged `inbox/<name of the box>`, to look through before you file it. Nothing else changes: it is not a service, nobody has to be approved, and there is no sender.

**What arrives shows in your Activity list**, as one line for each burst that is kept up to date ("12 photos arrived in Mia's birthday"), so you can see guests sending without opening the box. It is a line of the log only: nobody is notified for each photo.

**The Overview shows it live.** Each drop box is a stage in the live scene, like a service: the guests' drops fly from the left, named by whoever sent them (the name a guest gave, if the box shows names, otherwise *A guest*), into the box, and the total goes up by the number of photos in the drop. A **+N** fades in over the total as the photos reach the box and out again once it has gone up.

What a guest sees: the name of the box, your message, a form to choose photos (and videos, unless the box is for photos only), and afterwards what became of each file. **They cannot see your library, what other guests sent, or even what they sent themselves once they leave the page**, unless you switch on the [gallery](events-extras.md#an-event-from-the-first-guest-to-the-last-photo). Immich's own shared link with uploads switched on shows guests the whole album; a drop box does not.

What you set, all optional:

| Setting | What it does |
|---|---|
| **Open for (days)**, **Opens at**, **Closes at** | When the link works. By default it opens now and stops after the days you give (at most 365). Set **Opens at** to open later, and **Closes at** for an exact end (it replaces the days), for example Saturday 14:00 to Sunday 12:00. Times are in the app's time zone (`TZ`). Before it opens and after it closes the link looks like one that does not exist. |
| **Message for guests** | Shown above the form. |
| **Keep what guests leave for (days after it ends)** | How long what guests leave *with the box* (their names, their hearts and the gallery, see [An event](events-extras.md#an-event-from-the-first-guest-to-the-last-photo)) is kept after the box ends. `0` keeps it until you revoke the box; the default is 30. The photos in your library are never touched. Changed later by the owner under **The event: what is kept afterwards**. |
| **Guests may look at the photos and heart them**, **and download them**, **and see who sent each photo** | The [gallery](events-extras.md#an-event-from-the-first-guest-to-the-last-photo). Off by default: a gallery shows every guest every photo the box has kept. Changed later by the owner under **The event: what is kept afterwards**. |
| **Largest file / most megabytes / most files** | The most one file may weigh, and the most the box takes in all. A file that would go past the total is refused before it is stored. `0` is no limit. |
| **Album**, **Another tag** | Where what arrives is filed besides the `inbox/<name>` tag: an album of yours, chosen from the list or a new name typed (made when the first photo arrives) and a tag of your own, for example the event's name. Choosing an album gives the box's key permission to find, make and add to albums, nothing more. The same photo sent twice is stored once: Immich recognises it by its checksum, the second copy is not kept, and the guest is told it was already there. |
| **Shot list**, **Every photo must say which one it is for** | See [A shot list](events-extras.md#a-shot-list). |
| **Message to the host** | See [Message to the host](events-extras.md#message-to-the-host). |
| Wall, best-of, hand the photos on | See [The wall](events-extras.md#the-wall), [A best-of](events-extras.md#a-best-of) and [Handing the photos on](events-extras.md#handing-the-photos-on). |
| **I look at every file before it is kept** | See [Approving files before they are kept](#approving-files-before-they-are-kept). |
| **Guests may take back what they sent** | See [Guests taking back what they sent](events-extras.md#guests-taking-back-what-they-sent). |
| **Photos only** | Videos are turned away. |
| **Secure with a password...** | A menu: **No**; **Upload** (anyone with the link can see the page, and sending needs the password); or **The whole page** (the link shows only a request for the password: nothing of the box, not even its name, until it is given; the right password is remembered in that browser for a week). Choosing Upload or The whole page needs a password typed beside the menu, at most 64 characters. Five wrong tries from one guest in ten minutes silence that guest for ten minutes. A wall you start has its own link and is not covered by The whole page. A box with a password cannot take photos by email. |

What a drop box checks: it looks at the first bytes of every file, not at its name, and takes only what Immich can show as a photo or a video (JPEG, PNG, GIF, WebP, BMP, HEIC, AVIF, TIFF and DNG, MP4, MOV, WebM, 3GP). Pages, programs and archives are turned away and the log says so. A file is stored under a clean name with the extension its content calls for.

From the box's row you can see what it has taken so far (files, guests, the busiest hour), **Pause** it (the link then looks like one that does not exist), **Resume** it, copy its link or message, show its **QR code**, read its **log**, and **Revoke** it. Revoking ends it for good: the link stops working at once and the key is deleted from your Immich account. The photos already received stay in your library, and the log stays readable for a month.

### Approving files before they are kept

With **I look at every file before it is kept**, nothing a guest sends reaches your library until you say so. The file is checked as usual, then kept on the server's disk (in the data folder, under `dropbox-pending`, and not in backups) and shown to you on the box's row as a thumbnail with its name and size. Tick the ones you want and press **Approve chosen** or **Reject chosen**, or **Approve all** or **Reject all**. An approved file is stored in your library and filed (tags and album); a rejected one is deleted and its room goes back to the box's limits. Files nobody decides on are deleted after two weeks, and at most 1000 files or 10 GB wait in one box; past that the box tells guests it is full. Guests are told their files will be looked at, and see "waiting for approval" next to each. This needs the app to have a data folder (self-service switched on). Thumbnails are shown for photos; a video appears as a label with its name and size.

### Co-hosts

An owner can ask other people on the same Immich server to help with a box: under **Co-hosts** on the box's row, give their email and choose **Approve, pause and extend** or **Only look**. They find the box on their own Services tab. A co-host who manages can approve and reject files, pause and resume the box, extend it and see its log and link; one who only looks sees the summary and the log (and that files are waiting, not what they are), and not the link. **Nobody but the owner can end the box or choose co-hosts**, and no co-host ever sees your library or the key: they act through the app, which uses the box's key. A co-host can stop helping at any time from the box's row, and the owner can remove one. A box has at most ten.

### Moderator link

For a helper who has no account (a friend at the party), the owner can make a **moderator link** under **Moderator link** on the box's row: **Make a moderator link**, then copy it, show its QR code or open it. The page behind it, `…/mod/<long random token>`, needs no sign-in and shows the box's name and the files that wait for approval as thumbnails (the first 48; the rest appear as these are decided). The helper ticks files (or **Choose all**) and presses **Approve chosen** or **Reject chosen**, or **Approve all**; the page says how many were done. It is plain forms, so it works on any phone, with or without a script, and it does not refresh by itself. What the link does not show or do: your library, the box's link or settings, its log, its key, or anything about pausing, extending or ending the box; and it never shows a file that is not waiting in this box. It does the same as a co-host who manages does when deciding (an approved file is stored in your library and filed; a rejected one is deleted and its room goes back to the box), and the log says "A moderator approved 3 files". **Anyone who has the link can decide on the files**, so give it to people you trust; **New moderator link** ends the old one and makes another, **End the moderator link** ends it at once, and revoking the box ends it too. Only the owner makes or ends it. The link works as long as the box exists, also after it has closed, and a box that needs approval needs a data folder, like any such box.

Whenever files start to wait in a box that approves them, the owner is told once through `NOTIFY_URL` and `NOTIFY_FILE` (kind `dropbox`) and told again when files wait after the queue had been emptied, not for every file.

### Closing time

A box with an end reminds and sums up. About a day before it closes, `NOTIFY_URL` is told (so you remember); when it has closed, the log and `NOTIFY_URL` say what it took ("42 files (310.5 MB) from 12 guests"). On the box's row, **Extend by a day** or **by a week** (the owner and co-hosts who manage) moves the end later, from the old end or from now if it already ended, and opens the box again; extending resets the reminder and the summary. A box can never be set to end more than a year from now.

### The guest page

On a phone the page offers the camera ("Or take a photo now"); on a computer you can drop files anywhere on the page (the drop area wakes up while a file is over it). Every chosen file gets a tile with a small picture of it, made in the guest's own browser and never sent anywhere, and a **×** to take it out before sending; **Remove all** clears the lot, and the page shows how many files and how many megabytes are about to go. A box that takes photos only says so for a video instead of queueing it. **Send** always answers: it asks for photos when none are chosen, shows "Sending 2 of 5" while it works, and ends with a short message in place (how many were sent, or how many could not be and a **Try again**). Each file is sent on its own, with a progress bar on its tile, and the page says what became of it. **Interrupted uploads carry on:** if the connection drops, the page waits for it (it notices when the phone is back online) and tries the file again, up to eight times with growing pauses; a file that cannot be sent is marked, and the others are not held up. The page also remembers, in the guest's browser, which files it has already sent, so reloading and choosing the same files again skips them. This works file by file: a file cut off halfway is sent again from its start, not from where it stopped, and a photo sent twice is stored once anyway (the library recognises it). Without JavaScript the page is a plain form that still works.

Under **The look of the guest page** on a box's row you can set:

| Setting | What it does |
|---|---|
| **Colour** | The page's accent (`#rrggbb`), also the colour of the installed app's bar. Whatever colour you pick is made to read: text on it (a button) is black or white, whichever reads better, always at least 4.5 to 1 (the WCAG measure for small text), and text *in* the colour (links, small headings) is the colour made just dark enough on a light page and just light enough on a dark one to reach 4.5 to 1. So a pale yellow stays a pale yellow button, with its links in a darker yellow. |
| **Title guests see** | The name guests see, on the page, the album, the wall and under the installed app's icon. Empty: the box's name. The box's name stays what you see on your own page and what the inbox tag and the album are made from (`inbox/<name>`), so you can call a box `rossi-2026-06` and show guests "Anna & Marco's wedding". At most 60 letters. |
| **Logo** | A JPEG, PNG or WebP of up to 300 KB (not an SVG, which can carry script), shown above the name on the guest page and the album, and in a corner of the wall. |
| **Language** | The language of the page for guests who have not chosen one. A guest's own choice in the language menu wins. Default: their browser's. |
| **Cover photo** | A JPEG, PNG or WebP of up to 900 KB above the name. |
| **Shrink photos larger than** | 1600, 2400 or 4000 pixels, or never. Done **in the guest's browser** before sending, which saves their data and your storage. A shrunk photo is re-saved as a JPEG and **loses its metadata** (date, place); a photo the browser cannot decode (HEIC in most browsers) is sent as it is, and a shrunk file is never bigger than the original. It is off by default: leave it off to keep every photo exactly as taken. |

The page can be **installed** on a phone ("Add to home screen") under the box's name and colour, like the app's own pages; it only works while the box is open.

### More ways in

A box has a long link, which cannot be guessed, and can have more:

- **A short link** (**Make a short link**): ten letters and digits, `.../d/xxxxxxxxxx`, easy to type or read out. It opens the same box. It is easier to guess than the long link, so for a box that must stay private give it a password too. **Remove short link** ends it and leaves the long link and anything printed with it working; **New long link** (the owner only) ends the long link, and every card printed with it, and leaves the short one working. The long link, the short one and the printed cards are ended one at a time.
- **A printable card** (**Printable card with a QR code**): a page with the box's name, your message and a QR code of the link, to print for table cards or a poster (the print dialog leaves out everything else). There is one for the short link too.
- **By email** (**Give this box an email address**), if the person who runs the app set up a mailbox (see [Photos by email](../../admin/operations.md#photos-by-email)): the box gets an address such as `drop+k3m9x2tqaw@example.org`, shown with a copy button and on the printable card. Photos and videos attached to a mail sent to it arrive like uploads, under the box's limits, approval, filing and log; the text of the mail and anything that is not a photo or video is dropped, and **nobody gets a reply**. Anyone who knows the address can send, like the link, so a box with a **password cannot have an address** (nobody can type a password into a mail). The log says "A mail brought 2 files" with the same short mark as for any guest; the sender's address is never kept. Attachments smaller than 8 KB (the icons in a signature) are not taken. **Remove email address** ends it; photos already received stay.
