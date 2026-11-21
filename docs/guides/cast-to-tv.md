# Cast to a TV with a code

**Who it is for:** Someone who wants to show an album, a person's photos, the newest photos or a day's memories on a TV without signing in on the TV or typing a long link with a remote.

**What you get:** The TV opens a short address and shows a 6-character code. You type the code on your own page, choose what to show, and the TV starts a slideshow. From your phone you can pause it, skip to the next or the previous photo, show something else, or stop it.

## In a few steps

1. **Open the address on the TV.** In **Settings**, under **My library**, **Cast to a TV** shows the address to open in the TV's browser: your Handoff address followed by `/tv` (and `/tv/legacy` for an old TV, see below). The TV shows a code in large letters and the address of the page to open on the phone.
2. **Type the code on your phone.** Open **Show photos on a TV**, type the code, and choose **what to show**: everything new in the last 30 days, one of your albums, an album shared with you, the photos of a person, or the memories of a day. Choose how long each photo stays (8, 15, 30 or 60 seconds) and how one gives way to the next (fade, zoom, slide or rise). **Show it on the TV.**
3. **The TV starts.** Photos are shown once each in a random order before any comes round again, and the list is read again every two minutes (`FRAME_REFRESH`), so a photo you add shows up.
4. **Use your phone as the remote.** Under the TV on the same card: **Previous**, **Pause** or **Play**, **Next**, **Show something else** (the same choices again) and **Stop**. You can have up to 5 TVs at once.

## What to know

- **The code is short-lived and single-use.** It is good for 10 minutes and works once; a new one appears on the TV when it runs out. Five wrong codes from one person silence them for ten minutes, and a flood of wrong codes from anyone makes every claim wait. A single address can ask for up to 20 codes an hour.
- **The TV signs in to nothing.** Its only secret is a long id the app gave it. It gets the photos through the app, by ids that mean nothing outside it, and only the photos of what you chose. A TV that has not asked for half an hour, or has been showing for a day, ends; so does one you stop.
- **A key of your own.** The first time, the app makes a key named `handoff: cast` with your own sign-in. It can look at pictures and read your library to list a source (a memory also needs it to read your memories, which your sign-in adds the first time you cast one). Immich cannot limit a key to one album, so the key can see your whole library, as a [frame](frames-and-digest.md)'s key can; the app only hands the TV what you chose. The app deletes the key the next time you open your page after your last TV has ended.
- **Which TVs work.** The usual page needs a browser from about 2021 on, which is most TVs from about 2022 (recent Samsung, LG, Android TV and Google TV, Fire TV, and a browser on a Chromecast with Google TV). **Legacy compatibility mode** (`/tv/legacy`) is written for browsers from about 2016: plain JavaScript and simple styles, a fade only, no zoom. `/tv` tests the browser and loads the legacy page by itself if the usual one cannot run; use `/tv/legacy` directly if a TV shows the page wrong or nothing. An Apple TV has no browser: cast from an iPhone with AirPlay instead.
- **A shorter address.** The TV page shows the address it was opened on. If you want something shorter to type with a remote, point a short name at the app's `/tv` on your reverse proxy (for example `tv.example.com` to `/tv`); the code screen then tells people to open the page it was reached from.
- **Not instant.** The TV looks for your answer every three seconds and photos load one at a time, so a start can take a few seconds.

How it works: [the design note](../design/cast-design.md).
