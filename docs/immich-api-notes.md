# Immich API behaviour this project relies on

Facts verified against a real Immich v3.2.0 (see `test/`). Re-check them with `IMMICH_VERSION=<tag> test/run.sh` before upgrading.

## Sign-in sessions
- `POST /auth/login` sets the cookies `immich_access_token` (HttpOnly), `immich_auth_type` (HttpOnly) and `immich_is_authenticated`. Browsers share cookies across ports of one hostname, so an app on `host:8080` receives the cookie of Immich on `host:2283`.
- A forwarded cookie is accepted by `GET /api/users/me`, and by the thumbnail endpoint `GET /api/assets/{id}/thumbnail`. Without credentials the thumbnail returns 401.

## API keys
- A signed-in session (cookie or bearer token) can create an API key with a restricted permission list: `POST /api/api-keys` with `{"name", "permissions": [...]}` returns 201. The `secret` is only returned in that response.
- Keys can be named, and the owner can list (`GET /api/api-keys`) and delete (`DELETE /api/api-keys/{id}`, 204) their own keys with the session.
- `GET /api/api-keys/me` works with any key (no permission needed) and returns the key's own permissions, which makes it a cheap health check.
- Once a key is deleted, every call that uses it returns 401.

- `GET /api/api-keys` returns each key's name and permissions, which is how the tests confirm a copy-mode sender key has no `asset.delete`.
- Removing a tag from a photo (`DELETE /api/tags/{id}/assets`) works with `tag.asset` alone; `tag.delete` is not needed.

## Workflows
- A user can hold several workflows at once (two created with different names both listed).
- Workflow step config is stored in the owner's account; the webhook step sends `{type, trigger, data:{asset:{id, ownerId, ...}}}`.

## Users
- A non-admin user can list the server's users with `GET /api/users` (id, email, name, avatar colour). Anything built on this shows every user's email to every signed-in user.

## Search and tags
- Search pages with `page` and `nextPage`; `nextCursor` is never returned in 3.2.0.
- A tag applied in the first moments after an upload is occasionally dropped by Immich itself (about 1 in 50 in tests with no handoff app running). The same happens to a tag added and another removed on one photo within milliseconds, so the app adds the new tag, checks it, and only then removes the old one. The end-to-end tests' tagging helpers (`tagAsset`, `tagLikeThePhone`) check that a tag stuck and apply it again when it did not, so a dropped tag does not fail a run; they log when they had to.
- Immich is "healthy" about 30 seconds before its workflow plugins are loaded; creating a workflow earlier fails with `Unknown method`.

## Notes from handing over photos on a date, taking a hand-over back, and letting copies expire

Checked against a real Immich 3.2.0 (`test/e2e`):

- A tag inside another (`share/2999-01-01`) sends the same `AssetTagged` workflow event as any tag, and a search for the parent tag (`share`) also lists photos that carry only the child tag. The app therefore skips photos whose date has not come when the plain tag lists them.
- `POST /trash/restore/assets` needs `asset.delete`, the permission that moves photos to the trash.
- `GET /albums?assetId=` needs `album.read`, which a receiver's key does not have. To learn whether a photo is in an album the app searches for it among the photos in no album (`isNotInAlbum`, with the `id` filter), which needs only `asset.read`.
- `createdBefore` in a search is the date the asset was uploaded (the `createdAt` column), not the date the photo was taken; that is what a copy's lifetime is counted from. The end-to-end test ages a copy by changing that column in the throwaway Immich's database.
- A key cannot change its own permissions or make a key with permissions it lacks, which is why a receiver key gets or loses `asset.delete` when its owner opens the page (their own login changes it), not at boot.

## Notes from sharing, showing and notifying

Checked against a real Immich 3.2.0 (`test/e2e`) and, for push, a real Chrome and Google's push service:

- `AlbumResponseDto` (`GET /albums/{id}`) has no `assets` list in 3.2.0. The photos in an album are found with a search (`POST /search/metadata` with `albumIds`), which needs `asset.read`; so a key that only lists an album's photos and shows pictures is `asset.read` + `asset.view` (this is what a frame's key has).
- `GET /assets/{id}/thumbnail?size=preview` needs `asset.view` and works for the owner's own photos with such a key; a drop box's wall and a frame use it.
- A shared link is made with `sharedLink.create`, read with `sharedLink.read` and removed with `sharedLink.delete`; Immich allows downloads from a link only when metadata is shown.
- A key can add or take away permissions of another key only with the owner's own session (`PUT /api-keys/{id}`), which is why the digest and the frame ask the owner when they are turned on and off.
- A new push subscription can take a few seconds to be known to Google's push service: a push sent at once may be answered `410 Gone`. The app treats `404` and `410` as "forget this device", except for a device made in the last two minutes, which is kept so the test button can be pressed again.
- Photos taken at the same moment look alike to the best-of rules; they are compared by a small hash of the preview, so the preview must exist first (Immich makes it in the background, seconds after an upload).
