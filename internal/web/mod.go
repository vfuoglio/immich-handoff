package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// The moderator's page (/mod/<token>): a helper with no account looks at what waits in a box and approves or rejects
// it. See hub.SetDropModerator. It is plain forms, so it works on any phone with or without a script.

func (w *Server) modBox(rw http.ResponseWriter, r *http.Request) (store.DropBox, bool) {
	if w.Hub == nil {
		http.NotFound(rw, r)
		return store.DropBox{}, false
	}
	b, err := w.Hub.ModBox(r.Context(), r.PathValue("token"))
	if err != nil {
		http.NotFound(rw, r)
		return b, false
	}
	return b, true
}

func (w *Server) modPage(rw http.ResponseWriter, r *http.Request) {
	b, ok := w.modBox(rw, r)
	if !ok {
		return
	}
	ctx := r.Context()
	list, total, err := w.Hub.ModPending(ctx, b, 48)
	if err != nil {
		http.Error(rw, "try again", http.StatusServiceUnavailable)
		return
	}
	view := ModPageView{Token: r.PathValue("token"), Name: b.Name, Total: total}
	for _, p := range list {
		view.Items = append(view.Items, PendingView{ID: p.ID, Name: p.Name, Size: megabytes(p.Size), Image: p.Kind == "image"})
	}
	q := r.URL.Query()
	done, _ := strconv.Atoi(q.Get("done"))
	failed, _ := strconv.Atoi(q.Get("failed"))
	switch {
	case q.Get("nothing") == "1":
		view.Message = t(ctx, "Choose at least one file first.")
	case failed > 0:
		view.Message = t(ctx, "Some files could not be stored and are still waiting. Try again in a moment.")
	case done > 0 && q.Get("did") == "rejected":
		view.Message = n(ctx, "{n} file rejected.", "{n} files rejected.", done)
	case done > 0:
		view.Message = n(ctx, "{n} file approved.", "{n} files approved.", done)
	}
	// the photos the box kept, to pin or hide, when the key may look at pictures (a gallery or a wall)
	if b.Gallery || b.Wall != "" {
		view.Looks = true
		before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
		photos, err := w.Hub.ModPhotos(ctx, b, before, 36)
		if err == nil {
			for _, p := range photos {
				by := ""
				if p.Name != "" {
					by = t(ctx, "by {name}", "name", p.Name)
				}
				view.Photos = append(view.Photos, ModPhotoView{ID: strconv.FormatInt(p.ID, 10), By: by, Hearts: p.Hearts, Hidden: p.Hidden, Pinned: p.Pinned})
			}
			if len(photos) == 36 {
				view.More = strconv.FormatInt(photos[len(photos)-1].ID, 10)
			}
		}
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	ModPage(view).Render(ctx, rw)
}

// modImage gives the moderator the picture of a photo the box kept.
func (w *Server) modImage(rw http.ResponseWriter, r *http.Request) {
	b, ok := w.modBox(rw, r)
	if !ok || !(b.Gallery || b.Wall != "") {
		if ok {
			http.NotFound(rw, r)
		}
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	data, kind, err := w.Hub.ModImage(r.Context(), b, id)
	if err != nil {
		if errors.Is(err, hub.ErrDropGone) {
			http.NotFound(rw, r)
		} else {
			http.Error(rw, "try again", http.StatusBadGateway)
		}
		return
	}
	rw.Header().Set("Content-Type", kind)
	rw.Header().Set("Cache-Control", "private, max-age=600")
	rw.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	rw.Write(data)
}

// modMark pins or hides a photo, or takes that back, and goes back to the page.
func (w *Server) modMark(rw http.ResponseWriter, r *http.Request) {
	b, ok := w.modBox(rw, r)
	if !ok {
		return
	}
	r.ParseForm()
	id, err := strconv.ParseInt(r.PostFormValue("item"), 10, 64)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	switch err := w.Hub.ModMark(r.Context(), b, id, r.PostFormValue("do")); {
	case errors.Is(err, hub.ErrDropGone), errors.Is(err, hub.ErrBadMark):
		http.NotFound(rw, r)
		return
	case err != nil:
		http.Error(rw, "try again", http.StatusServiceUnavailable)
		return
	}
	loc := "../" + r.PathValue("token")
	if before := r.PostFormValue("before"); before != "" {
		loc += "?before=" + url.QueryEscape(before)
	}
	rw.Header().Set("Location", loc+"#photo-"+strconv.FormatInt(id, 10))
	rw.WriteHeader(http.StatusSeeOther)
}

// lovedDropBox puts the photos guests loved and the host pinned in an album of the owner's.
func (w *Server) lovedDropBox(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "loved album", func(ctx context.Context, me *member, id string) error {
		_, _, err := w.Hub.MakeLovedAlbum(ctx, me.sess, me.user, id)
		return err
	})
}

func (w *Server) modFile(rw http.ResponseWriter, r *http.Request) {
	b, ok := w.modBox(rw, r)
	if !ok {
		return
	}
	p, err := w.Hub.ModPendingFile(r.Context(), b, r.PathValue("id"))
	if err != nil || p.Kind != "image" {
		http.NotFound(rw, r)
		return
	}
	f, err := os.Open(p.Path)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := f.Read(head)
	f.Seek(0, io.SeekStart)
	rw.Header().Set("Content-Type", http.DetectContentType(head[:n]))
	rw.Header().Set("Cache-Control", "private, no-store")
	rw.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	io.Copy(rw, f)
}

// modDecide is a moderator's approval or rejection; it answers with a redirect back to the page, which says how it went.
func (w *Server) modDecide(approve bool) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		b, ok := w.modBox(rw, r)
		if !ok {
			return
		}
		r.ParseForm()
		only := r.PostForm["item"]
		if r.PostFormValue("all") == "1" {
			only = nil
		} else if len(only) == 0 {
			http.Redirect(rw, r, "../"+r.PathValue("token")+"?nothing=1", http.StatusSeeOther)
			return
		}
		done, failed, err := w.Hub.ModDecide(r.Context(), b, only, approve)
		if err != nil && !errors.Is(err, hub.ErrDropGone) {
			http.Error(rw, "try again", http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			http.NotFound(rw, r)
			return
		}
		did := "approved"
		if !approve {
			did = "rejected"
		}
		rw.Header().Set("Location", "../"+r.PathValue("token")+"?"+url.Values{"done": {fmt.Sprint(done)}, "failed": {fmt.Sprint(failed)}, "did": {did}}.Encode())
		rw.WriteHeader(http.StatusSeeOther)
	}
}

// moderatorDropBox makes, renews or ends the box's moderator link, for the owner.
func (w *Server) moderatorDropBox(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "moderator link", func(ctx context.Context, me *member, id string) error {
		_, err := w.Hub.SetDropModerator(ctx, me.user, id, r.PostFormValue("on") == "1")
		return err
	})
}
