# Using it

This page is for the people who send and receive photos. Everything happens on the handoff web page and in the normal Immich app.

## Words used here

| Word | Meaning |
|---|---|
| **Service** | A named place photos can be handed to, with one tag. "Holiday 2026" with the tag `share` is a service. |
| **Receiver** (or **owner**) | The person whose library the photos land in. They create the service and decide who may send. |
| **Sender** | Someone who tags photos to hand them to a service. |
| **Tag** | The label you add to a photo in Immich to send it. Each service has its own. |
| **Inbox** | A tag on everything that just arrived, so the receiver can find what is new. |

> **Before you start: Tags must be on in Immich.** It is off by default and each person has it for their own account. If it is off, this page shows a notice with a **Turn on Tags** button; you can also do it in Immich under Account Settings, then Features, **Tags**. Senders need it to tag photos; receivers need it to see the inbox tag and filter by it. Switching it on is not enough: **refresh the Immich tab or window afterwards**, or the tag options do not show up (on the phone app, close and reopen it).

## The Overview

The page opens with what matters, in this order:

1. **Needs you.** Everything that waits on a person, most urgent first, each with its button:
   - **Connection lost** and **Removed by its owner**: **Clean up**, which deletes what the app made in your Immich account (then you can join again).
   - **Disconnected** (a service you offer): **Reconnect**.
   - **Request**: someone wants to send to a service you offer; **Approve** or **Decline**.
   - **Paused**: a service you paused, or your own sending (**Resume**), or a service its owner paused (nothing for you to press).
   - **Limit**: a sender is at the daily limit, or you are (see [Daily limits](#daily-limits-for-senders)).
   When nothing waits, it says so. A request is answered here and nowhere else.
2. **This week.** How many photos were handed over in the last seven days, as a small chart with one point per day (UTC days, today on the right). It counts what the app recorded for people who joined from the page: the photos **you sent**, and the ones that **arrived in services you offer**. Senders set up in `.env` or a routes file are not recorded, so they do not appear in it. Next to it, **Waiting to go** (photos you tagged that are still to be handed over, if you send) and **In your inbox** (what arrived and is not yet filed, if you receive). They refresh by themselves whenever a hand-over finishes.
3. **Get started**, until you are done: turn on Tags, join or offer a service, hand over your first photo. A ring shows how far you are. It goes away by itself after the first hand-over, or when you press **Hide this guide**, and does not come back.

Below that are your services, in sections by what you do with them: **You send to**, **You receive**, **You offer** (services you created) and **You can join**. Each service is one line (its initial, name, what it is, any state such as Paused or Limit, and the number that matters: photos waiting, or in your inbox). Press it to open it in place and see its settings: the tag, the album, who sends and how much they have used, the limits, the invitation link and the pause button. With only one service it starts open.

What cannot be undone sits apart at the bottom of an open service, in a dashed box that says what it does: **Leave** (for a service you send to) and **Delete service** (for one you offer). Every question, such as joining, pausing, leaving or deleting, is asked in a dialog in the page itself, with a title, one clear sentence and a button named for what it does (red when it cannot be undone); Escape or Cancel changes nothing.

### The live scene

Near the bottom of the Overview, **Live** shows what is happening as it happens. Each service you take part in has a stage: one **lane** for each sender (you send to a service: your own lane; you offer or receive from it: the senders), and a gate into the receiving library on the right. When a photo is handed over, a small coloured square travels along the sender's lane into the gate and the **arrived while you watch** counter goes up. The squares are only squares: the scene never shows a photo or a file name, and it shows only real hand-overs, never made-up ones.

A lane also says in words what state it is in, so the colour is never the only sign: **Flowing**, **Paused by the owner** or **Paused by the sender** (dashed lane), **Daily limit reached** with how many wait and until when (amber), **Connection lost** (red, with a cross). A short log under the stages lists the last few things in a sentence each, and a screen reader is told the same sentence.

It stays calm: nothing moves while nothing happens, only a few squares fly at once (a big upload counts the rest without more squares), and if your browser asks for reduced motion, or the tab is hidden, the squares are skipped and only the labels, the counter and the log change. If the connection to the app drops, the scene dims and says so, then catches up by itself when it is back.

## Sending photos

Open the page. It shows your services and what is waiting:

![The page for a sender, with photos waiting to be handed over](images/dashboard-sender.png)

What reaches the receiver is the photo's file, its name, its dates and the favourite mark. Things Immich keeps only in its own database, such as a description you typed in Immich, albums and edits, do not travel: see [What is carried over](what-is-carried-over.md). To send a photo, add the service's tag to it in the Immich app (select photos, **Add tags**). Within a few seconds the photo is copied into the receiver's library, and the page's **Waiting** count goes back to zero. The page also works on a phone, and follows its light or dark setting:

<p align="center"><img src="images/dashboard-mobile-dark.png" alt="The page on a phone in dark mode" width="280"></p>

### Joining a service

Under **Services you can send to** you see the services you may join. Joining asks one question that is yours alone to answer, **what should happen to your originals?**

- **Join and keep my originals** (the usual choice). A copy goes to the receiver and your photo stays where it is. The app's key for you has no permission to delete anything.
- **Join and move my originals to my trash.** Once the receiver has a verified copy, your photo goes to your trash, where Immich keeps it for 30 days, so a mistake can be undone.

You can choose differently for different services. To change your mind, leave the service and join again.

When you join, the app creates an API key named `handoff: <service>` in your own Immich account, using your login, and keeps it encrypted. Nobody sees it, not even you. You can end it any time (see below).

Some services need the owner's approval. You press **Ask to join**, the owner approves, and then you press **Activate**. (The app can only create a key in your account while you are signed in, which is why the last step is yours.)

### Handing over a whole album

Instead of tagging photos one by one, you can link one of your own albums to a service you joined from this page. On the service's card press **Hand over a whole album**, choose the album, and confirm. Everything in it is handed over to that service, **now and whatever you add to it later**, with no tag.

- **Why not just tag the album?** Immich tags belong to single photos, not to albums, so there is no such thing as tagging an album. Linking is different: the app keeps looking at the album.
- **It is checked on a timer.** Immich sends no event when a photo is added to an album (see [why the inbox tag takes minutes to go](#when-the-inbox-tag-goes-away)), so the app looks at linked albums every 15 minutes (`ALBUM_CHECK_MINUTES`). Tagging a photo is still instant.
- **Each photo goes once.** With **keep my originals**, a handed-over photo stays in your album and is marked with the `handed-over` tag, which is how the app knows to leave it alone. With **move**, the original goes to your trash. A photo the receiver refused (they have it in their trash) is tagged `share-duplicate` and left alone until you remove that tag.
- **Only your own photos.** An album can hold other people's photos (a shared album); those are never handed over.
- **One album per service.** Linking another replaces the first. **Stop handing over this album** on the card unlinks it. Photos already handed over stay where they are.
- **Move mode asks first,** because it sends every photo in the album to your trash once the other side has a copy.
- **The key gets one more permission.** Linking adds `album.read` to your existing key, using your own login, so you do not have to join again. It can read albums and nothing more. See [Security](security.md#what-a-key-can-do).
- **Progress.** The "waiting to be handed over" number on the card counts down photo by photo while a long hand-over runs. When photos from an album have been handed over, a line appears under **Recent events**, and a big one (20 photos or more) also sends a notification to `NOTIFY_URL` if the person running the app set one.
- **If the album cannot be read** (you deleted it, or the key lost the permission), photos you tag are still handed over, and the problem is logged and notified once. Link another album to fix it.
- Services defined in a routes file or `.env` cannot have a linked album; this is for services you joined from the page.

## Receiving photos

### Offering a service

Under **Offer a service of your own**, choose:

- a **name** and the **tag** senders will use;
- **who can find it**: everyone on this Immich server, or only people you send a link to;
- **who may send**: people you approve, or anyone;
- optionally (**More options**) the tag put on what arrives, how many days an unfiled photo keeps it, and an **album** to put arrivals in.

![The form for offering a service](images/offer-form.png)

Each service gets its own inbox tag by default, for example `inbox/Holiday 2026`, which Immich shows nested under `inbox`. Filter by it in Immich to see what arrived through that service. The inbox tag removes itself once a photo is in an album, or after 30 days; photos are never deleted by that. [When exactly](#when-the-inbox-tag-goes-away) is explained below.

### Putting arrivals in an album

Give a service an album (for example "Holiday 2026") and every photo handed over to it is put in that album in your Immich account, so there is nothing to file by hand.

- **Instead of the inbox tag.** A photo in the album does not also get the inbox tag. The album is where you look; the page counts its photos. (The inbox tag would be removed within minutes anyway, because the photo is in an album.)
- **The album is yours.** The app uses an album you own with exactly that name, and makes one when the first photo arrives if you do not have it. It never uses an album that was only shared with you. If two of your albums have that name, it uses the first Immich lists.
- **If you delete or rename it,** the next photo makes a new album under the configured name. Photos already in the old one stay with it.
- **Photos you already had** (the same photo handed over again) are left where you put them; only new arrivals are filed.
- **It cannot get in the way of a hand-over.** If the album cannot be used, for example because the key is missing the album permissions, the photo still arrives, tagged inbox as without an album, and the log and the notification say why.
- **The key asks for more.** Creating a service with an album gives its key permission to find, create and add to albums, and nothing more. A service without an album does not. See [Security](security.md#what-a-key-can-do). The album is chosen when the service is created.

### Approving senders and sharing a link

Your services appear on the page with who is waiting and who is connected:

![A service you offer, with a pending request](images/owner-service.png)

- **Approve** or **Decline** each request. Anyone you approve then activates their own side.
- For an invitation-only service the card shows an **invitation link**. Whoever opens it, signed in to Immich, lands on a page for exactly that service. **New link** makes a fresh link and the old one stops working at once; people already approved or joined are not affected. The link is only as private as the way you share it.
- **List for everyone** and **Make invitation-only** switch between the two. **Delete service** ends it.

You see each sender's name and, on a request, their email address. Senders see only your name.

![The page for an owner](images/dashboard-owner.png)

## Pausing

A sender can press **Pause sending** on a service they joined from the page, and **Resume sending** later. An owner can press **Pause service** on a service they offer, and **Resume service**. Both are for being away, or for sorting something out, without leaving and losing the key and the workflow.

- While paused, **nothing is handed over and nothing is touched**: photos tagged meanwhile stay tagged, in the sender's account, and the daily check and the album check skip them too.
- On **Resume**, the waiting photos are handed over at once.
- Everyone involved can see it: a sender sees "You paused" or "The owner paused this service" on the card, and an owner sees "(paused)" next to a sender who paused. It also shows on the [administrator's page](admin.md) and in the recent events.
- A pause survives a restart of the app. It does not change what is stored in Immich, and a sender can still leave while paused.

## Daily limits for senders

When anyone on the Immich server may send, one person could fill the receiver's storage. An owner can cap what **each sender** hands over to a service in a day: a number of **photos**, a number of **megabytes**, or both (**More options** when creating the service, or **Save limits** on the service's card later). Empty or 0 means no limit.

- "A day" is the **last 24 hours**, not the calendar day: a photo counts until 24 hours after it was handed over.
- Only photos that were actually handed over count (not duplicates and not refusals), by the size of the original. The size limit stops further photos once it is reached, so the last photo may take a sender slightly past it.
- When a sender is at the limit, the rest of their tagged photos **wait, still tagged**. The sender's card says "The daily limit is reached, so N photos wait... They are handed over from <time>", the owner sees each sender's use ("12 photos, 340 MB in the last 24 hours") and a line in the recent events, and `NOTIFY_URL` is told once. The photos go on their own as soon as the oldest hand-over is 24 hours old, or at once when the owner raises or removes the limit.
- It applies to photos from a linked album too. It applies to people who joined from the page; senders set up in `.env` or the routes file have no stored membership and are not limited.
- What was handed over is recorded from the start, so a limit set today already counts today's photos. Records older than eight days are forgotten (the Overview's weekly chart uses them too).

## Leaving, and when a connection breaks

- A sender presses **Leave** on a service to stop. The app deletes the key it made in their account and the workflow it set up.
- An owner presses **Delete service**. Its senders then see **Removed by its owner** and press **Clean up** to remove their own key.
- Either side can also delete the key named `handoff: ...` in Immich (Account Settings -> API Keys). The app notices, pauses that connection, tells both sides, and **does not touch any photo**.

![A connection that was cut](images/connection-lost.png)

A sender whose key was deleted presses **Clean up** and can join again; photos they tagged meanwhile are handed over once they have. An owner whose own key was deleted presses **Reconnect**, and senders resume by themselves.

If an Immich administrator removes you from a service, or removes the service, you see why on this page with a **Clean up** button. Your API key in Immich stays valid until you delete it, which **Clean up** does for you ([why](admin.md#why-removal-cannot-delete-the-keys)).

The page also lists recent events (joined, asked, approved, connection lost, reconnected) for the services you take part in.

## Good to know

- **Nothing is removed without a copy.** A photo only goes to the trash after every receiver holds a verified copy, and never while a connection is broken.
- **A photo the receiver already has in their trash is not handed over.** The original is kept and tagged `share-duplicate` (the service's tag is replaced), so you can see why. Once the receiver empties their trash, add the tag again.
- **Phone backups.** A photo in your trash is skipped by your phone's backup. Once Immich empties the trash, a phone that still has the photo may back it up again.
- **A tag you just added is gone.** Immich occasionally drops a tag added in the first moments after a photo was uploaded. Add it again.

Something not working? See [Troubleshooting](troubleshooting.md).

## When the inbox tag goes away

- **Filed in an album:** the tag is removed within about 15 minutes, not instantly.
- **Not in any album:** the tag is removed once the photo has been in the receiver's library for the service's expiry (30 days by default). The clock starts when the photo was uploaded to Immich by the hand-over, not when it was taken, and editing or re-tagging the photo does not restart it.

**Why a delay, and not an instant removal?** The app learns about new photos because Immich tells it: a workflow fires when a tag is added and calls the app. Immich 3.2.0 offers no such event for albums. The only workflow triggers are an asset being created, its metadata being extracted and a tag being added, so the app cannot be told "this photo was just put in an album". The only way to notice is to look. So the app checks on a timer, and the check is deliberately small: one search for photos carrying the inbox tag per receiver, and nothing more when that is empty.

**If you want it faster or slower,** change `CLEANUP_INTERVAL_MINUTES` ([Settings](settings.md)). Shorter means more (small) requests to Immich and a shorter wait; `0` turns the timer off and leaves it to the daily check. If Immich adds an album event in a future version, this can become instant.
