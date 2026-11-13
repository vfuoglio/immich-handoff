package immich

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

// DropBoxPermissions is everything the key of a drop box needs: storing a photo and tagging it, and nothing else. It
// cannot read a photo back, change or delete one, or touch an album, so a stolen key can only add photos.
var DropBoxPermissions = perms("user.read", "tag.read", "tag.create", "tag.asset", "asset.upload")

// UploadFile stores a file that is read from r, with no copy kept on disk, as a new photo or video of the
// account. When Immich already has the same file it answers with that one, and duplicate is true. A reader that
// fails (a file that turns out too large, a guest who went away) fails the upload and nothing is stored.
func (a *API) UploadFile(ctx context.Context, name string, r io.Reader, at time.Time) (id openapi_types.UUID, duplicate bool, err error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	watch := time.AfterFunc(StallTimeout, func() { cancel(ErrStalled) })
	defer watch.Stop()
	body := &progressReader{r: r, onRead: func() { watch.Reset(StallTimeout) }, onEOF: func() { watch.Stop() }}

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		pw.CloseWithError(func() error {
			for k, v := range map[string]string{
				"fileCreatedAt":  at.UTC().Format(time.RFC3339Nano),
				"fileModifiedAt": at.UTC().Format(time.RFC3339Nano),
			} {
				if err := mw.WriteField(k, v); err != nil {
					return err
				}
			}
			h := make(textproto.MIMEHeader)
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="assetData"; filename=%q`, name))
			h.Set("Content-Type", "application/octet-stream")
			part, err := mw.CreatePart(h)
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, body); err != nil {
				return err
			}
			return mw.Close()
		}())
	}()
	resp, err := a.bulk.UploadAssetWithBodyWithResponse(ctx, nil, mw.FormDataContentType(), pr)
	pr.CloseWithError(err)
	if err != nil {
		return id, false, stalled(ctx, err)
	}
	switch {
	case resp.JSON201 != nil:
		return resp.JSON201.Id, false, nil
	case resp.JSON200 != nil:
		return resp.JSON200.Id, true, nil
	}
	return id, false, statusErr("uploadAsset", resp.StatusCode(), resp.Body)
}

// DropBoxPermissionsAlbum is DropBoxPermissions for a box that files what arrives in an album: it can also find, make
// and add to albums, and still cannot change, share or delete one, or read a photo back.
var DropBoxPermissionsAlbum = perms("user.read", "tag.read", "tag.create", "tag.asset", "asset.upload",
	"album.read", "album.create", "albumAsset.create")

// FindUserByEmail looks for a person on this Immich server by their exact email, for choosing a co-host. It returns
// false when nobody has that address.
func (a *API) FindUserByEmail(ctx context.Context, email string) (User, bool, error) {
	r, err := a.c.SearchUsersWithResponse(ctx)
	if err != nil {
		return User{}, false, err
	}
	if r.JSON200 == nil {
		return User{}, false, statusErr("searchUsers", r.StatusCode(), r.Body)
	}
	for _, u := range *r.JSON200 {
		if strings.EqualFold(string(u.Email), strings.TrimSpace(email)) {
			return User{ID: u.Id, Name: u.Name, Email: string(u.Email)}, true, nil
		}
	}
	return User{}, false, nil
}

// DropBoxPermissionsFor is the key of a new drop box: the five a box needs, the album ones when it files into an album,
// asset.read and asset.delete only when the owner lets guests delete what they sent (to check a photo is still in the
// inbox, and to move it to the owner's trash, never to delete it for good), asset.view when guests may look at the
// photos (pictures only) and asset.download when they may download them.
func DropBoxPermissionsFor(album, guestDelete, view, download bool) []Permission {
	out := append([]Permission{}, DropBoxPermissions...)
	if album {
		out = append(out, "album.read", "album.create", "albumAsset.create")
	}
	if guestDelete {
		out = append(out, "asset.read", "asset.delete")
	}
	if view {
		out = append(out, "asset.view")
	}
	if download {
		out = append(out, "asset.download")
	}
	return out
}

// CreateAlbumLink makes a view-only shared link for an album of this account, so people with no Immich account can
// look at it and download from it. It returns the link's id (to remove it later) and its key (the address is
// <Immich>/share/<key>). A zero expiry means it does not expire; an empty password means none. This needs the
// signed-in person's own session (or a key with sharedLink.create), never a drop box's key.
func (a *API) CreateAlbumLink(ctx context.Context, album openapi_types.UUID, expires time.Time, password, description string) (id openapi_types.UUID, key string, err error) {
	// Immich lets a shared link allow downloads only when it also shows metadata (dates, places, cameras), so both are on
	yes, no := true, false
	body := CreateSharedLinkJSONRequestBody{Type: ALBUM, AlbumId: &album, AllowDownload: &yes, AllowUpload: &no, ShowMetadata: &yes}
	if !expires.IsZero() {
		body.ExpiresAt = &expires
	}
	if password != "" {
		body.Password = &password
	}
	if description != "" {
		body.Description = &description
	}
	r, err := a.c.CreateSharedLinkWithResponse(ctx, body)
	if err != nil {
		return id, "", err
	}
	if r.JSON201 == nil {
		return id, "", statusErr("createSharedLink", r.StatusCode(), r.Body)
	}
	return r.JSON201.Id, r.JSON201.Key, nil
}

// RemoveLink ends a shared link of this account. One that is already gone is not an error.
func (a *API) RemoveLink(ctx context.Context, id openapi_types.UUID) error {
	r, err := a.c.RemoveSharedLinkWithResponse(ctx, id)
	if err != nil {
		return err
	}
	if c := r.StatusCode(); (c < 200 || c > 299) && c != 404 {
		return statusErr("removeSharedLink", c, r.Body)
	}
	return nil
}

// Thumbnail fetches a picture of a photo: its small thumbnail or its larger preview, as Immich made it (a JPEG or a
// WebP). It needs asset.view. At most 8 MB are read.
func (a *API) Thumbnail(ctx context.Context, id openapi_types.UUID, preview bool) (data []byte, contentType string, err error) {
	size := Thumbnail
	if preview {
		size = Preview
	}
	resp, err := a.bulk.ViewAsset(ctx, id, &ViewAssetParams{Size: &size})
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, "", statusErr("viewAsset", resp.StatusCode, b)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return data, resp.Header.Get("Content-Type"), err
}

// RemoveFromAlbum takes photos out of an album of this account (they stay in the library). One that is not in it is not
// an error.
func (a *API) RemoveFromAlbum(ctx context.Context, album openapi_types.UUID, ids ...openapi_types.UUID) error {
	r, err := a.c.RemoveAssetFromAlbumWithResponse(ctx, album, RemoveAssetFromAlbumJSONRequestBody{Ids: ids})
	if err != nil {
		return err
	}
	if r.JSON200 == nil {
		return statusErr("removeAssetFromAlbum", r.StatusCode(), r.Body)
	}
	return nil
}

// Original opens the original file of a photo, to be streamed to a guest who may download it. It needs asset.download.
// The caller closes the body. There is no limit on the time it takes: a video can be large.
func (a *API) Original(ctx context.Context, id openapi_types.UUID) (*http.Response, error) {
	resp, err := a.bulk.DownloadAsset(ctx, id, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		resp.Body.Close()
		return nil, statusErr("downloadAsset", resp.StatusCode, b)
	}
	return resp, nil
}
