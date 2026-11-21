package web

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/albumtree"
	"github.com/vfuoglio/immich-handoff/internal/hub"
)

// The album tree page (#274): the person's albums as folders by the slashes in their names, with covers, read with their own session.
// It needs no store, so it works however the app is set up.

func (w *Server) albumHub() *hub.Hub {
	if w.Hub != nil {
		return w.Hub
	}
	return &hub.Hub{}
}

func (w *Server) albumTreePage(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	tree, err := w.albumHub().AlbumTreeOf(r.Context(), me.sess)
	if err != nil {
		w.Log.Warn("could not list albums for the tree", "user", me.user.Email, "err", err)
		http.Error(rw, t(r.Context(), "Could not read your albums from Immich."), http.StatusBadGateway)
		return
	}
	d := AlbumTreePageView{User: me.user.Name, Mine: tree.Mine, Shared: tree.Shared, Folders: tree.Mine.Folders(), ImmichURL: w.PublicImmichURL}
	rw.Header().Set("Cache-Control", "no-store")
	AlbumTreePage(d).Render(r.Context(), rw)
}

// albumCover shows the cover of an album the person owns or was given, found by the album's id in their own list.
func (w *Server) albumCover(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	album, err := uuid.Parse(r.PathValue("album"))
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	cover, ok, err := w.albumHub().AlbumCover(r.Context(), me.sess, album)
	if err != nil || !ok {
		http.NotFound(rw, r)
		return
	}
	data, ct, err := me.sess.Thumbnail(r.Context(), cover, false)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	if !strings.HasPrefix(ct, "image/") {
		ct = "image/jpeg"
	}
	rw.Header().Set("Content-Type", ct)
	rw.Header().Set("Cache-Control", "private, max-age=3600")
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.Write(data)
}

// ownAlbum finds one of the person's own albums by its id.
func (w *Server) ownAlbum(r *http.Request, me *member, id string) (name string, folders []string, ok bool) {
	album, err := uuid.Parse(id)
	if err != nil {
		return "", nil, false
	}
	own, err := me.sess.OwnedAlbums(r.Context())
	if err != nil {
		return "", nil, false
	}
	var entries []albumtree.Entry
	for _, a := range own {
		entries = append(entries, albumtree.Entry{ID: uuid.UUID(a.ID).String(), Name: a.Name})
		if uuid.UUID(a.ID) == album {
			name, ok = a.Name, true
		}
	}
	return name, albumtree.Build(entries).Folders(), ok
}

// currentFolder is the folders an album's name already puts it in, written as the form takes them.
func currentFolder(name string) string {
	f, _ := albumtree.Split(name)
	return strings.Join(f, " / ")
}

func (w *Server) albumMoveForm(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	name, folders, ok := w.ownAlbum(r, me, r.URL.Query().Get("album"))
	if !ok {
		http.NotFound(rw, r)
		return
	}
	folder := currentFolder(name)
	newName, _ := albumtree.Rename(name, folder)
	rw.Header().Set("Cache-Control", "no-store")
	AlbumMoveForm(r.URL.Query().Get("album"), name, newName, folder, folders).Render(r.Context(), rw)
}

func (w *Server) albumMovePreview(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	name, _, ok := w.ownAlbum(r, me, r.URL.Query().Get("album"))
	if !ok {
		http.NotFound(rw, r)
		return
	}
	newName, _ := albumtree.Rename(name, r.URL.Query().Get("folder"))
	rw.Header().Set("Cache-Control", "no-store")
	AlbumMovePreview(name, newName).Render(r.Context(), rw)
}

// moveAlbum renames one of the person's own albums into a folder, as the preview showed.
func (w *Server) moveAlbum(rw http.ResponseWriter, r *http.Request) {
	me := w.guarded(rw, r)
	if me == nil {
		return
	}
	ctx := r.Context()
	album, err := uuid.Parse(r.PostFormValue("album"))
	if err == nil {
		_, err = w.albumHub().MoveAlbum(ctx, me.sess, me.user, album, r.PostFormValue("folder"))
	} else {
		err = hub.ErrNoAlbum
	}
	if err != nil {
		status := http.StatusInternalServerError
		if hub.IsUserError(err) {
			status = http.StatusConflict
		}
		w.Log.Warn("album move failed", "user", me.user.Email, "err", err)
		rw.Header().Set("HX-Reswap", "innerHTML")
		rw.WriteHeader(status)
		rw.Write([]byte(t(ctx, "Could not do that: {reason}", "reason", t(ctx, err.Error()))))
		return
	}
	rw.Header().Set("HX-Refresh", "true")
	rw.WriteHeader(http.StatusNoContent)
}
