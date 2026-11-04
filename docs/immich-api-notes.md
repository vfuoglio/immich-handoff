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
- A tag applied in the first moments after an upload is occasionally dropped by Immich itself (about 1 in 50 in tests with no handoff app running). The same happens to a tag added and another removed on one photo within milliseconds, so the app adds the new tag, checks it, and only then removes the old one.
- Immich is "healthy" about 30 seconds before its workflow plugins are loaded; creating a workflow earlier fails with `Unknown method`.
