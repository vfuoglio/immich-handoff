package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// A client room is a link where a photographer's client looks at an album through marked pictures. This file is the
// photographer's side (making, pausing and ending a room) and the client's page.

// roomViews are the person's client rooms for the Services tab.
func (w *Server) roomViews(ctx context.Context, me *member) []RoomView {
	rooms, err := w.Hub.Rooms(ctx, me.user.ID)
	if err != nil {
		w.Log.Warn("could not list client rooms", "err", err)
		return nil
	}
	l := loc(ctx)
	var out []RoomView
	for _, r := range rooms {
		v := RoomView{ID: r.ID, Name: r.Name, Title: r.Title, Album: r.AlbumName, Token: r.Token, PIN: r.Password != "",
			Mark: markSummary(r), Ended: r.State == store.RoomEnded || (!r.EndsAt.IsZero() && !r.EndsAt.After(time.Now())), Paused: r.State == store.RoomPaused}
		if !r.EndsAt.IsZero() {
			v.Ends = l.Stamp(r.EndsAt.Local())
		}
		out = append(out, v)
	}
	return out
}

// roomAlbums lists the person's own albums for the form that makes a room; it is asked for when the form is opened.
func (w *Server) roomAlbums(rw http.ResponseWriter, r *http.Request) {
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
	RoomAlbumSelect(opts).Render(r.Context(), rw)
}

func (w *Server) createRoom(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "room made", func(ctx context.Context, me *member, _ string) error {
		album, err := uuid.Parse(r.PostFormValue("album"))
		if err != nil {
			return hub.ErrNoAlbum
		}
		days, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("ends")))
		var logo []byte
		if f, _, err := r.FormFile("logo"); err == nil {
			defer f.Close()
			if logo, err = io.ReadAll(io.LimitReader(f, 300<<10+1)); err != nil {
				return hub.ErrRoomLogo
			}
		}
		_, err = w.Hub.CreateRoom(ctx, me.sess, me.user, hub.RoomParams{Name: r.PostFormValue("name"), Title: r.PostFormValue("title"),
			AlbumID: album, PIN: strings.TrimSpace(r.PostFormValue("pin")), EndsInDays: days,
			Mark: r.PostFormValue("mark"), MarkText: r.PostFormValue("marktext"), Logo: logo})
		return err
	})
}

func (w *Server) pauseRoom(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "room paused", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.PauseRoom(ctx, me.user, r.PostFormValue("room"), r.PostFormValue("paused") == "1")
	})
}

func (w *Server) endRoom(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "room ended", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.EndRoom(ctx, me.sess, me.user, r.PostFormValue("room"))
	})
}

// roomFor finds the room a link opens; unknown, ended and paused rooms look the same.
func (w *Server) roomFor(rw http.ResponseWriter, r *http.Request) (store.Room, bool) {
	if w.Hub == nil {
		http.NotFound(rw, r)
		return store.Room{}, false
	}
	room, err := w.Hub.RoomFor(r.Context(), r.PathValue("token"))
	if err != nil || room.State != store.RoomActive {
		http.NotFound(rw, r)
		return room, false
	}
	return room, true
}

// roomPage is the client's page. It is public (the long link, and the PIN if there is one, is the only protection), is never
// indexed or cached, and sends no referrer.
func (w *Server) roomPage(rw http.ResponseWriter, r *http.Request) {
	room, ok := w.roomFor(rw, r)
	if !ok {
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	rw.Header().Set("X-Robots-Tag", "noindex")
	photos, err := w.Hub.RoomPhotos(r.Context(), room)
	if err != nil {
		http.Error(rw, "try again", http.StatusServiceUnavailable)
		return
	}
	ids := make([]string, 0, len(photos))
	for _, p := range photos {
		ids = append(ids, p.ID)
	}
	RoomStub(room.ClientTitle(), room.Token, ids).Render(r.Context(), rw)
}

// roomPhoto gives a client the picture of one photo: marked, never the plain preview.
func (w *Server) roomPhoto(rw http.ResponseWriter, r *http.Request) {
	room, ok := w.roomFor(rw, r)
	if !ok {
		return
	}
	data, kind, err := w.Hub.RoomImage(r.Context(), room, r.PathValue("id"))
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
	rw.Header().Set("X-Robots-Tag", "noindex")
	rw.Write(data)
}

// markSummary says how a room's pictures are marked, for the photographer.
func markSummary(r store.Room) string {
	switch r.Mark {
	case "logo":
		return "logo"
	case "both":
		return "both:" + r.MarkText
	}
	return "text:" + r.MarkText
}
