# Album tree

**Built** in 1.4.0 (#274). Immich's discussions 2073 (nested albums, 394 votes) and 5078 (tree-structured album folders, 146 votes) ask for it. Immich itself has no nested albums.

## What it does

A page, `/albums`, lists the person's own albums and the ones shared with them, and shows the names as a tree by their slashes: `Trips / 2024 / Rome` is the album `Rome` in the folder `2024` in the folder `Trips`. A helper renames an own album to move it into a folder.

## How

- **Splitting.** `internal/albumtree` is a pure package: `Split` cuts a name on `/`, trims each part and drops empty parts (so `Trips/2024/Rome` and `Trips / 2024 / Rome` are the same), stops at eight folders, and keeps the rest in the title. `Build` nests the entries, treats folder names as the same whatever the capitals, orders folders and albums by name, keeps two albums with the same path side by side and counts albums and photos below each folder. `Rename` is the new name for a move: the folders, then the album's own title, refused when it would be longer than 100 characters (the limit the app uses for the albums it makes).
- **Reading.** The person's own session (their cookie) lists their albums (`getAllAlbums`, owned and not owned). No key is made and nothing is stored: the page works for anyone signed in to Immich, whether or not self-service is on.
- **Covers.** Served by album id (`GET /albums/{id}/cover`). The app looks the album up in the person's own lists and asks Immich for the album's thumbnail photo with their session; an id that is not in their lists is a 404, and a photo id is never taken from the address.
- **Renaming.** `PATCH /albums/{id}` with `albumName` and nothing else (`updateAlbumInfo`), with the person's session. The hub looks the album up in their **own** list first, so an album that is not theirs is refused before Immich is asked. The page shows the old and the new name before the move, and writes nothing until the person presses the button. Checked on Immich 3.4.1: the owner's session may rename an album, and nothing the app does touches an album that is somebody else's (see `TestAlbumTree`).
- **Links.** Each album opens at `PUBLIC_IMMICH_URL/albums/<id>` in a new tab; without that setting the names are plain text.

## Not done

- The app does not rename in bulk: each move is one album and one confirmation.
- Immich shows no tree of its own, so the tree exists only on this page.
- A big library loads all its albums in one call (Immich has no paging for that list).
