package immich

import (
	"context"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

// SenderPermissions is everything a sender's key needs. workflow.logs (reading its own workflow's run log) is
// only used by `immich-handoff doctor`, which works without it, so a key made before it was added is still fine. It is also what the README tells people to
// grant when they create a key by hand.
var SenderPermissions = perms("user.read", "tag.read", "tag.create", "tag.asset",
	"asset.read", "asset.download", "asset.delete", "workflow.read", "workflow.create", "workflow.update", "workflow.logs")

// SenderPermissionsCopy is what a sender's key needs when originals are kept: no delete permission at all,
// so the app cannot remove a photo even by mistake.
var SenderPermissionsCopy = perms("user.read", "tag.read", "tag.create", "tag.asset",
	"asset.read", "asset.download", "workflow.read", "workflow.create", "workflow.update", "workflow.logs")

// ReceiverPermissions is everything a receiver's key needs.
var ReceiverPermissions = perms("user.read", "tag.read", "tag.create", "tag.asset", "asset.read", "asset.upload")

// ReceiverPermissionsAlbum is what a receiver's key needs when handed-over photos are also put in an album:
// ReceiverPermissions plus finding, creating and adding to albums. It cannot change or delete an album.
var ReceiverPermissionsAlbum = perms("user.read", "tag.read", "tag.create", "tag.asset", "asset.read", "asset.upload",
	"album.read", "album.create", "albumAsset.create")

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
