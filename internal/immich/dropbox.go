package immich

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
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
