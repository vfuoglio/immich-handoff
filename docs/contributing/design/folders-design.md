---
title: "Albums made from the folders of an external library"
audience: contributor
---

# Albums made from the folders of an external library

**Built** in 1.4.0 (#198, in #220, #221 and #222). Immich's discussion 4279 (albums created automatically from the folders of an external library) asks for it. What Immich does with an external library, found on 3.4.1: [the API notes](immich-api-notes.md#notes-from-albums-made-from-the-folders-of-an-external-library-immich-341).

## What it does

A person tells the app a folder of their external library, such as `/external/photos`, and how to read the folders below it. Every folder becomes an album of their own, named after it, holding the photos in it. Photos that come later are offered the same way. Nothing is ever removed.

- **Which folder is an album (the level).** `0`, the default: the folder a photo is directly in. `n`: the folder `n` levels below the chosen one. So `/photos/2025-02/paris/a.jpg` is in `paris` at level 0, `2025-02` at level 1 and `paris` at level 2. A photo with no folder at that level (directly in the chosen folder, or shallower) has no album, and the dry run says how many.
- **The name.** The last folder (`paris`) by default; the parent and the last (`2025-02 / paris`) as an option, so two folders with the same name in different places do not merge. With the default name they do merge: both Paris folders feed one album `paris`.
- **Whose photos.** Only the person's own, found with their own key. Photos that are trashed, or are not really under the folder (Immich matches the text anywhere in the path, and the case matters), are left out.
- **The folder is a path the person types.** Immich lets only the administrator list libraries, so the form asks for the folder and says how many of the person's photos it finds under it.

## The finder

`hub.PlanFolders` is a pure function from a spec (folder, level, naming) and the photos' paths to a plan: the albums (name, the folders that feed it, its photos) and the number of photos with no folder. `hub.DryRunFolders` is the read-only look at a real Immich: one search for the folder with a trailing slash (`/photos/`, so `/photos-old` is not found), through every page, then the plan. It changes nothing. The end-to-end test prints it for the mounted library:

```
dry run {Prefix:/external/photos Depth:0 Naming:}:
  Summer trip: 1 photo(s) from Summer trip
  paris: 3 photo(s) from 2025-02/paris, 2025-03/paris
  rome: 1 photo(s) from 2025-02/rome
  3 album(s), 5 photo(s), 1 photo(s) with no folder at that level
dry run {Prefix:/external/photos Depth:1 Naming:}:
  2025-02: 3 photo(s) from 2025-02
  2025-03: 1 photo(s) from 2025-03
  Summer trip: 1 photo(s) from Summer trip
  3 album(s), 5 photo(s), 1 photo(s) with no folder at that level
dry run {Prefix:/external/photos Depth:0 Naming:parent}:
  2025-02 / paris: 2 photo(s) from 2025-02/paris
  2025-02 / rome: 1 photo(s) from 2025-02/rome
  2025-03 / paris: 1 photo(s) from 2025-03/paris
  photos / Summer trip: 1 photo(s) from Summer trip
  4 album(s), 5 photo(s), 1 photo(s) with no folder at that level
```

## Go

Go. Nothing in Immich's workflows or library settings does it (a workflow adds one folder's photos to one named album, made by hand for each folder), the cost of listing is small, and everything needed (`asset.read`, `album.read`, `album.create`, `albumAsset.create`) is a permission a person's own key can have. The rule kind, the key and the pass are in #221, the page in #222.
