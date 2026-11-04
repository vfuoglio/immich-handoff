// Package web serves the webhook receiver and the web page.
// The page uses Immich's own sign-in: the browser's cookie is forwarded to /api/users/me. Any Immich
// user can open it; only people who take part in a service see its activity.
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

//go:embed static
var staticFS embed.FS

type Server struct {
	S             *syncer.Syncer
	Hub           *hub.Hub // nil: people cannot join or leave from the page
	BaseURL       string
	WebhookHeader string
	WebhookSecret string
	Log           *slog.Logger
	BackgroundCtx context.Context
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
	mux.HandleFunc("POST /join", w.join)
	mux.HandleFunc("POST /leave", w.leave)
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

// member is the signed-in Immich user, with a client that acts with their own session.
type member struct {
	user immich.User
	sess *immich.API
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
	return &member{user: u, sess: a}, 0, ""
}

// takesPart is the account the user holds in the service in this role, if any.
func (m *member) account(svc *syncer.Service, role string) *syncer.Account {
	group := svc.Senders
	if role == "receiver" {
		group = svc.Receivers
	}
	for _, a := range group {
		if a.UserID == m.user.ID {
			return a
		}
	}
	return nil
}

func (w *Server) servicesFor(ctx context.Context, m *member) []ServiceView {
	joined := map[string]bool{}
	if w.Hub != nil {
		joined, _ = w.Hub.Joined(ctx, m.user.ID)
	}
	var out []ServiceView
	for _, svc := range w.S.Current() {
		if m.account(svc, "sender") != nil {
			out = append(out, ServiceView{svc.Label(), "sender", svc.Tag, len(svc.Senders), len(svc.Receivers), joined[svc.Label()]})
		}
		if m.account(svc, "receiver") != nil {
			out = append(out, ServiceView{svc.Label(), "receiver", svc.InboxTag, len(svc.Senders), len(svc.Receivers), false})
		}
	}
	return out
}

func (w *Server) joinable(m *member) []JoinView {
	if w.Hub == nil {
		return nil
	}
	var out []JoinView
	for _, svc := range w.Hub.Joinable(m.user.ID) {
		out = append(out, JoinView{svc.Label(), svc.Tag})
	}
	return out
}

func (w *Server) dashboard(rw http.ResponseWriter, r *http.Request) {
	me, code, _ := w.authenticate(r)
	if me == nil {
		rw.WriteHeader(code)
		Notice("Sign in to Immich first",
			"This page uses your Immich login. Sign in to Immich in this browser, then reload this page.", false).Render(r.Context(), rw)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	Dashboard(me.user.Name, w.servicesFor(r.Context(), me), w.joinable(me), w.S.Last()).Render(r.Context(), rw)
}

// requireMember is for fragments and streams: sign-in plus taking part in at least one service.
func (w *Server) requireMember(rw http.ResponseWriter, r *http.Request) *member {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return nil
	}
	if len(w.servicesFor(r.Context(), me)) == 0 {
		http.Error(rw, "you do not take part in any service", http.StatusForbidden)
		return nil
	}
	return me
}

// countFragment is how many photos wait (sender) or sit in the inbox (receiver) for one service.
func (w *Server) countFragment(rw http.ResponseWriter, r *http.Request) {
	me := w.requireMember(rw, r)
	if me == nil {
		return
	}
	name, role := r.URL.Query().Get("service"), r.URL.Query().Get("role")
	for _, svc := range w.S.Current() {
		if svc.Label() != name {
			continue
		}
		acct := me.account(svc, role)
		if acct == nil {
			break
		}
		tag := svc.Tag
		if role == "receiver" {
			tag = svc.InboxTag
		}
		rw.Header().Set("Cache-Control", "no-store")
		Count(w.count(r.Context(), acct, tag), role).Render(r.Context(), rw)
		return
	}
	http.Error(rw, "no such service for you", http.StatusNotFound)
}

func (w *Server) lastRunFragment(rw http.ResponseWriter, r *http.Request) {
	if w.requireMember(rw, r) == nil {
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	LastRun(w.S.Last()).Render(r.Context(), rw)
}

// events tells the open page when a pass has finished, so it can refresh its cards.
func (w *Server) events(rw http.ResponseWriter, r *http.Request) {
	if w.requireMember(rw, r) == nil {
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

// guarded runs an action that changes something: it needs the cross-site header and a sign-in.
func (w *Server) guarded(rw http.ResponseWriter, r *http.Request) *member {
	// Cookie auth is ambient, so require a header a cross-site form cannot send (CSRF guard).
	if r.Header.Get("X-Requested-With") != "immich-handoff" {
		http.Error(rw, "missing X-Requested-With", http.StatusBadRequest)
		return nil
	}
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return nil
	}
	return me
}

func (w *Server) syncNow(rw http.ResponseWriter, r *http.Request) {
	me := w.guarded(rw, r)
	if me == nil {
		return
	}
	if len(w.servicesFor(r.Context(), me)) == 0 {
		http.Error(rw, "you do not take part in any service", http.StatusForbidden)
		return
	}
	go w.S.Run(w.BackgroundCtx, "manual: "+me.user.Email, false)
	rw.WriteHeader(http.StatusAccepted)
	rw.Write([]byte("Sync started"))
}

func (w *Server) join(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "joined", func(ctx context.Context, me *member, service string) error {
		return w.Hub.Join(ctx, me.sess, me.user, service)
	})
}

func (w *Server) leave(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "left", func(ctx context.Context, me *member, service string) error {
		return w.Hub.Leave(ctx, me.sess, me.user, service)
	})
}

// membership runs a join or leave and answers htmx: a refresh on success, a short message on failure.
func (w *Server) membership(rw http.ResponseWriter, r *http.Request, done string, act func(context.Context, *member, string) error) {
	me := w.guarded(rw, r)
	if me == nil {
		return
	}
	if w.Hub == nil || !w.Hub.SelfService() {
		http.Error(rw, "joining from this page is not switched on", http.StatusNotFound)
		return
	}
	service := r.PostFormValue("service")
	if err := act(r.Context(), me, service); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, hub.ErrNotJoinable) || errors.Is(err, hub.ErrAlready) || errors.Is(err, hub.ErrNotJoined) {
			status = http.StatusConflict
		}
		w.Log.Warn("membership change failed", "action", done, "user", me.user.Email, "service", service, "err", err)
		rw.Header().Set("HX-Reswap", "innerHTML")
		rw.WriteHeader(status)
		fmt.Fprint(rw, "Could not do that: ", err.Error())
		return
	}
	rw.Header().Set("HX-Refresh", "true")
	rw.WriteHeader(http.StatusNoContent)
}
