# Questions people ask first

**Is this the same as Immich's shared albums or share links?**
No. Those let people *look at* your photos. Handoff puts a photo **in another library**, collects photos from people who have no account, and connects two servers. See the [comparison](../README.md#why-not-shared-albums-or-share-links).

**Does the photo really move to the other person's library?**
Yes: a copy arrives in their Immich as their own photo, with its name, dates, favourite mark, description, rating and location. [What is carried over](what-is-carried-over.md) lists what travels and what does not.

**What happens to my original?**
You choose when you join a service: **keep** it (a copy is sent) or **move** it to your trash, which happens only once the other side has a verified copy. The receiver cannot decide that for you.

**Do I need to give anyone my password or a key?**
No. The app makes a named, restricted key in *your* account with your own sign-in, keeps it encrypted, and nobody sees it. Delete it in Immich, or press **Leave**, and everything stops without touching a photo. See [Security](security.md).

**Do the people who send need to install anything?**
No. Senders use Immich as they do now: add a tag, or press the **Hand over...** button if your administrator [added it](find-handoff.md#hand-over-from-inside-immich). Guests without an account use a link or QR code in their browser.

**Can I keep a photo someone shared with me?**
Yes: the button inside Immich shows **Add to my library** on photos from albums shared with you and from a partner, and copies them into your own library with their description, rating and location (the person who shared them is not told, and a copy takes as much room as the photo). See [Add to my library](find-handoff.md#add-to-my-library-from-inside-immich). To have the new photos of an album shared with you offered to you as they are added, [follow the album](guides/automate.md#following-an-album-shared-with-you).

**Does it work with the Immich phone app?**
Tagging in the app works. The button inside Immich works in a browser, not in the native app. The page also sends photos from a phone: see [Send photos from a phone](guides/phone.md#send-photos-from-a-phone).

**Can I use it for a wedding or a party?**
Yes. A [drop box](guides/events.md#drop-boxes-photos-from-people-who-do-not-have-immich) is a write-only link or QR code; you can approve photos first, show them on a wall, give guests a gallery with hearts, and make a best-of.

**Can a photographer use it for clients?**
Yes: a [client room](guides/client-rooms.md#client-rooms-a-shoot-for-a-client-to-look-at-and-choose-from) shows an album through marked pictures, the client chooses favourites, and you release the originals when you decide.

**Can I share with someone on another Immich server?**
Yes, with [albums shared across servers](guides/shared-albums.md#albums-shared-with-other-servers), and your friend does not even need Handoff if you give a key with a short list of permissions.

**What does it need?**
Immich 3.2.0 or newer, Docker, and Tags switched on for everyone who takes part. One small container, no database server. See [Install](install.md).

**Is it official?**
No. It is an independent community project, not made or endorsed by the Immich team.

**What if Immich changes?**
The project's tests run against a real Immich, and the button inside Immich is checked against its pages on the pinned version; a change shows up when that version is raised. If the button cannot find its place it hides itself and nothing else breaks.

**What if Immich adds this itself?**
It may add some of it. Immich's team develops sharing in the open, and people have long asked for a way to copy a shared photo into their own library. immich-handoff only uses Immich's public API and changes nothing inside it, so if Immich covers a part of what you use it for, you can stop using that part and nothing else is affected.

What would still be useful on its own: the sender choosing whether the original stays or moves (and nothing is removed before a verified copy exists), guests without an account sending through a QR code with moderation, a wall and a gallery, client rooms for photographers, and sharing between two servers. Leaving is easy: delete the key in Immich, or press **Leave**, and every photo stays where it is.
