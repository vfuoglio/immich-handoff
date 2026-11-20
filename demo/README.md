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

## The newer features

Everything below is already prepared: Mailpit (**http://localhost:8025**) catches every mail the app sends, and three places to hand photos to are running next to it. The pages are at `/handoff/`.

- **Add to my library** (Bob). Alice shared her library with him, so in Immich's viewer he sees photos that are not his: press **Hand over...** and pick *Add to my library*, or use the default destination on his Handoff page.
- **Follow a shared album** (Bob). On his page, under *Add to my library*, follow the album **Holiday** that Alice shared with him: its photos wait as suggestions with pictures.
- **On this day** (Alice or Bob). Both have photos and Immich memories dated today in earlier years. On the Overview, **On this day**, tick **An email**, press **Turn on**, and the mail arrives in Mailpit within a few seconds.
- **Albums from folders** (Bob). He has an external library. Under *Albums from my folders* choose **Make albums from a folder** and type `/external/photos`; the form shows the albums it would make (`paris`, `rome`, `Summer trip`).
- **Stacking** (Bob). Under *Stack photos that belong together*, folder `/external/stacks`: a RAW with its JPEG and an edit with its original are pairs; the three `burst` frames are offered and always ask first.
- **Share a person's photos both ways** (Alice and Bob). Alice has a person called *Mia* and Bob one called *Leo* (a face is put on four photos each, since the demo has no face recognition). As Alice, **Offer to share photos** to `bob@demo.test`, take the link in a second browser profile as Bob, accept and tick *Leo*, then look at the suggestions each of them gets.
- **Destinations.** As Alice, open **Services**, a service's card, **Destinations**: **nas** (a folder), **cloud** (WebDAV) and **bucket** (S3) are offered. Choose one for *Family album*, hand a photo over as Bob, and the file appears at the destination with its record beside it.
- **SFTP, entered by a person.** On the same card choose **Add my own**: SFTP, host `sftp`, port `22`, user `demo`, password `demo-pass`, folder `/photos`; press **Look at the SFTP server's key** and compare it before saving (it is not pinned in advance on purpose).
- **Following a destination** (Alice). Under *Add to my library*, **Follow a destination**, choose **nas**: it already holds four photos in `incoming`, offered with pictures. Photos Bob hands to *cloud* or *bucket* can be followed the same way by whoever entered that destination.

What cannot be tried here: the **pet** rule (it needs the optional pet helper image and real animals; see the automate guide), and anything that needs a real face recognition, a phone or a real cloud account.

## Stop it

```
docker compose down -v      # removes the demo and everything in it
```

The demo uses ports 2295 (Immich and Handoff together) and 8025 (Mailpit). To try the version in this checkout, nothing else is needed: the app is built from it.
