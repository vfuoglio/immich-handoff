package immich

import (
	"context"
	"sort"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

// Users lists every account on the server with its name and email. Immich lets any signed-in user do it, which is why the app
// offers it only when the administrator allows it.
func (a *API) Users(ctx context.Context) ([]User, error) {
	r, err := a.c.SearchUsersWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, statusErr("searchUsers", r.StatusCode(), r.Body)
	}
	out := make([]User, 0, len(*r.JSON200))
	for _, u := range *r.JSON200 {
		out = append(out, User{ID: u.Id, Name: u.Name, Email: string(u.Email)})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// CirclePermissions is what the key for circles needs: to read the person's albums (and who they are shared with), and to add a
// person to an album and take one off. It cannot read a photo, change or delete an album, or touch anything else.
var CirclePermissions = perms("user.read", "album.read", "albumUser.create", "albumUser.delete")

// ShareAlbum adds a person to an album of this account, as "viewer" or "editor". One who is already on it is not an error.
func (a *API) ShareAlbum(ctx context.Context, album, user openapi_types.UUID, role string) error {
	r := AlbumUserRole(role)
	resp, err := a.c.AddUsersToAlbumWithResponse(ctx, album, AddUsersToAlbumJSONRequestBody{AlbumUsers: []AlbumUserAddDto{{UserId: user, Role: &r}}})
	if err != nil {
		return err
	}
	if resp.JSON200 == nil {
		if resp.StatusCode() == 400 && strings.Contains(strings.ToLower(string(resp.Body)), "already") {
			return nil
		}
		return statusErr("addUsersToAlbum", resp.StatusCode(), resp.Body)
	}
	return nil
}

// UnshareAlbum takes a person off an album of this account. One who is not on it (or an album that is gone) is not an error.
func (a *API) UnshareAlbum(ctx context.Context, album, user openapi_types.UUID) error {
	resp, err := a.c.RemoveUserFromAlbumWithResponse(ctx, album, user.String())
	if err != nil {
		return err
	}
	if c := resp.StatusCode(); c >= 300 && c != 400 && c != 404 {
		return statusErr("removeUserFromAlbum", c, resp.Body)
	}
	return nil
}
