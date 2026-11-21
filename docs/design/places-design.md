# Guess missing places

**Built** in 1.4.0 (#273). Immich's discussion 1675 (92 votes) asks for it. The facts about Immich below were checked on 3.4.1 by `TestPlacesSpike`.

## What it does

A rule of kind `place` offers a place to the person's photos that have none, taken from a located photo of the same camera taken minutes from them. It asks first, shows both photos and why, and writes the place with a key of the person's own.

## The facts about Immich

- A place is written with `PUT /assets` (`updateAssets`, `asset.update`) with `latitude` and `longitude`: HTTP 204. Immich **finds the city and the country itself** a little later (Lisbon, Portugal for 38.7223, -9.1393 in the test). The call is deprecated in the spec but works on 3.4.1.
- A place **cannot be cleared**: `latitude` and `longitude` are numbers, and a null in `PUT /assets/{id}` or in the bulk call is answered with 400 (`expected number, received null`), so the app cannot restore "no place". There is therefore **no undo**, and the feature says so before the first yes. If a later Immich accepts a null, an undo like the stacks' (a record of what was written, kept 15 minutes, and checked to be unchanged) is the way to add it.
- A place written into a photo of an **external library** whose files are read-only is accepted (HTTP 204) and **not kept**: 30 seconds later the photo still has no place (`TestPlacesSpike`). Uploaded photos keep it at once. The app cannot tell beforehand, so `lookPlace` counts, on each look, the photos it gave a place that still have none, and puts the problem on the rule ("Immich did not keep the place written into N photos ...").
- There is no search filter for "no location": the app reads the photos with their EXIF (`withExif`) and decides in Go from the latitude and longitude. A search has no time-of-shooting window except `takenAfter` and `takenBefore`, which the app uses to read the photos around a day.

## How

- **The finder** (`internal/hub/placefind.go`) is pure and table-tested. Per camera (make and model, lowercased) the photos are put in the order they were taken. For each photo with no place, the nearest photo with a place before and after it are the neighbours (a photo with no place in between is skipped). A neighbour counts only on the same local day (`LocalDateTime`), with the same lens when both have one and the same time zone when both have one. **Both sides within 15 minutes:** the two places must be at most 2 km apart and at most 60 km/h from each other, otherwise there is no guess (the camera travelled); the place is the nearer neighbour's, and the guess is *two-sided*. **One side within 10 minutes:** a *one-sided* guess. Photos with no camera or no time of shooting are skipped.
- **The look** (`lookPlace`, in the rules' round). The candidates are the person's own photos, images only, not trashed or archived, with no place, a camera and a time, that arrived in the last week (and after the rule), have been in Immich for the usual wait and have not been handled; at most 200 a pass. Then, for each distinct day of the candidates (at most 30, newest first), the photos taken around that day are read (at most 800) to find the located neighbours. The neighbour's id is kept as a `seen` row so the review page can show its picture; the guess is stored as the candidate's reason (`placeWhy`: sides, place, neighbour, minutes, metres, the words of the place).
- **Writing** (`placeForRule`): the chosen photos are grouped by the place they get and written together, 200 at a time. A photo Immich refuses stays a suggestion.
- **By itself.** After three *Always* the person may let it go on by itself. Only a **two-sided** guess for a photo that arrived after the switch and has been in Immich six times the wait is written without asking; a one-sided guess always asks.
- **The key.** `handoff: places`: `user.read`, `asset.read`, `asset.update`. `asset.update` is broad (description, rating, date); the app only writes a place. It is made with the person's own session, sealed in `place_keys` (vault context `places:`) and deleted with the rule.
- **The page.** A card in Settings, a Needs-you card with the photos, and a review page with each photo beside its neighbour, the reason and a plain link to OpenStreetMap (`mlat`, `mlon`) that loads nothing until clicked: no map, no tile and no `img-src` change. Writing asks once more in a dialog.

## Not done

- No undo (above).
- No reverse-geocoding by the app: the place's name is the neighbour's city and country as Immich holds them.
- No guess from photos of other cameras or other days, and none from a photo's own faces or content.
