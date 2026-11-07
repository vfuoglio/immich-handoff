# The administrator's page

Anyone who administers the Immich server can open **Admin** (a link in the header, only they see it) to look at everything the app is doing, and to remove a service or a member that is stuck. Everyone else, signed in or not, gets "not found" for that address, so the page does not show itself.

## Who is an administrator

The app keeps no list of its own. When someone opens the page it asks Immich, with that person's own sign-in, whether they are an administrator, which is the same setting as **Administrator** on their account in Immich. Make someone an administrator or take it away in Immich, and the next page they open follows.

## What it shows

At the top, three numbers: how many **services** are offered, how many **senders** there are across them, and how many **need attention** (a service or sender that is paused, limited or disconnected, and people waiting to clean up).

- **Every service, as a row** that opens in place (the same way as on your own page): its tag, who offers it, who may send, whether originals are kept or moved, the album arrivals go to, and whether it works. Services from the configuration (`.env` or the routes file) are marked **From configuration**.
- **Pauses and limits**: a **Paused** chip on a service its owner paused, "(paused)" next to a sender who paused, and the daily limit per sender when there is one.
- **Its members**: name, email, whether they keep or move their originals, the album they linked, and whether their connection works or why it does not.
- **Waiting to clean up**: people whose service was removed and who still hold a key in Immich.
- **Backups**: whether automatic backups are on (how often, how many are kept, which folder), the newest backup and when it was made, or why the last one failed. Only installs that keep data have this section.
- **Recent events** across all services.
- **Check now**: runs the same setup check as `immich-handoff doctor` (see [Troubleshooting](troubleshooting.md#check-your-setup-first)) and shows the result on the page: every key and its permissions, Tags, each sender's workflow and how Immich's recent runs of it ended. It only reads.

It never shows an API key, a photo or a thumbnail. It does show names and emails, which an Immich administrator can already see.

## Removing a service or a member

**Remove** next to a member, or **Remove this service** on a service someone created on the page. Services and senders from the configuration cannot be removed here (change `.env` or the routes file instead). Both ask for confirmation first.

What removal does:

- the app **forgets the API key** and stops using it, so nothing more is handed over for that person or service;
- the person sees on their own page why, with a **Clean up** button that deletes the key and workflow the app made in their account (for a removed service, its owner and each member are told);
- an event is added to the recent events of the people involved, and the address in `NOTIFY_URL`, if set, is told;
- **no photo is touched**, and a removed member can join again if the service lets them.

### Why removal cannot delete the keys

An API key lives in the account of the person it belongs to. Immich lets **only that person** list or delete their keys: it has no call for an administrator to do it for them, and the app holds no sign-in of anyone else, only the restricted key each person gave it, which has no permission to delete itself. So whoever removes a person from the app can stop the app from using their key, but cannot revoke it.

The key stays valid in Immich until its owner deletes it. It is a restricted key (it can read and tag photos, and for a sender it can download and optionally delete the ones they handed over; it cannot change the account), and the app no longer has it, so it is harmless unless someone else holds a copy. The person is told its name (`handoff: <service>`) and where to delete it (Account Settings, API Keys), or they press **Clean up**, which does it for them. An administrator who wants the key gone for certain can ask the person, or disable or delete their Immich account.
