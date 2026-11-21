package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Casting to a TV (#269). The TV's side is public (no sign-in; its only secret is the id it was given); the person's side is signed in,
// in the Settings card.

func (w *Server) castOn() bool { return w.Hub != nil && w.Hub.SelfService() }

// tvNoStore is what every TV answer is: not kept, not indexed.
func tvNoStore(rw http.ResponseWriter) {
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	rw.Header().Set("X-Robots-Tag", "noindex")
}

func (w *Server) tvPage(legacy bool) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if !w.castOn() {
			http.NotFound(rw, r)
			return
		}
		tvNoStore(rw)
		TVPage(legacy).Render(r.Context(), rw)
	}
}

// tvNew gives a TV its secret and a code to show.
func (w *Server) tvNew(rw http.ResponseWriter, r *http.Request) {
	if !w.castOn() {
		http.NotFound(rw, r)
		return
	}
	secret, code, expires, err := w.Hub.NewTV(w.clientAddr(r))
	if err != nil {
		tvNoStore(rw)
		status := http.StatusServiceUnavailable
		if errors.Is(err, hub.ErrCastAddress) {
			status = http.StatusTooManyRequests
		}
		http.Error(rw, "try again later", status)
		return
	}
	tvNoStore(rw)
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(map[string]any{"s": secret, "code": code, "expires": expires.Unix()})
}

func (w *Server) tvState(rw http.ResponseWriter, r *http.Request) {
	if !w.castOn() {
		http.NotFound(rw, r)
		return
	}
	st := w.Hub.CastState(r.Context(), r.URL.Query().Get("s"))
	tvNoStore(rw)
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(map[string]any{"state": st.State, "paused": st.Paused, "seconds": st.Seconds, "fx": st.FX, "seq": st.Seq, "cmd": st.Cmd, "refresh": st.Refresh})
}

func (w *Server) tvFeed(rw http.ResponseWriter, r *http.Request) {
	if !w.castOn() {
		http.NotFound(rw, r)
		return
	}
	items, err := w.Hub.CastFeed(r.Context(), r.URL.Query().Get("s"))
	if err != nil {
		tvNoStore(rw)
		if errors.Is(err, hub.ErrDropGone) {
			http.NotFound(rw, r)
		} else {
			http.Error(rw, "try again", http.StatusServiceUnavailable)
		}
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
	tvNoStore(rw)
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(out)
}

func (w *Server) tvPhoto(rw http.ResponseWriter, r *http.Request) {
	if !w.castOn() {
		http.NotFound(rw, r)
		return
	}
	data, kind, err := w.Hub.CastImage(r.Context(), r.URL.Query().Get("s"), r.URL.Query().Get("id"))
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
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.Write(data)
}

// --- the person's side ---

func (w *Server) castViews(ctx context.Context, me *member) CastView {
	if !w.castOn() {
		return CastView{}
	}
	v := CastView{Available: true}
	list, err := w.Hub.CastsOf(ctx, me.user.ID)
	if err != nil {
		return v
	}
	l := loc(ctx)
	for _, s := range list {
		v.Sessions = append(v.Sessions, CastSessionView{ID: s.ID, Source: castSourceWords(ctx, s), Since: l.Stamp(s.PairedAt.Local()), Paused: s.Paused})
	}
	return v
}

func castSourceWords(ctx context.Context, s store.CastSession) string {
	switch s.Kind {
	case store.CastAlbum:
		return t(ctx, "The album {name}", "name", s.SourceName)
	case store.CastPerson:
		return t(ctx, "Photos of {name}", "name", s.SourceName)
	case store.CastMemory:
		return t(ctx, "The memories of {day}", "day", s.Day)
	}
	return t(ctx, "Everything new in the last 30 days")
}

func (w *Server) castForm(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	if !w.castOn() {
		http.NotFound(rw, r)
		return
	}
	ctx := r.Context()
	v := CastFormView{Session: r.URL.Query().Get("tv"), Today: time.Now().Format("2006-01-02")}
	own, err := me.sess.OwnedAlbums(ctx)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}
	for _, a := range own {
		v.Albums = append(v.Albums, AlbumOption{ID: a.ID.String(), Label: albumLabel(ctx, a.Name, a.Count)})
	}
	if shared, err := me.sess.AlbumsSharedWithMe(ctx); err == nil {
		for _, a := range shared {
			v.Shared = append(v.Shared, AlbumOption{ID: a.ID.String(), Label: albumLabel(ctx, a.Name, a.Count)})
		}
	}
	if people, err := me.sess.People(ctx); err == nil {
		for _, p := range people {
			v.People = append(v.People, PersonOption{ID: p.ID.String(), Name: p.Name})
		}
	}
	rw.Header().Set("Cache-Control", "no-store")
	CastForm(v).Render(ctx, rw)
}

// castSourceOf reads what the form chose.
func castSourceOfForm(r *http.Request) hub.CastSource {
	src := hub.CastSource{Kind: r.PostFormValue("kind"), Day: r.PostFormValue("day"), FX: r.PostFormValue("fx")}
	src.Seconds, _ = strconv.Atoi(r.PostFormValue("seconds"))
	switch src.Kind {
	case "album":
		src.ID, _ = uuid.Parse(r.PostFormValue("album"))
	case "shared":
		src.Kind = store.CastAlbum
		src.ID, _ = uuid.Parse(r.PostFormValue("shared"))
	case "person":
		src.ID, _ = uuid.Parse(r.PostFormValue("person"))
	}
	return src
}

func (w *Server) claimCast(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "cast started", func(ctx context.Context, me *member, _ string) error {
		_, err := w.Hub.ClaimCast(ctx, me.sess, me.user, r.PostFormValue("code"), castSourceOfForm(r))
		return err
	})
}

func (w *Server) changeCast(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "cast changed", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.ChangeCast(ctx, me.sess, me.user, r.PostFormValue("tv"), castSourceOfForm(r))
	})
}

func (w *Server) controlCast(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "cast controlled", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.CastControl(ctx, me.sess, me.user, r.PostFormValue("tv"), r.PostFormValue("do"))
	})
}
