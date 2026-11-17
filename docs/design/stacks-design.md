# Stacking by rule

**Built** in 1.4.0 (#197, in #223, #224 and #225). Immich's discussion 2479 (automatic stacking) asks for it. What Immich allowed, found on 3.4.1: below.

## What Immich does already

- **Mobile edits (3.3.0).** Immich's phone app stacks an edit made in another app over the original when it uploads. That covers edits that are uploaded from the phone. It does not cover a camera's RAW and JPEG, bursts, edits that were already in the library, or an external library, which is what this does.
- **Workflows (core plugin 2.0.1 on 3.4.1).** Filters on file name, path, date, tags, EXIF and so on, and actions to archive, favourite, tag, lock or add to albums. There is **no stack action**.
- **Nothing built in** for stacking by file name or time. The community tools (a command-line tool that groups by file name, a workflows plugin) are outside Immich. If Immich ships a built-in detector, this goes.

## Facts found on 3.4.1

- **A search never says which stack a photo is in.** The `stack` field of a search result is empty, with `withStacked` set or not (a single `GET /assets/{id}` does carry it). But `withStacked: false` leaves **every** stacked photo out, the cover too, while `withStacked: true` (or not set) lists them all. So the app lists both ways and calls a photo stacked when only the first lists it. It costs one more search, not one request for each photo.
- **EXIF comes with the search** (`withExif`): the make, the model and `dateTimeOriginal` (which is the moment the photo was taken, and is what a RAW and its JPEG share). A file that has a RAW's name and extension and a JPEG's bytes is read the same way, which is what the end-to-end samples are.
- **A key with `user.read asset.read stack.read stack.create stack.update stack.delete`** can make a stack (the first photo given is the cover), read it and take it apart, and cannot delete a photo (`asset.delete` is missing).
- **Taking a stack apart** is `DELETE /stacks/{id}`: the photos stay, each alone again.

## The grouping

`hub.PlanStacks` is a pure function from a spec and the photos (path, taken, make, model, stacked) to groups. A photo is in at most one group; the strong signals come first.

- **Pair:** a RAW (`.cr2 .cr3 .nef .arw .dng .raf .orf .rw2 .pef ...`) and another image with the **same base name in the same folder**, taken within two seconds. Case does not matter.
- **Edit:** the same stem in one folder, taken within two seconds, where a file's name ends with `-edited`, `_edited`, `-edit`, `_edit` or a number in brackets (`IMG_1 (1).jpg`). A RAW, its JPEG and an edit are one pair.
- **Burst:** three or more shots from one camera (make and model) in one folder, each within 1.5 seconds of the last. Never for RAW. Always asks, never goes by itself.
- **Not groups:** the same name in another folder, or hours later (the camera's counter came round), two plain files that share a name, two shots a second apart, a camera with no make.
- **Cover:** the plain JPEG first (the default), then the edits, then the RAW; or the RAW first; or an edit first.
- **Already stacked:** a photo in a stack is left alone and a group with one among its members is skipped, never merged.
- **A folder** limits it, with a trailing slash and a check that the path starts with it.

## The read-only run

`hub.DryRunStacks` lists the person's images with their EXIF, asks both ways for the stacked ones, and prints the groups. Over the eleven sample photos (`test/external-photos/stacks`, made by `test/gen-stack-samples`) it finds exactly three:

```
burst: DSC_0101.jpg, DSC_0102.jpg, DSC_0103.jpg
pair: IMG_1001.JPG, IMG_1001.DNG
edit: IMG_1002.jpg, IMG_1002-edited.jpg
```

and none of the other eight: the same name in another folder two hours later, two shots a second apart, and a photo alone. After the pair is stacked by hand the run finds two groups. No other photo of the shared test Immich makes a group.

## Go

Go. The grouping is cheap and certain for pairs and edits (a shared name and moment), and kept apart from bursts, which are the guess and always ask. Everything it needs is a permission the person's own key can have. The rule kind, the key and the undo are in #224, the page in #225.
