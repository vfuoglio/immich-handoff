---
title: "Questions people ask first"
feature: faq
audience: user
goal: start
order: 370
tip: "Short answers to what people ask before they start."
---

# Questions people ask first

Short answers, and where to read more.

## The basics

**Is this the same as Immich's shared albums or share links?**
No. Those let people *look at* your photos. Handoff puts a photo **in another library**, collects photos from people who have no account, and connects two servers. See the [comparison](../start/why-handoff.md#why-not-shared-albums-or-share-links).

**Does the photo really move to the other person's library?**
Yes. A copy arrives in their Immich as their own photo, with its name, dates, favourite mark, description, rating and location. [What is carried over](what-is-carried-over.md) lists what travels and what does not.

**What does it need?**
Immich 3.2.0 or newer, Docker, and Tags switched on for everyone who takes part. One small container, no database server. See [Install](../admin/install.md).

**Do the people who send need to install anything?**
No. They use Immich as they do now: add a tag, or press the **Hand over...** button if your administrator [added it](../admin/find-handoff.md#hand-over-from-inside-immich). Guests without an account use a link or QR code in their browser.

**Does it work with the Immich phone app?**
Tagging in the app works. The button inside Immich works in a browser, not in the native app. The page also sends photos from a phone: see [Send photos from a phone](../guides/collect/phone.md#send-photos-from-a-phone).

## Your photos and your keys

**What happens to my original?**
You choose when you join a service: **keep** it (a copy is sent) or **move** it to your trash, which happens only once the other side has a verified copy. The receiver cannot decide that for you.

**Do I need to give anyone my password or a key?**
No. The app makes a named, restricted key in *your* account with your own sign-in, keeps it encrypted, and nobody sees it. Delete it in Immich, or press **Leave**, and everything stops without touching a photo. See [Security](security.md).

**Is it official?**
No. It is an independent community project, not made or endorsed by the Immich team.

## What else it can do

**Hand photos to people with no Immich account, for a wedding or a party?**
Yes. A [drop box](../guides/collect/events.md) is a write-only link or QR code: approve photos first, show them on a wall, give guests a gallery with hearts, make a best-of.

**Deliver a shoot to a client?**
Yes. A [client room](../guides/collect/client-rooms.md) shows an album through marked pictures, the client chooses favourites, and you release the originals when you decide.

**Share with someone on another Immich server?**
Yes, with [albums shared across servers](../guides/share/shared-albums.md). Your friend does not even need Handoff if you give a key with a short list of permissions.

**Share a person's photos with my partner, both ways?**
Yes. In **Settings**, **Offer to share photos** makes a private sharing between two people on the server: each of you chooses the people whose photos go to the other, the other says yes on a page of their own, and those photos are offered with pictures (or sent by themselves, if you switch that on). It is not instant, each library keeps its own copy, and it cannot recognise pets yet. See [Share a person's photos](../guides/share/share-people.md).

**Keep a photo someone shared with me?**
Yes. The button inside Immich shows **Add to my library** on photos from albums shared with you and from a partner. See [Add to my library](../admin/find-handoff.md#add-to-my-library-from-inside-immich), or [follow the album](../guides/share/follow-album.md#following-an-album-shared-with-you) to have new photos offered as they are added.

**Send photos to a NAS, Nextcloud, S3 or SFTP, instead of or as well as another Immich account?**
Yes. The administrator names the places in a file and the owner of a service adds one. The photo is written, checked, and only then is the original moved or marked; if a place is down, the original stays tagged until it holds the photo. See [Destinations](../guides/share/destinations.md).

**Stack my RAW and JPEG files, edits and bursts?**
Yes, with your say-so: **Stack photos that belong together** offers each stack, never changes or deletes a photo, and can take its own stacks apart for 15 minutes. See [Stacking](../guides/automate/stacking.md).

**Make albums from the folders of my external library?**
Yes: **Albums from my folders** offers an album for each folder and puts the photos in once you say yes. It only adds. See [Albums from folders](../guides/automate/folder-albums.md).

**Give photos from a camera without GPS the place they were taken?**
Yes, with care: **Places for photos that have none** offers the place of a photo taken minutes before or after by the same camera, and writes nothing until you say yes. The app cannot take a written place back (Immich does not allow it). See [Places](../guides/automate/places.md).

**Make a shareable recap of a trip or a year?**
Yes: [Stories](../guides/show/stories.md) turn an album, a person, a stretch of time or a drop box into a short animated recap with a link anyone can open, with no account.

**Show photos on my TV with a code?**
Yes: open `/tv` in the TV's browser and type its code into **Cast to a TV**. See [Cast to a TV](../guides/show/cast-to-tv.md).

**See my albums as folders, like Trips / 2024 / Rome?**
Yes, on a page of its own: name albums with slashes and open **Album tree**. See [Album tree](../guides/show/album-tree.md).

**Get a message on the days Immich has memories for me?**
Yes: [On this day](../guides/show/memories.md) sends one message a day, by notification or email, when there are memories you have not watched.

## The future

**What if Immich changes?**
The project's tests run against a real Immich, and the button inside Immich is checked against its pages on the pinned version, so a change shows up when that version is raised. If the button cannot find its place it hides itself and nothing else breaks.

**What if Immich adds this itself?**
It may add some of it. Handoff only uses Immich's public API and changes nothing inside it, so if Immich covers a part of what you use it for, you can stop using that part and nothing else is affected. What would still be useful on its own: choosing whether the original stays or moves (and nothing removed before a verified copy exists), guests without an account sending through a QR code with moderation, a wall and a gallery, client rooms for photographers, and sharing between two servers. Leaving is easy: delete the key in Immich, or press **Leave**, and every photo stays where it is.

## What next

- Hand over a first photo: [Quick start](../start/quickstart.md)
- Pick a goal: [Using it](../using.md)
- Something not working: [Troubleshooting](troubleshooting.md)
