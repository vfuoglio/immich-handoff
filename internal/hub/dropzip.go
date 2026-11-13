package hub

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"mime"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
)

// ErrZipBusy is returned when a ZIP of this box is already being made.
var ErrZipBusy = errZip("a ZIP of this drop box is already being made; try again when it has finished")

type errZip string

func (e errZip) Error() string { return string(e) }

// ZipPhotos writes every photo the box kept, as originals, into a ZIP on w, one after another so nothing is held in
// memory or on disk. It is done with the owner's own session, which can read their own library, so the box's key needs no
// new permission; only the owner may. The files are stored, not compressed (photos and videos already are). It returns how
// many photos went in; a photo that is no longer in the library (the owner deleted it) is left out. One ZIP of a box at a
// time.
func (h *Hub) ZipPhotos(ctx context.Context, sess *immich.API, owner immich.User, id string, w io.Writer) (int, error) {
	b, _, err := h.dropBoxFor(ctx, owner.ID, id, "owner")
	if err != nil {
		return 0, err
	}
	if _, busy := h.zipping.LoadOrStore(b.ID, true); busy {
		return 0, ErrZipBusy
	}
	defer h.zipping.Delete(b.ID)
	ids, err := h.Store.DropAssetIDs(ctx, b.ID)
	if err != nil {
		return 0, err
	}
	zw := zip.NewWriter(w)
	used := map[string]bool{}
	n := 0
	for _, raw := range ids {
		asset, err := uuid.Parse(raw)
		if err != nil {
			continue
		}
		resp, err := sess.Original(ctx, asset)
		if err != nil {
			if immich.IsNotFound(err) {
				continue
			}
			return n, err
		}
		name := zipName(resp.Header.Get("Content-Disposition"), raw, resp.Header.Get("Content-Type"), used)
		hdr := &zip.FileHeader{Name: name, Method: zip.Store, Modified: time.Now()}
		fw, err := zw.CreateHeader(hdr)
		if err == nil {
			_, err = io.Copy(fw, resp.Body)
		}
		resp.Body.Close()
		if err != nil {
			return n, err
		}
		n++
	}
	return n, zw.Close()
}

// zipName is the name a photo gets in the ZIP: the one Immich gave the original, cleaned of any path, made unique by a
// number if two photos share it, or, with no name, its id and an extension from its type.
func zipName(disposition, id, contentType string, used map[string]bool) string {
	name := ""
	if _, params, err := mime.ParseMediaType(disposition); err == nil {
		name = filepath.Base(strings.ReplaceAll(params["filename"], "\\", "/"))
	}
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == "/" {
		ext := ".bin"
		if exts, _ := mime.ExtensionsByType(strings.SplitN(contentType, ";", 2)[0]); len(exts) > 0 {
			ext = exts[0]
		}
		name = id + ext
	}
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; used[strings.ToLower(name)]; i++ {
		name = fmt.Sprintf("%s (%d)%s", base, i, ext)
	}
	used[strings.ToLower(name)] = true
	return name
}
