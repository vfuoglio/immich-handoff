package immich

import (
	"context"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

// FramePermissions is what a frame's key gets: looking at pictures and listing which photos are in an album (Immich
// lists them with a search, which needs asset.read), nothing more: no downloading originals, no changes.
var FramePermissions = perms("asset.view", "asset.read")

// RoomPermissions is what a client room's key gets: looking at pictures and listing which photos are in an album, and nothing
// else. It cannot download an original: that permission (asset.download) is added by the photographer's own session when
// they release the originals, and never before.
var RoomPermissions = perms("asset.view", "asset.read")

// DigestPermissions is what a service's receiver key is given for the weekly digest: looking at pictures, making and
// filling an album, and making, reading and ending a shared link. The owner adds them (and takes them off) with their
// own session.
var DigestPermissions = perms("asset.view", "album.read", "album.create", "albumAsset.create", "sharedLink.create", "sharedLink.read", "sharedLink.delete")

// KeyPermissions lists what an API key may do. It acts with the person's own session.
func (a *API) KeyPermissions(ctx context.Context, key openapi_types.UUID) ([]Permission, error) {
	r, err := a.c.GetApiKeyWithResponse(ctx, key)
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, statusErr("getApiKey", r.StatusCode(), r.Body)
	}
	return r.JSON200.Permissions, nil
}

// SenderPermissions is everything a sender's key needs. workflow.logs (reading its own workflow's run log) is
// only used by `immich-handoff doctor`, which works without it, so a key made before it was added is still fine.
// stack.read (reading which photos make up a stack) is optional too: without it only the tagged photo of a stack is
// handed over. It is also what the README tells people to grant when they create a key by hand.
var SenderPermissions = perms("user.read", "tag.read", "tag.create", "tag.asset",
	"asset.read", "asset.download", "asset.delete", "stack.read", "workflow.read", "workflow.create", "workflow.update", "workflow.logs")

// SenderPermissionsCopy is what a sender's key needs when originals are kept: no delete permission at all,
// so the app cannot remove a photo even by mistake.
var SenderPermissionsCopy = perms("user.read", "tag.read", "tag.create", "tag.asset",
	"asset.read", "asset.download", "stack.read", "workflow.read", "workflow.create", "workflow.update", "workflow.logs")

// ReceiverPermissions is everything a receiver's key needs. stack.create (making a stack of the photos that arrive
// together) is optional: without it they arrive as separate photos.
var ReceiverPermissions = perms("user.read", "tag.read", "tag.create", "tag.asset", "asset.read", "asset.upload", "stack.create")

// ReceiverPermissionsAlbum is what a receiver's key needs when handed-over photos are also put in an album:
// ReceiverPermissions plus finding, creating and adding to albums. It cannot change or delete an album.
var ReceiverPermissionsAlbum = perms("user.read", "tag.read", "tag.create", "tag.asset", "asset.read", "asset.upload", "stack.create",
	"album.read", "album.create", "albumAsset.create")

// ReceiverPermissionsFor is what a new receiver key gets: ReceiverPermissions, with the album permissions when the
// service puts arrivals in an album, and asset.delete when the administrator allows it (RECEIVER_DELETE, on by default).
// asset.delete lets the app move to the receiver's trash the copies it handed over: a hand-over that expires, or one a
// sender takes back. It never touches a photo the receiver put in an album, and only ever the trash is used.
func ReceiverPermissionsFor(album, canDelete bool) []Permission {
	out := append([]Permission{}, ReceiverPermissions...)
	if album {
		out = append([]Permission{}, ReceiverPermissionsAlbum...)
	}
	if canDelete {
		out = append(out, "asset.delete")
	}
	return out
}

// Optional are the permissions that add a convenience: a key without them still works, and the app carries on without
// that convenience (and `doctor` says so as a warning, not a failure). (workflow.logs is optional too, see above.)
var Optional = map[Permission]string{
	"stack.read":   "only the tagged photo of a stack is handed over, not the whole stack",
	"stack.create": "photos that arrive as a stack arrive as separate photos",
}

func perms(names ...string) []Permission {
	out := make([]Permission, len(names))
	for i, n := range names {
		out[i] = Permission(n)
	}
	return out
}

// CreateAPIKey makes a key in the signed-in account. The secret is only returned here, once.
func (a *API) CreateAPIKey(ctx context.Context, name string, permissions []Permission) (id openapi_types.UUID, secret string, err error) {
	r, err := a.c.CreateApiKeyWithResponse(ctx, CreateApiKeyJSONRequestBody{Name: &name, Permissions: permissions})
	if err != nil {
		return id, "", err
	}
	if r.JSON201 == nil {
		return id, "", statusErr("createApiKey", r.StatusCode(), r.Body)
	}
	return r.JSON201.Id, r.JSON201.Secret, nil
}

// DeleteAPIKey removes one of the signed-in account's keys. Anything still using it gets 401.
func (a *API) DeleteAPIKey(ctx context.Context, id openapi_types.UUID) error {
	r, err := a.c.DeleteApiKeyWithResponse(ctx, id)
	if err != nil {
		return err
	}
	if c := r.StatusCode(); c < 200 || c > 299 {
		return statusErr("deleteApiKey", c, r.Body)
	}
	return nil
}

// KeyAlive reports whether the API key behind this client is still accepted, and its permissions.
// It needs no permission itself, so it is a cheap health check.
func (a *API) KeyAlive(ctx context.Context) (alive bool, permissions []Permission, err error) {
	r, err := a.c.GetMyApiKeyWithResponse(ctx)
	if err != nil {
		return false, nil, err
	}
	switch c := r.StatusCode(); {
	case r.JSON200 != nil:
		return true, r.JSON200.Permissions, nil
	case c == 401 || c == 403:
		return false, nil, nil
	default:
		return false, nil, statusErr("getMyApiKey", c, r.Body)
	}
}

// FindWorkflow returns the id of the workflow with this exact name, or nil.
func (a *API) FindWorkflow(ctx context.Context, name string) (*openapi_types.UUID, error) {
	r, err := a.c.SearchWorkflowsWithResponse(ctx, &SearchWorkflowsParams{Name: &name})
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, statusErr("searchWorkflows", r.StatusCode(), r.Body)
	}
	for _, w := range *r.JSON200 {
		if w.Name != nil && *w.Name == name {
			id := w.Id
			return &id, nil
		}
	}
	return nil, nil
}

func (a *API) DeleteWorkflow(ctx context.Context, id openapi_types.UUID) error {
	r, err := a.c.DeleteWorkflowWithResponse(ctx, id)
	if err != nil {
		return err
	}
	if c := r.StatusCode(); c < 200 || c > 299 {
		return statusErr("deleteWorkflow", c, r.Body)
	}
	return nil
}

// FromTag is the tag a photo from a sender gets where it arrives, when the sender should be told apart: "from/<name>", one
// inside another, so everything from one person is found together. Bridges and event pools both use it.
func FromTag(sender string) string {
	name := strings.TrimSpace(strings.ReplaceAll(sender, "/", "-"))
	if name == "" {
		name = "someone"
	}
	return "from/" + name
}

// PoolPermissionsFor is what a member's key (and the owner's) needs in an event pool, where the same key sends and
// receives: everything a sender that keeps its originals needs, and everything a receiver needs.
func PoolPermissionsFor(album, canDelete bool) []Permission {
	have := map[Permission]bool{}
	var out []Permission
	for _, group := range [][]Permission{SenderPermissionsCopy, ReceiverPermissionsFor(album, canDelete)} {
		for _, p := range group {
			if !have[p] {
				have[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}
