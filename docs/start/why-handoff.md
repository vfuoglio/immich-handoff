---
title: "Why Handoff, and what it does"
feature: why-handoff
audience: user
goal: start
order: 2
tip: "What Handoff does that shared albums and links do not, and why you can trust it."
---

# Why Handoff, and what it does

Immich keeps every photo in the account that uploaded it. Shared albums and links let people *look* at your photos; partner sharing is read-only. None of them puts a photo **in someone else's library**, collects photos from people who have no account, or connects two servers. Handoff adds that step. Ready to try it? Start with the [quick start](quickstart.md).

## What it does, and why you can trust it

- **Nothing new to learn.** Send by adding a tag in the Immich app you already use, or with the button inside Immich, on a phone or the web.
- **You decide about your own originals.** When you take part you choose: **keep** your photo and send a copy, or **move** it to your trash once the other side has it. The choice is yours, not the receiver's.
- **Anyone can offer a service.** Create one, decide who may send (people you approve, or anyone), and share an invitation link if you want it private.
- **Keys nobody can see, and a way out.** The app makes a named, restricted key in your account with your own login. Delete it in Immich, or press **Leave**, and everything stops safely without touching a photo.
- **Careful by design.** A photo is only removed after the other side holds a verified copy. Duplicates are recognised. If anything fails, your original stays put.
- **Beyond people with accounts.** A **drop box** is a link (or a QR code, or an email address) that anyone can send photos to, with no account, and it doubles as an **event**: a gallery for guests (hearts, downloads, a ZIP), a moderator link for a helper, a wall, an album of what guests loved and your own title, logo and colours; an **event pool** gives everybody everybody's photos for a trip or a party; a **frame** shows an album on a screen; a **weekly digest** picks the best of the week.
- **From your phone, too.** Besides tagging, the **Send** tab sends photos and videos from the phone in pieces that survive a lost connection, tagged for the service you pick; on Android it is in the Share menu of the gallery and finishes with the screen off. See the [phone guide](../guides/collect/phone.md#send-photos-from-a-phone).
- **Rules instead of tags.** A sender can let a **rule** look in Immich for photos of named people, taken between two dates, in a place, in an album or looking like a phrase, and **suggest** them on the Overview with pictures: *Send*, *Not these* or *Always*, and the rule can send by itself once you switch that on (it says plainly what that means). See [Rules](../guides/automate/rules.md).
- **Hand over from inside Immich.** A **Hand over...** button in Immich's own photo viewer, selection bar and three-dot menus lists the services you send to and hands the photos over, with **Undo**, no tags needed. See [Hand over from inside Immich](../admin/find-handoff.md#hand-over-from-inside-immich).
- **For photographers.** A **client room** shows an album to a client through pictures marked with your name, on a phone, behind a PIN if you like: the client chooses favourites (up to a limit, in rounds if you want), leaves notes, and you release the originals when you decide. See [Client rooms](../guides/collect/client-rooms.md).
- **Told your way.** Notifications to ntfy, Home Assistant or any webhook, to your mailbox, and, if people ask for it, to their own phone or email, with buttons to approve a request from the message itself.
- **Small.** One container, no database server, nothing stored unless you let people join from the page.

## Why not shared albums or share links?

Immich's own sharing is the right tool for *showing* photos. Handoff is for *moving, collecting and delivering* them.

| | Shared album / link | Partner sharing | immich-handoff |
|---|---|---|---|
| The photo ends up in the other person's library | No | No (read-only) | **Yes**, a real copy they own |
| You choose to keep or move your original | n/a | n/a | **Yes**, a move waits for a verified copy |
| People without Immich can send you photos | Upload links, but no review, no wall, no gallery for the guests | No | **Yes**, write-only drop box with moderation, wall and gallery |
| Between two servers | Public link only | No | **Yes**, signed, both ways |
| Photos picked automatically | No | Everything | **Rules** by person, place, date or phrase |
| Revoke it | Delete the link | Stop sharing | Delete the key in Immich; nothing else is touched |
