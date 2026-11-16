# The product film: storyboard

About 75 seconds, 1920 by 1080, 30 frames a second, music and burned-in captions, no voice. Every picture is the real product running on the demo (`demo/`) with its public-domain photos. The music is **Mister Blues** (CC0, OpenGameArt, https://opengameart.org/content/mister-blues), about 125 beats a minute, so one bar is 1.92 seconds and the film is 39 bars, 74.9 seconds. The track holds a steady groove with no big build, so the energy comes from the cuts and the pushes in; it is faded out over the last bar.

**The idea in one line:** *Photos arrive in the right Immich library. By tag, button, rule or QR code.*

**The look:** the app's own colours (indigo, magenta, orange of its icon) and fonts; the product is shown full screen inside a soft window frame, with big captions at the bottom left (white on a dark glass band, 56 px, never more than seven words) and a slow push-in on whatever matters. Photos get a gentle lift and a glow when they arrive. A cursor with a ripple shows every click.

## The scenes

| # | Seconds (bars) | What you see | Caption | Music |
|---|---|---|---|---|
| 1 | 0 to 3.8 (2) | Dark gradient. A grid of twelve demo photos drops into place, one by one, until they fill the screen like a library. | Everyone's photos... | Groove starts, quietly |
| 2 | 3.8 to 7.7 (2) | The grid dims; the icon and **immich-handoff** rise; the tagline types in. | ...in the right library. | The groove opens up on "right" |
| 3 | 7.7 to 15.4 (4) | **The host.** The Handoff page, Services tab: *Make a drop box*, a name ("Mia's birthday"), Enter, the box appears and its QR code opens and stays. Push-in on the QR code. | Make a link. Share a QR code. | Beat, bright |
| 4 | 15.4 to 23 (4) | **The guest.** A phone frame: the guest page, three photos chosen, *Send*, "Thank you!". Cursor becomes a finger. | Guests need only a browser. | Same groove |
| 5 | 23 to 30.7 (4) | **They arrive.** The Overview's live scene: the box has a lane of its own; each guest's drop flies from the left, named (or *A guest*), into the box, the total goes up and a +N fades in over it. | They arrive in your library. | Same groove, cuts on the bar |
| 6 | 30.7 to 38.4 (4) | **Show and keep.** The wall on a screen: a new photo arrives with a ring; then the host's *best-of* album, hearts on the favourites. | Show them. Keep the best. | Same, a longer shot |
| 7 | 38.4 to 44.2 (3) | **Inside Immich.** The photo viewer: **Hand over...**, the list, *Family album*, the message with **Undo**. | Hand over from inside Immich. | Cuts every three bars |
| 8 | 44.2 to 49.9 (3) | **A rule.** The Overview card: three photos found, *Send*, *Not these*, *Always*; Send is pressed. | Rules suggest. You decide. | Same |
| 9 | 49.9 to 55.7 (3) | **A client.** A client room on a phone: the marked pictures, hearts, "3 of 6 chosen". | Deliver a shoot to a client. | Same |
| 10 | 55.7 to 61.4 (3) | **Two servers.** The shared-album row, "Agreed", photos moving both ways between two Immich logos. | Share albums across servers. | Cuts every three bars |
| 11 | 61.4 to 67.2 (3) | **Trust.** Three lines appear over a calm photo wall: *Keys you can delete any time. A verified copy before anything moves. Tested against a real Immich.* | You stay in control. | Filter down a little under the captions |
| 12 | 67.2 to 74.9 (4) | **End card.** Icon, **immich-handoff**, `github.com/vfuoglio/immich-handoff`, *Try the demo in one command*; small credits line. | (none) | Fade out over the last bar |

## What each scene needs (capture and graphics)

- **Capture on the demo (real product):** 3 (Services, drop box form, QR), 4 (guest page on a 390 px phone), 5 (Overview live scene; Immich timeline), 6 (the wall; the best-of album in Immich), 7 (Immich viewer and the button), 8 (the rule's card), 9 (the client room on a phone), 10 (the shared-album row: a still from the screenshot tool, animated).
- **Motion graphics (HTML, rendered frame by frame):** 1 and 2 (photo grid, title), the captions, the cursor and ripples, the photo glow and the flight into the library in 5, the trust lines in 11, the end card in 12, the zoom and push-in on every captured scene.
- **Shared by all scenes:** the window frame, the caption band, the soft vignette and the music fade.

## Constraints I will keep to

- Only demo data on screen: no real names, addresses or accounts; the three demo people only.
- Captions stay inside the safe area (80 px from the edges) and are on screen for at least two seconds.
- Photo credits (Wikimedia Commons, CC0 and public domain) and the music credit are on the end card, small.
- The file stays under 10 MB so GitHub can play it on the repository page.

## What I need from you

1. Your changes to this storyboard (scenes to cut, reorder or add; any caption you dislike).
2. Nothing about the music: it is chosen (Mister Blues); tell me if you want a different cut of it, for example starting later in the track.
