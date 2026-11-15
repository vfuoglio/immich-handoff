# Try it first

A throwaway copy of everything on your machine, in one command, with three people and sample photos already in place. Nothing leaves your computer, and one command removes it. It is for looking around, not for real use (see [Install](../docs/install.md) for that).

You need Docker and about 4 GB of free memory. The first start builds the app and downloads Immich, so allow several minutes.

```
cd demo
docker compose up -d --build
docker compose logs -f seed        # wait for "Done", then press Ctrl+C
```

Open **http://localhost:2295** and sign in to Immich as one of the people (password `Demo-pass-1`):

| Who | Sign in | What they have |
|---|---|---|
| Alice | `alice@demo.test` | 10 photos (a trip to Innsbruck), an album "Holiday", and two services she offers: **Family album** (open) and **Photo club** (she approves senders) |
| Bob | `bob@demo.test` | 10 photos (a party); he joined Family album and five of his photos were already handed over |
| Carol | `carol@demo.test` | 10 photos (travels), and no services yet: ask to join Photo club |

(The Immich administrator is `admin@demo.test` / `Demo-admin-1`.)

The sample photos in `photos/` are public-domain or CC0 pictures from Wikimedia Commons, with no recognisable people; [`photos/CREDITS.md`](photos/CREDITS.md) lists the author, license and source of each. Each person also has a near-duplicate, a too-dark photo and a blurry one, so the duplicate and quality checks have something to show.

The app is on the same address under **/handoff/**, and a small nginx puts a script into Immich's pages, so you see two things inside Immich itself: the Handoff icon in the header, and a **Hand over...** button in the photo viewer and when photos are selected.

## Things to try

- **Hand over from inside Immich.** Sign in as Bob, open a photo, press **Hand over...**, choose Family album. A message says it was handed over and offers **Undo**. Then open the Handoff page (the header icon) and watch it arrive on the Overview.
- **Be the receiver.** Sign in as Alice in a second browser profile (or a private window): the photos Bob sent are in her library, tagged `inbox`.
- **Ask to join.** As Carol, open Handoff, find **Photo club**, press **Ask to join**. As Alice, approve it from the Overview.
- **Collect from guests.** As Alice, open **Services**, **Make a drop box**, copy the link in a private window, and send a photo as a guest.
- **A client room.** As Alice, make a **client room** from the album "Holiday" and open its link on your phone.

## Stop it

```
docker compose down -v      # removes the demo and everything in it
```

The demo uses ports 2295 (Immich and Handoff together) only. To try the version in this checkout, nothing else is needed: the app is built from it.
