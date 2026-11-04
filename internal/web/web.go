// Package web serves the webhook receiver and the status page.
// Authentication for the page is Immich's own session: the browser's cookie is forwarded to
// /api/users/me and only accounts that take part in a service are let in.
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

//go:embed static
var staticFS embed.FS

type Server struct {
	S             *syncer.Syncer
	BaseURL       string
	WebhookHeader string
	WebhookSecret string
	Log           *slog.Logger
	BackgroundCtx context.Context

	// resolved at Init: which Immich user is which account
	users map[uuid.UUID]*syncer.Account
}

// Init resolves which Immich user each configured account belongs to (once, at startup).
func (w *Server) Init(ctx context.Context) error {
	w.users = map[uuid.UUID]*syncer.Account{}
	for _, svc := range w.S.Services {
		for _, group := range [][]*syncer.Account{svc.Senders, svc.Receivers} {
			for _, a := range group {
				u, err := a.API.Me(ctx)
				if err != nil {
					return err
				}
				if _, ok := w.users[u.ID]; !ok {
					w.users[u.ID] = a
				}
			}
		}
	}
	return nil
}

func (w *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheFor(http.FileServerFS(static), time.Hour)))
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) { rw.Write([]byte("ok")) })
	mux.HandleFunc("POST /webhook/immich", w.webhook)
	mux.HandleFunc("GET /{$}", w.dashboard)
	mux.HandleFunc("GET /fragments/count", w.countFragment)
	mux.HandleFunc("GET /fragments/lastrun", w.lastRunFragment)
	mux.HandleFunc("GET /events", w.events)
	mux.HandleFunc("POST /sync", w.syncNow)
	return mux
}

func cacheFor(h http.Handler, d time.Duration) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(d.Seconds())))
		h.ServeHTTP(rw, r)
	})
}

// The payload is deliberately not trusted: the webhook only wakes the importer, and the asset id it
// names is a hint that is checked against Immich before use.
func (w *Server) webhook(rw http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(w.WebhookHeader)), []byte(w.WebhookSecret)) != 1 {
		http.Error(rw, "bad webhook secret", http.StatusUnauthorized)
		return
	}
	if body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10)); err == nil {
		w.Log.Debug("webhook received", "body", string(body))
		var ev struct {
			Data struct {
				Asset struct {
					ID uuid.UUID `json:"id"`
				} `json:"asset"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &ev) == nil && ev.Data.Asset.ID != uuid.Nil {
			w.S.AddHint(ev.Data.Asset.ID)
		}
	}
	go w.S.Run(w.BackgroundCtx, "webhook", false)
	rw.WriteHeader(http.StatusAccepted)
}

type member struct {
	user immich.User
	acct *syncer.Account
}

func (w *Server) authenticate(r *http.Request) (*member, int, string) {
	cookie := r.Header.Get("Cookie")
	if cookie == "" {
		return nil, http.StatusUnauthorized, "not signed in to Immich"
	}
	a, err := immich.New(w.BaseURL, immich.Auth{Cookie: cookie}, nil)
	if err != nil {
		return nil, http.StatusInternalServerError, err.Error()
	}
	u, err := a.Me(r.Context())
	if err != nil {
		return nil, http.StatusUnauthorized, "not signed in to Immich"
	}
	acct, ok := w.users[u.ID]
	if !ok {
		return nil, http.StatusForbidden, "this Immich account does not take part in any service"
	}
	return &member{user: u, acct: acct}, 0, ""
}

// servicesFor lists the services the member takes part in (a member can both send and receive).
func (w *Server) servicesFor(me *member) []ServiceView {
	var out []ServiceView
	for _, svc := range w.S.Services {
		for _, a := range svc.Senders {
			if a == me.acct {
				out = append(out, ServiceView{svc.Label(), "sender", svc.Tag, len(svc.Senders), len(svc.Receivers)})
			}
		}
		for _, a := range svc.Receivers {
			if a == me.acct {
				out = append(out, ServiceView{svc.Label(), "receiver", svc.InboxTag, len(svc.Senders), len(svc.Receivers)})
			}
		}
	}
	return out
}

func (w *Server) dashboard(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		title := "Sign in to Immich first"
		text := "This page uses your Immich login. Sign in to Immich in this browser, then reload this page."
		if code == http.StatusForbidden {
			title, text = "Not part of any service", msg
		}
		rw.WriteHeader(code)
		Notice(title, text, code != http.StatusUnauthorized).Render(r.Context(), rw)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	Dashboard(me.user.Name, w.servicesFor(me), w.S.Last()).Render(r.Context(), rw)
}

// countFragment is how many photos wait (sender) or sit in the inbox (receiver) for one service.
func (w *Server) countFragment(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	name, role := r.URL.Query().Get("service"), r.URL.Query().Get("role")
	for _, sv := range w.servicesFor(me) {
		if sv.Name != name || sv.Role != role {
			continue
		}
		n := w.count(r.Context(), me.acct, sv.Tag)
		rw.Header().Set("Cache-Control", "no-store")
		Count(n, role).Render(r.Context(), rw)
		return
	}
	http.Error(rw, "no such service for you", http.StatusNotFound)
}

func (w *Server) lastRunFragment(rw http.ResponseWriter, r *http.Request) {
	if me, code, msg := w.authenticate(r); me == nil {
		http.Error(rw, msg, code)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	LastRun(w.S.Last()).Render(r.Context(), rw)
}

// events tells the open page when a pass has finished, so it can refresh its cards.
func (w *Server) events(rw http.ResponseWriter, r *http.Request) {
	if me, code, msg := w.authenticate(r); me == nil {
		http.Error(rw, msg, code)
		return
	}
	fl, ok := rw.(http.Flusher)
	if !ok {
		http.Error(rw, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("X-Accel-Buffering", "no") // do not let a reverse proxy hold the stream back
	ch, cancel := w.S.Subscribe()
	defer cancel()
	fmt.Fprint(rw, ": connected\n\n")
	fl.Flush()
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			fmt.Fprint(rw, "event: run\ndata: 1\n\n")
		case <-keepalive.C:
			fmt.Fprint(rw, ": keepalive\n\n")
		}
		fl.Flush()
	}
}

// count is how many photos carry the tag in the account (the app's own key does the looking).
func (w *Server) count(ctx context.Context, a *syncer.Account, tag string) int {
	id, err := a.API.FindTag(ctx, tag, false)
	if err != nil || id == nil {
		return 0
	}
	assets, err := a.API.Search(ctx, immich.MetadataSearchDto{TagIds: &[]uuid.UUID{*id}})
	if err != nil {
		return 0
	}
	return len(assets)
}

func (w *Server) syncNow(rw http.ResponseWriter, r *http.Request) {
	// Cookie auth is ambient, so require a header a cross-site form cannot send (CSRF guard).
	if r.Header.Get("X-Requested-With") != "immich-handoff" {
		http.Error(rw, "missing X-Requested-With", http.StatusBadRequest)
		return
	}
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	go w.S.Run(w.BackgroundCtx, "manual: "+me.user.Email, false)
	rw.WriteHeader(http.StatusAccepted)
	rw.Write([]byte("Sync started"))
}
