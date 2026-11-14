# Images

Screenshots for the documentation are made by `tools/screenshots` (see its README). The project icon files are here too:

| File | What it is |
|---|---|
| `icon.svg` | The icon, full drawing. Use at 48 px and larger. |
| `icon-small.svg` | The same icon, simplified and bolder. Use at 32 px and smaller (favicon, browser tab). |
| `icon-512.png` | The full icon as a 512 px PNG with a transparent background, for places that need a raster image (registry listings, the Docker Hub icon). |
| `icon-maskable.svg` | The icon drawn full-bleed (no rounded corners, the artwork kept inside the central 80%) so a phone can crop it to any shape. The home-screen icons are made from it. |
| `banner.png` | 1800 x 320 banner at the top of the README (shown at 900 px wide, so 900 x 160). The icon, the name and the tagline on a wide twilight canvas. |
| `live-scene.gif` | 760 px wide, about 14 s: the live scene on the Overview while two senders' photos are handed over. Shown at the top of the README. |
| `arrival-dark.gif`, `phone-dark.gif`, `palette-dark.gif`, `theme-switch.gif`, `services-light.gif`, `invite-qr.gif` | The "In action" table in the README: the numbers rolling during a hand-over (desktop and phone, dark), the command palette (dark), the theme button, a service opening in place with the cards tilting (light), and an invitation with its QR code (light). All are filmed from the real page; see `tools/screenshots` ("The animations"). |
| `send-phone.png`, `send-queue.png`, `send-install.png` | The Send page on a phone (390 px): photos chosen with the service picked, the queue with one photo sent, one sending and one waiting, and the card that offers to install the app. Made by `go run . send <outdir> <photos>` in `tools/screenshots`, which sends three real sample photos through the page, slowly. |
| `social-preview.png` | 1280 x 640 image for the repository's social preview (Settings, General, Social preview) and for link cards. |

The icon is two photo frames with an arrow passing from the back one to the front one, on a twilight gradient. Do not stretch or recolour it; if it is needed at a size between the two drawings, prefer `icon-small.svg` below 40 px.

The icons the page serves for installing it on a phone (`internal/web/static`) are made from these drawings with Inkscape: `icon-192.png` and `icon-512.png` from `icon.svg`, and `icon-maskable-512.png` and `apple-touch-icon.png` (180 px) from `icon-maskable.svg`, for example `inkscape icon-maskable.svg --export-type=png --export-width=180 --export-filename=apple-touch-icon.png`.
