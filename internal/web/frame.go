package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// A frame is a link that shows one of a person's albums full screen on a device with no account: a photo frame, an old
// tablet, a TV. The page holds no session; it asks for the list of photos now and then and fetches each picture through
// the app, which asks Immich with a key that can only look at pictures.

func (w *Server) frameFor(rw http.ResponseWriter, r *http.Request) (store.Frame, bool) {
	if w.Hub == nil {
		http.NotFound(rw, r)
		return store.Frame{}, false
	}
	f, err := w.Hub.FrameFor(r.Context(), r.PathValue("token"))
	if err != nil {
		http.NotFound(rw, r) // unknown, ended and past its end date look the same
		return f, false
	}
	return f, true
}

// framePage is the full-screen page. It is public (the long link is the only protection), is never indexed or cached, and
// sends no referrer.
func (w *Server) framePage(rw http.ResponseWriter, r *http.Request) {
	f, ok := w.frameFor(rw, r)
	if !ok {
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	FramePage(f.Name, r.PathValue("token"), f.Hold).Render(r.Context(), rw)
}

// frameFeed lists the photos now in the album, newest first, by opaque id, and says when to ask again.
func (w *Server) frameFeed(rw http.ResponseWriter, r *http.Request) {
	f, ok := w.frameFor(rw, r)
	if !ok {
		return
	}
	items, err := w.Hub.FrameFeed(r.Context(), f)
	if err != nil {
		http.Error(rw, "try again", http.StatusServiceUnavailable)
		return
	}
	type item struct {
		ID string `json:"id"`
	}
	out := struct {
		Items   []item `json:"items"`
		Refresh int    `json:"refresh"`
	}{Items: []item{}, Refresh: int(w.Hub.FrameRefreshEvery().Seconds())}
	for _, i := range items {
		out.Items = append(out.Items, item{i.ID})
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(rw).Encode(out)
}

// framePhoto gives one picture to the frame: a photo that is in the album now, by its opaque id.
func (w *Server) framePhoto(rw http.ResponseWriter, r *http.Request) {
	f, ok := w.frameFor(rw, r)
	if !ok {
		return
	}
	data, kind, err := w.Hub.FrameImage(r.Context(), f, r.PathValue("id"))
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

// --- the owner's side ---

// frameViews are the person's frames for the Services tab.
func (w *Server) frameViews(ctx context.Context, me *member) []FrameView {
	frames, err := w.Hub.Frames(ctx, me.user.ID)
	if err != nil {
		w.Log.Warn("could not list frames", "err", err)
		return nil
	}
	l := loc(ctx)
	var out []FrameView
	for _, f := range frames {
		v := FrameView{ID: f.ID, Name: f.Name, Album: f.AlbumName, Token: f.Token, Seconds: f.Hold, Ended: f.State == store.FrameEnded || (!f.EndsAt.IsZero() && !f.EndsAt.After(time.Now()))}
		if !f.EndsAt.IsZero() {
			v.Ends = l.Stamp(f.EndsAt.Local())
		}
		out = append(out, v)
	}
	return out
}

// frameAlbums lists the person's own albums for the form that makes a frame; it is asked for when the form is opened.
func (w *Server) frameAlbums(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	albums, err := me.sess.OwnedAlbums(r.Context())
	if err != nil {
		w.Log.Warn("could not list albums", "user", me.user.Email, "err", err)
		http.Error(rw, "could not list your albums: "+err.Error(), http.StatusBadGateway)
		return
	}
	opts := make([]AlbumOption, 0, len(albums))
	for _, a := range albums {
		opts = append(opts, AlbumOption{ID: a.ID.String(), Label: albumLabel(r.Context(), a.Name, a.Count)})
	}
	rw.Header().Set("Cache-Control", "no-store")
	FrameAlbumSelect(opts).Render(r.Context(), rw)
}

func (w *Server) createFrame(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "frame made", func(ctx context.Context, me *member, _ string) error {
		album, err := uuid.Parse(r.PostFormValue("album"))
		if err != nil {
			return hub.ErrNoAlbum
		}
		days, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("ends")))
		hold, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("hold")))
		_, err = w.Hub.CreateFrame(ctx, me.sess, me.user, hub.FrameParams{Name: r.PostFormValue("name"), AlbumID: album, EndsInDays: days, HoldSeconds: hold})
		return err
	})
}

func (w *Server) endFrame(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "frame ended", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.EndFrame(ctx, me.sess, me.user, r.PostFormValue("frame"))
	})
}
