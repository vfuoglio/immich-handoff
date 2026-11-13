package hub

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// ErrZipBusy is returned when a ZIP of this box is already being made.
var ErrZipBusy = errZip("a ZIP of this drop box is already being made; try again when it has finished")

type errZip string

func (e errZip) Error() string { return string(e) }

// zipOpener opens the original of one photo of a box; the caller closes the body. A photo that is gone gives a not-found.
type zipOpener func(ctx context.Context, asset uuid.UUID) (*http.Response, error)

// writeZip writes the originals of the given photos into a ZIP on w, one after another so nothing is held in memory or
// on disk. The files are stored, not compressed (photos and videos already are). It returns how many went in; a photo
// that is no longer in the library is left out.
func writeZip(ctx context.Context, w io.Writer, ids []string, open zipOpener) (int, error) {
	zw := zip.NewWriter(w)
	used := map[string]bool{}
	n := 0
	for _, raw := range ids {
		asset, err := uuid.Parse(raw)
		if err != nil {
			continue
		}
		resp, err := open(ctx, asset)
		if err != nil {
			if immich.IsNotFound(err) {
				continue
			}
			return n, err
		}
		name := zipName(resp.Header.Get("Content-Disposition"), raw, resp.Header.Get("Content-Type"), used)
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: time.Now()})
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

// ZipPhotos writes every photo the box kept, as originals, into a ZIP on w. It is done with the owner's own session,
// which can read their own library, so the box's key needs no new permission; only the owner may. One ZIP of a box at a
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
	return writeZip(ctx, w, ids, func(ctx context.Context, asset uuid.UUID) (*http.Response, error) { return sess.Original(ctx, asset) })
}

// guestZipsPerBox is how many ZIPs of one box guests may be having made at once.
const guestZipsPerBox = 3

// GalleryZip writes every photo the gallery shows, as originals, into a ZIP on w, for a guest, if the box lets guests
// download. who tells the guest apart (a guest has one ZIP at a time) and a box has at most three being made at once,
// so a crowd cannot make the app carry every original of an event at the same moment.
func (h *Hub) GalleryZip(ctx context.Context, b store.DropBox, who string, w io.Writer) (int, error) {
	if !b.GalleryDownload {
		return 0, ErrDropGone
	}
	if _, busy := h.zipping.LoadOrStore(b.ID+"|"+who, true); busy {
		return 0, ErrZipBusy
	}
	defer h.zipping.Delete(b.ID + "|" + who)
	h.zipMu.Lock()
	if h.zipCount[b.ID] >= guestZipsPerBox {
		h.zipMu.Unlock()
		return 0, ErrZipBusy
	}
	if h.zipCount == nil {
		h.zipCount = map[string]int{}
	}
	h.zipCount[b.ID]++
	h.zipMu.Unlock()
	defer func() {
		h.zipMu.Lock()
		h.zipCount[b.ID]--
		h.zipMu.Unlock()
	}()
	full, err := h.Store.DropBox(ctx, b.ID)
	if err != nil {
		return 0, ErrDropGone
	}
	api, err := h.Connect(full.Key)
	if err != nil {
		return 0, err
	}
	ids, err := h.Store.DropVisibleAssetIDs(ctx, b.ID) // what the host hid is not given
	if err != nil {
		return 0, err
	}
	return writeZip(ctx, w, ids, func(ctx context.Context, asset uuid.UUID) (*http.Response, error) {
		select { // one original at a time of those the whole app carries
		case h.originals() <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		resp, err := api.Original(ctx, asset)
		if err != nil {
			<-h.originals()
			return nil, err
		}
		resp.Body = original{resp.Body, func() { <-h.originals() }}
		return resp, nil
	})
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
