# Control what is handed over

Pause a service, cap what each sender hands over in a day, set quiet hours, hand a photo over on a chosen day, let copies expire, and take back the last hand-over. The page says in words why photos wait and when they will go.

![A service's settings: daily limits, quiet hours, what it accepts and the lifetime of copies](../../images/controls-limits.png)

## Pausing

A sender can press **Pause sending** on a service they joined from the page, and **Resume sending** later. An owner can press **Pause service** on a service they offer, and **Resume service**. Both are for being away, or for sorting something out, without leaving and losing the key and the workflow.

- While paused, **nothing is handed over and nothing is touched**: photos tagged meanwhile stay tagged, in the sender's account, and the daily check and the album check skip them too.
- On **Resume**, the waiting photos are handed over at once.
- Everyone involved can see it: a sender sees "You paused" or "The owner paused this service" on the card, and an owner sees "(paused)" next to a sender who paused. It also shows on the [administrator's page](../../admin/admin-page.md) and in the recent events.
- A pause survives a restart of the app. It does not change what is stored in Immich, and a sender can still leave while paused.

## Daily limits for senders

When anyone on the Immich server may send, one person could fill the receiver's storage. An owner can cap what **each sender** hands over to a service in a day: a number of **photos**, a number of **megabytes**, or both (**More options** when creating the service, or **Save limits** on the service's card later). Empty or 0 means no limit.

- "A day" is the **last 24 hours**, not the calendar day: a photo counts until 24 hours after it was handed over.
- Only photos that were actually handed over count (not duplicates and not refusals), by the size of the original. The size limit stops further photos once it is reached, so the last photo may take a sender slightly past it.
- When a sender is at the limit, the rest of their tagged photos **wait, still tagged**. The sender's card says "The daily limit is reached, so N photos wait... They are handed over from <time>", the owner sees each sender's use ("12 photos, 340 MB in the last 24 hours") and a line in the recent events, and `NOTIFY_URL` is told once. The photos go on their own as soon as the oldest hand-over is 24 hours old, or at once when the owner raises or removes the limit.
- It applies to photos from a linked album too. It applies to people who joined from the page; senders set up in `.env` or the routes file have no stored membership and are not limited.
- What was handed over is recorded from the start, so a limit set today already counts today's photos. Records older than eight days are forgotten (the Overview's weekly chart uses them too).

## Quiet hours, and what a service accepts

