package web

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// The guests' side of an event's gallery: the photos a box kept, one at a time, as pictures; hearts; and, if the owner
// allows, the originals. Everything is asked for by a photo's place in the box's list, never by its id in Immich.

// guestSecretFor is the secret this guest's browser holds for the box, made and handed over in a cookie the first time:
// it is how a guest's hearts and uploads are found again, and only a hash of it is kept.
func (w *Server) guestSecretFor(rw http.ResponseWriter, r *http.Request, b store.DropBox) string {
	if s := guestSecret(r, b); s != "" {
		return s
	}
	s := hub.NewGuestSecret()
	http.SetCookie(rw, &http.Cookie{Name: guestSecretCookie(b.ID), Value: s, Path: "/", MaxAge: 60 * 24 * 3600, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"})
	return s
}

// galleryBox finds the box a gallery request is for. A box with no gallery, a link that has ended and a page whose
// password has not been given all answer "not found", like a link that does not exist.
func (w *Server) galleryBox(rw http.ResponseWriter, r *http.Request) (store.DropBox, bool) {
	if w.Hub == nil {
		http.NotFound(rw, r)
		return store.DropBox{}, false
	}
	b, err := w.Hub.GalleryBox(r.Context(), r.PathValue("token"))
	if err != nil || pageLocked(r, b) {
		http.NotFound(rw, r)
		return b, false
	}
	return b, true
}

// galleryFeed lists photos for a guest's page, newest first, and says what the box lets a guest do with them.
func (w *Server) galleryFeed(rw http.ResponseWriter, r *http.Request) {
	b, ok := w.galleryBox(rw, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	items, err := w.Hub.GalleryFeed(r.Context(), b, guestSecret(r, b), before, after, 0)
	if err != nil {
		http.Error(rw, "try again", http.StatusServiceUnavailable)
		return
	}
	type item struct {
		ID      int64  `json:"id"`
		Name    string `json:"name,omitempty"`
		Caption string `json:"caption,omitempty"`
		Hearts  int    `json:"hearts"`
		Mine    bool   `json:"mine,omitempty"`
	}
	out := struct {
		Items    []item `json:"items"`
		Download bool   `json:"download"`
	}{Items: []item{}, Download: b.GalleryDownload}
	for _, i := range items {
		out.Items = append(out.Items, item{i.ID, i.Name, i.Caption, i.Hearts, i.Mine})
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(rw).Encode(out)
}

// galleryPhoto gives a guest the picture of one photo of the box.
func (w *Server) galleryPhoto(rw http.ResponseWriter, r *http.Request) {
	b, ok := w.galleryBox(rw, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	data, kind, err := w.Hub.GalleryImage(r.Context(), b, id)
	if err != nil {
		if errors.Is(err, hub.ErrDropGone) {
			http.NotFound(rw, r)
		} else {
			http.Error(rw, "try again", http.StatusBadGateway)
		}
		return
	}
	rw.Header().Set("Content-Type", kind)
	rw.Header().Set("Cache-Control", "private, max-age=3600")
	rw.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	rw.Write(data)
}

// galleryOriginal lets a guest download the original of one photo, if the box allows it.
func (w *Server) galleryOriginal(rw http.ResponseWriter, r *http.Request) {
	b, ok := w.galleryBox(rw, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	resp, err := w.Hub.GalleryOriginal(r.Context(), b, id)
	if err != nil {
		switch {
		case errors.Is(err, hub.ErrDropGone):
			http.NotFound(rw, r)
		case errors.Is(err, hub.ErrDropBusy):
			rw.Header().Set("Retry-After", "10")
			http.Error(rw, "busy, try again in a moment", http.StatusServiceUnavailable)
		default:
			http.Error(rw, "try again", http.StatusBadGateway)
		}
		return
	}
	defer resp.Body.Close()
	h := rw.Header()
	h.Set("Content-Type", resp.Header.Get("Content-Type"))
	if n := resp.Header.Get("Content-Length"); n != "" {
		h.Set("Content-Length", n)
	}
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": savedName(resp.Header.Get("Content-Disposition"), id)}))
	h.Set("Cache-Control", "private, no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	io.Copy(rw, resp.Body)
}

// savedName is the name a download is saved under: the one Immich gave the original, cleaned of any path, or a plain
// one made from the photo's place in the list.
func savedName(disposition string, id int64) string {
	if _, params, err := mime.ParseMediaType(disposition); err == nil {
		if n := filepath.Base(strings.ReplaceAll(params["filename"], "\\", "/")); n != "" && n != "." && n != "/" {
			return strings.Map(func(r rune) rune {
				if r < 32 || r == 127 {
					return -1
				}
				return r
			}, n)
		}
	}
	return "photo-" + strconv.FormatInt(id, 10)
}

// galleryHeart gives a photo the guest's heart, or takes it back.
func (w *Server) galleryHeart(rw http.ResponseWriter, r *http.Request) {
	b, ok := w.galleryBox(rw, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	hearted, n, err := w.Hub.GalleryHeart(r.Context(), b, w.guestSecretFor(rw, r, b), id)
	switch {
	case errors.Is(err, hub.ErrDropGone):
		http.NotFound(rw, r)
		return
	case errors.Is(err, hub.ErrDropBusy):
		rw.Header().Set("Retry-After", "600")
		http.Error(rw, "too many hearts, slow down", http.StatusTooManyRequests)
		return
	case err != nil:
		http.Error(rw, "try again", http.StatusServiceUnavailable)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(rw).Encode(map[string]any{"hearted": hearted, "hearts": n})
}
