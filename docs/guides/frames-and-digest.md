# Frames and the weekly digest

**Who it is for:** Anyone with an album worth showing.

**What you get:** A frame shows an album on any screen with no account; a digest picks the best of the week for a service.

**What you see:** A screen that follows the album, and a digest album every Monday.

## Frames: an album on a screen

A **frame** shows one of your albums full screen on a photo frame, an old tablet or phone, or a TV, with no account on it. Under **Frames** on the Services tab, **Make a frame**: give it a name, choose one of your own albums, how long each photo stays (8, 15, 30 or 60 seconds) and whether the link works until you end it or for 30 days, 90 days or a year. You get a link (and a QR code) to open on the screen; double-click the page for full screen (it is a web page, so on a TV open it in the TV's browser, or cast a tab).

The page shows each photo once in a random order before any comes round again, never the same one twice in a row, and starts with what was added most recently. A frame has the same buttons for the transition (**Fade**, **Zoom**, **Slide**, **Rise**) and the backdrop (**Glow**, **Black**) as the wall, which appear when you touch or move the pointer and fade again, and the same address options (`?fx=zoom&bg=black`); a new photo is given a moment, as on the wall. It looks at the album again every couple of minutes (`FRAME_REFRESH`): **photos you add appear** and **photos you take out of the album disappear** within about that time, and nothing but the album's photos (not videos, not archived or trashed ones) is ever shown. If the network drops it keeps showing what it has and asks again, less often the longer it fails. **End frame** stops the link at once and deletes its key. A frame whose end date has come stops by itself and stays in your list, marked *Ended*, until you remove it (that deletes its key; only you can, from your account).

## Weekly digest

On a service you own, **Weekly digest** turns on a page of the week's best photos. Every Monday at about 8 in the morning (the app's time zone, `TZ`) the app looks at what arrived in the service during the last seven days, leaves out what is too dark, too bright, blurred or the same moment as a better shot (the same rules as a drop box's best-of), keeps the best forty, puts them in a new album in your Immich account called "<service> week of <date>", and makes a **shared link** to it that ends after a month, so you can send it to people with no account. The card shows the link, how many photos came from how many people, and **what was left out and why**. You are told when it is ready (in your Activity, and by mail or notification if you asked for those); the service's members are not. A week in which nothing arrived makes nothing and says so.

**Make this week's digest now** makes one at once from the last seven days. Turning the digest on takes effect from next Monday. Turning it **off** ends its link and takes back the permissions it needed; the albums it made stay, they are yours. Nothing is ever deleted or changed in your library. To do this without you signed in, turning it on gives the service's own key a few more permissions (looking at pictures, making an album and a shared link), which is why it asks you and tells you; see [Security](../security.md#weekly-digest).
