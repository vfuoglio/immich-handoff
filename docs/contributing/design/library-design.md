---
title: "Add to my library, and following an album shared with you"
audience: contributor
---

# Add to my library, and following an album shared with you

**Built** in 1.4.0 (#187, #188). How to use it: [Add to my library](../../admin/find-handoff.md#add-to-my-library-from-inside-immich) and [Following an album shared with you](../../guides/share/follow-album.md#following-an-album-shared-with-you). What Immich allowed, found by a spike on 3.4.1: [the API notes](immich-api-notes.md#notes-from-copying-what-is-shared-with-a-person-into-their-own-library-immich-341).

## The problem

Immich keeps every photo in the account that uploaded it. A shared album and partner sharing let a person *look* at a photo; if the owner deletes it or stops sharing, it is gone. The most voted sharing request on Immich's side (about 578 votes) is to copy such a photo into your own library. Immich has not built it, so this project does it from outside, using only the public API.

## What a copy is

A viewer can read, download and upload what is shared with them (a session and a restricted key both can). So the app **downloads the original with the person's own sign-in and uploads it to the same person**: a real photo of theirs, with the description, rating, location and archived state (carried in the XMP sidecar of the upload, so no permission to update an asset is needed) and, for a live photo, the clip first and hidden, then the still linked to the person's own clip. It is checked by checksum. The owner is not asked or told: it is the same as downloading the photo and uploading it again. The original is never touched.

- **Repeat:** Immich answers `duplicate` with the same asset (the check is per owner), so a second press is harmless.
- **A copy taken out and added again** (Undo, or the person's own delete): Immich answers `duplicate` with the one in the trash, so it is restored and filed like a new copy.
- **Where it goes**, the person's choice, remembered per person: tagged with who shared it (`from/<name>`), in the library as it is, or in an album (found or made by name). The tag is added after Immich has read the new upload and checked to have stuck, as in a hand-over.
- **Undo** (15 minutes): trashes, into Immich's restorable trash, only copies that the person owns, that were added in the last 15 minutes and that they have not filed in an album of their own (the destination album does not count).

## The button

`GET /immich/owned` says which photos are the person's own and which are someone else's they can see; the script shows Hand over for the first and **Add to my library** for the second, both for a mix. The first press asks where copies go (with a list of the person's albums); once remembered, a press adds at once. See [the button's design](immich-button-design.md).

## Following an album

A rule of **kind library** (`store.Rule.Kind`) has no service. It follows the album in `Cond.Album` (with who shared it, `Cond.AlbumOwner`) and keeps `Dest` and `DestAlbum`. It reuses the rules machinery: suggestions with pictures, **Send / Not these / Always**, automatic after three **Always**, pause, and the activity log.

- **The key.** The rule runs on a timer, so it needs a key. The person makes it from their own sign-in the first time they follow an album: `handoff: library`, with `user.read album.read asset.read asset.download asset.upload tag.read tag.create tag.asset` (and `album.create albumAsset.create` only when copies go to an album, added to the same key). It is stored sealed, can be deleted by the person in Immich at any time (the rule then says so), and is deleted by the app when the last followed album ends. It cannot delete or change a photo.
- **What the rule finds.** Every pass reads the whole album with the key (the album is the filter: a photo's upload date says nothing about when it was added to the album). Photos that are the person's own, in the trash, not photos or videos, not yet settled (`RULES_SETTLE_MINUTES`) or already known to the rule are left out; the rest are offered with the reason "added to the album X, shared by Y".
- **What was in the album when it was followed** is only remembered (state `seen`), never offered, unless the person chose to start with everything.
- **By itself.** After three **Always** the person may let the rule copy by itself: photos that are new at a pass are copied, what was already waiting stays a suggestion.
- **When the album stops being shared** (or is deleted) the rule shows "the album is not shared with you any more"; nothing else breaks.

## What it cannot do

Show shared albums in the person's own timeline, or real user groups: those need Immich. Copies are the answer, not those. A copy takes as much room as the photo.

## How it is checked

Hub tests with a small Immich of their own (`internal/hub/library_test.go`, `libraryfollow_test.go`), web and store tests, the script's pure helpers (`handover-core.test.ts`), and on a real Immich in a real browser `TestAddToMyLibrary` and `TestFollowSharedAlbum`.
