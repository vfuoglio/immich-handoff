# Rules: hand-over by people, places and dates

The conclusion of #153, and the design #129 is built from. The question was whether Handoff can suggest, and then hand over, photos by **who is in them, where and when** with nothing tagged by hand, and what Immich gives for it.

## Conclusion

**It can be done with what Immich already offers, and without waiting for face detection.** One metadata search (people, dates, place, album) finds candidates; a few checks the app makes itself (archived, not the sender's own, "except" conditions) finish the job; and because Immich never says that face detection is *finished*, a rule never decides "no match": it looks again at recent photos on every pass, so a face found an hour after upload simply makes the photo appear later. Sending is the app tagging the photos with the service's tag through the sender's own key, so everything that already exists (limits, quiet hours, take-back, the ledger, the live scene) applies unchanged.

## What was checked

On a real Immich 3.2.0 (without machine learning, so faces were made by hand with `POST /faces`, which is also how the end-to-end test gets people). The check stays in the suite as `TestRulesImmichRules` (hub group).

| Question | Result |
|---|---|
| Which permissions? | **Searching by person and reading who is in each result needs only `asset.read`.** Listing the people to choose from (and their thumbnails) needs `person.read`. City and country suggestions need only `asset.read`. The picker in the page runs with the person's own session, so the sender's key needs no new permission for the picker; the key used by the timer needs `asset.read` (it has it) and `asset.view` is not needed (the card's thumbnails go through the person's session). |
| Several people in one search? | **All of them** (a photo with Nora *and* Bob). "Except" conditions are therefore checked by the app, on the people each result carries (`withPeople`) and on the photos of the albums to leave out. |
| Dates? | `takenAfter` and `takenBefore` (the date in the photo) and `createdAfter` (when it reached Immich: "from now on" uses this one). They combine with people and places. |
| Places? | Immich reverse-geocodes GPS on its own: the city can be a **neighbourhood** ("Graça" for a point in Lisbon) and matching is **exact and case-sensitive**. The values to offer come from `/search/suggestions?type=city|country`, so a rule picks from what the library really holds and never types a place. |
| Archived, locked, trashed photos? | A default search **returns archived photos**; locked ones are not returned. The app leaves out `isArchived` and anything that is not in the timeline itself. |
| Is a photo ready? | An asset says `hasMetadata` and a `thumbhash` when metadata and thumbnail are done. **Nothing says that face detection (or the smart-search embedding) has finished.** So: a rule looks again at every recent photo on every pass; a photo "settles" after a few minutes (5 for a suggestion, 30 for auto) before it can be offered, so that a second face and an "except" person have had time to appear. |
| People that disappear? | A deleted person matches nothing, and is gone from the list; a rule keeps the person's id and notices it is no longer in the list ("this person is gone, choose again"). Merging could not be tried on a server without machine learning (the call answers an empty list), the same notice covers it. |
| Smart search? | `POST /search/smart` needs machine learning; without it the answer is **400 "Smart search is not enabled"**, which the rule shows plainly. It cannot be tested end to end here. A phrase has no certain match (Immich answers its closest photos), so a **phrase rule is suggestion-only, never automatic**. |

## Design

### The rule
A rule belongs to **one sender and one service** they have joined (the way the watched album does). It holds: people (all must be in the photo), people to leave out, a date range (taken), a place (country, optionally city, from the suggestions), an album to take from, albums to leave out, a phrase; at least one condition must be set. It has a **mode** (suggest, or auto when offered and chosen), the number of times the person answered *Always*, and a "from now on" moment (`since`). It can be paused and deleted.

### The pass
A timer (`RULES_CHECK_MINUTES`, default 10) and each pass look at every active rule: search with the conditions and `createdAfter` = the later of `since` and a week ago (a rule looks at what came in this week, so the cost stays small and a late face is still seen); take the results the person owns; leave out archived, trashed, not-yet-settled, the "except" people and albums, and anything the rule has already suggested, sent or been told *not* about; the rest are **candidates**, each with a line saying why it matched ("Nora is in it", "taken 3 May", "Portugal", "in the album Summer", "looks like 'beach'").

### Suggestions
Candidates join the rule's **open batch** (one per rule; it grows until answered), shown as a *Needs you* card with thumbnails, **Send**, **Not these** and **Always**; the same through a notification with one-tap buttons and on a review page where single photos can be unticked. **Send** tags the photos with the service's tag using the sender's key: from there the normal hand-over does the rest. **Not these** remembers the photos as declined for that rule; the rule stays. **Always** sends and counts; after three, **auto** is offered.

### Auto
Sends without asking, only photos that arrive **after the switch**, once they have settled for 30 minutes, within the service's limits and quiet hours, and can be taken back like any hand-over. A rule with a phrase never becomes automatic. The person can switch back to suggest at any time.

### Look back
A button on a rule suggests the older photos that match (up to 200, newest first) as a batch of suggestions; nothing old is ever sent without a yes.

### Library-wide routing (later)
A person's rules can route to several services; a photo that matches rules for two services is offered to each, once. Open: ordering and conflicts between rules; not part of the first release.

### What a rule can see
Only what a search shows the sender's own key: their own photos, and who is in them. A rule never reads a face, only the names Immich already gives.

## Answers to the open questions of the child issues
- **#154 the form:** people from the person's own session (names and thumbnails), places from the suggestions, no free text for places.
- **#155 what Immich filters:** people (all), dates, place, one album and the phrase are Immich's; "except" people and albums, the sender's own, archived and settled are the app's.
- **#157 notification:** the existing signed one-tap links.
- **#160 conflicts:** a photo matching two rules of different services is offered to each.

## Risks, plainly
- Face detection is imperfect and names can be wrong; a wrong photo can be suggested, which is why nothing leaves without a yes until the person chooses auto, and auto waits for the photo to settle.
- Smart search has no certain answer and needs machine learning on the server.
- A rule looks at a week of recent photos; a face found later than that is not seen by the rule (the look-back button covers it).
- Immich's city can be a neighbourhood: the picker offers what the library holds, but the person may be surprised.