An owner can set two kinds of rules for a service (**Hand over only between** and the rules under it, in the service's settings), and the administrator can set quiet hours for the whole app (`QUIET_HOURS`, see [Settings](../../reference/settings.md)).

**Quiet hours.** "Hand over only between 01:00 and 06:00": outside those hours photos **wait, still tagged**, and go when the hours begin (a window that ends before it starts, such as 22:00 to 06:00, crosses midnight). With **Only files larger than (MB)** set, only big files wait and small ones go at once, which suits a slow line: photos now, long videos at night. The time zone is the app's (`TZ`). The sender's card says "The quiet hours are not open, so N photos wait... They are handed over from <time>", and the card of every sender shows the hours. A service's own hours replace the app-wide ones.

**What a service accepts.** An owner can say that a service takes only photos rated at least 1 to 5 stars, only photos (no videos), only landscape or only portrait pictures, and nothing larger than a number of megabytes. This turns a service into a feed for a photo frame or a relative's tablet. Whatever does not match is **not handed over**: its service tag comes off and the **refused tag** (`share-duplicate` unless set otherwise) goes on, so the sender finds it in Immich, and the activity says "N photos were not handed over to <service>: they do not match what the service accepts". The sender's card lists what the service accepts, in words. Some details:

- The **tagged photo** is the one judged: a stack or a live photo goes, or stays, as one.
- The rating and the size come from Immich. A rule the app cannot check because Immich does not say (no dimensions, no size) lets the photo through; an unrated photo is below any minimum rating.
- Changing the rules looks at waiting photos again at once. A photo that was refused stays refused: tag it again.
- A service in a routes file sets these in the file ([routes example](../../admin/fixed-accounts.md#fixed-accounts)); a plain `.env` install sets them with `ACCEPT_*`.

## Tagging a photo for a date

To hand a photo over **on a day** (a surprise, an anniversary, Christmas morning), tag it with the service's tag, a separator and the date: `share/2027-06-01` (in the Immich app type the slash and Immich makes a tag inside `share`), or `share 2027-06-01`, `share-2027-06-01` or `share_2027-06-01`. The photo waits, still tagged, and is handed over from the start of that day in the app's time zone (`TZ`). Details:

- Immich tells the app when you tag, as it does for any tag, so a date that has already come (today or in the past) hands the photo over at once. For a future date the app arranges a pass for that day, and the daily sweep is the safety net (also after a restart).
- A photo with both the plain tag and a future date waits for the date. A photo with only the plain tag goes at once, as always.
- To cancel, take the dated tag off. The receiver is not told that something is on its way.
- Photos tagged only for a date are **not counted** in the page's "Waiting to go" until their day.
- Checked against a real Immich 3.2.0 and 3.4.1: a tag inside `share` (such as `share/2999-01-01`) is also found by a search for `share`, which is why the app skips photos whose day has not come.

## Hand-overs that expire

An owner can make the copies that arrive **expire**: "Move copies to my trash after N days" in the service's settings. A copy that is still **unfiled** (in no album) and still has the inbox tag when its time is up goes to the owner's **trash**, never deleted for good, so it can be put back for as long as Immich keeps the trash. To keep a copy, file it in an album or take the inbox tag off; both leave it alone. It is meant for photos that should not stay, such as a school picture sent to a teacher, or for a service that is only ever a place to look at things.

- The time is counted from the day the copy arrived. A service with a lifetime does not take the inbox tag off at the usual inbox expiry first; the lifetime decides.
- Senders are told on their card that photos expire ("moved to the receiver's trash after N days unless they file them in an album"), and the owner's activity says when copies expired.
- It needs the administrator to allow it (`RECEIVER_DELETE`, on by default; see [Settings](../../reference/settings.md)). A service made before that existed gets the permission when its owner opens the page, which is also when they set a lifetime.
- Without the permission (a key made by hand, see [Fixed accounts](../../admin/fixed-accounts.md)) the setting does nothing, and the log says so once.

## Taking back the last hand-over

A sender who joined from the page sees **Take back the last hand-over** on a service's card for 24 hours after handing photos over. It undoes what the last pass handed over to that service, as far as it can be undone:

- The receiver's **copies** go to the receiver's trash (not deleted for good).
- Your **originals** come back from your trash, if you moved them, and lose the service tag so they are not handed over again. With **keep my originals** nothing was in the trash; the "handed over" tag comes off.
- A copy the receiver has **already filed in an album** is theirs now: it stays, and so does your original's state. The page says how many stayed.
- A stack and a live photo (with its motion clip) are taken back whole.
- It takes back the most recent hand-over not yet taken back, so pressing it again takes back the one before. Only people who joined from the page have it (accounts set up in `.env` or the routes file are not recorded). It needs `RECEIVER_DELETE` (on by default); if the owner's key cannot move photos to the trash yet, the page asks the sender to tell the owner to open the page once.

## If something is off

- **Photos wait and I do not know why.** The sender's card says: paused, the daily limit is reached, or the quiet hours are not open, and from when they go.
- **A photo was "not handed over".** It did not match what the service accepts; its service tag came off and the refused tag went on. Change the rules or tag it again.
- **I handed something over by mistake.** **Take back the last hand-over** works for 24 hours.
- **A dated photo went too early.** A date that has already come hands the photo over at once; check the tag.

## What next

- Make rules instead of tags: [Rules](rules.md)
- Share with your household: [Share photos with your household or friends](../share/household.md)
- The settings the administrator can set: [Settings](../../reference/settings.md)
