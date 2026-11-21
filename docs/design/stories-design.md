# Stories

**Built** in 1.4.0 (#272). Immich's discussion 2836 (a showcase of memories, 238 votes) and reviewers ask for curated recaps; Immich's own memories cannot be sent to someone with no account.

## What it does

A story is a short animated recap with a public link: a title, chapter cards, about forty of the best photos with their dates and places, and an end. It is made from an album, a person's photos, a range of dates or a drop box.

## How

- **Choosing the photos** (`internal/hub/storybuild.go`). Once, in the background, with the owner's session. The candidates come from the source (`StoryAssets`: one search with the place and the people of each photo, newest first, at most 3000; a drop box's photos that the host did not hide, through `Asset`, at most 300), filtered to photos that are not trashed or archived and fall inside the story's dates, and **sampled to at most 600 evenly over time** because the cost is one preview per photo. `analyseAssets` looks at each (the same check as a drop box's best-of: too dark, too bright, blurred, or the same moment as a better shot; `judge`), then `selectStory` keeps about 40: the time is cut into months (or days for a span under 45 days), each stretch gets a share by the largest-remainder method on the **square root** of its photo count (so a holiday month with most of the photos does not take most of the slots) and at least one, and inside a stretch the sharpest win, a favourite counting a quarter more (and a pinned drop box photo counting as one). The result is in time order.
- **Chapters** (`chaptersFor`). A span of 45 days or more is cut by month. A shorter one is cut by place when most of the photos have a city (a run in one city is a chapter; a run of one photo joins the chapter before), otherwise by day, or by week when there are more than twelve days. A chapter with no name is shown with its dates in the reader's language.
- **What is stored** (`stories`): the picks (the photo's Immich id, chapter, date, place, names) and the chapters. The public page never reads the library: it is given a document with opaque ids (`hmac(token, asset)[:16]`) and only the words the switches allow (`StoryDocOf`). Coordinates are never stored.
- **The link.** A 256-bit token like a frame's, an end date (7, 30, 90 days or none), and a key made with the owner's session (`FramePermissions`: `asset.view`, `asset.read`), sealed like the others. Unknown, being made, ended and expired stories all answer 404. `StoryMaintenance` (every ten minutes) ends the ones whose date has come; removing a story deletes the row, the key (with the owner's session) and, by being unreachable, its pictures in the shared in-memory cache (48 MB, FIFO).
- **Pictures** are the previews, fetched with the story's key and served with `private, max-age=3600` and `Content-Security-Policy: default-src 'none'; sandbox`, only for ids in the story. The **cover** (for chat previews) is served only while the owner's preview switch is on.
- **The player** (`ts/story-core.ts`, `ts/story.ts`). `buildTimeline` makes the scenes (title 3.5 s, chapter 3 s, photo 6 s, end waits for a tap); the runner reuses `stage.ts` (`present`, the zoom drift, the lite mode) with a blurred copy of the photo behind it, a progress bar moved with `transform` only, tap/swipe/hold, keys, and a pause when the page is hidden. No sound.
- **The TV.** A story is a cast source (`CastStory`): the TV is given the picks in order with a caption (chapter, date, place, as the story allows) and plays them as an ordered slideshow, round and round; the legacy TV page plays the photos in order without captions.
- **The year card.** `StoryYearOffer` runs when the person opens their page in December or the first half of January: one search for the year's photos with their own session (kept an hour), at least 30, and no answer yet in `story_offers`. Making the story of that year, or "Not this year", is recorded.

## Not done

- No music, and no video export: a story is a page, not a file. Screen recording is how to keep one.
- Videos are not part of a story.
- A story is a snapshot: new photos need **Pick the photos again**.
- The expiry of a link does not delete the story's key (only the owner's sign-in can); removing the story does.
