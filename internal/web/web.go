// Package web serves the webhook receiver and the status page.
// Authentication for the page is Immich's own session: the browser's cookie is forwarded to
// /api/users/me and only accounts that take part in a service are let in.
package web

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

//go:embed index.html
var indexHTML []byte

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
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) { rw.Write([]byte("ok")) })
	mux.HandleFunc("POST /webhook/immich", w.webhook)
	mux.HandleFunc("GET /api/status", w.status)
	mux.HandleFunc("POST /api/sync", w.syncNow)
	mux.HandleFunc("GET /{$}", func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		rw.Write(indexHTML)
	})
	return mux
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

type serviceStatus struct {
	Name    string `json:"name"`
	Role    string `json:"role"` // sender | receiver
	Tag     string `json:"tag"`
	Waiting int    `json:"waiting"`
}

func (w *Server) status(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		writeJSON(rw, code, map[string]string{"detail": msg})
		return
	}
	var services []serviceStatus
	for _, svc := range w.S.Services {
		for _, a := range svc.Senders {
			if a == me.acct {
				services = append(services, serviceStatus{svc.Label(), "sender", svc.Tag, w.count(r.Context(), a, svc.Tag)})
			}
		}
		for _, a := range svc.Receivers {
			if a == me.acct {
				services = append(services, serviceStatus{svc.Label(), "receiver", svc.InboxTag, w.count(r.Context(), a, svc.InboxTag)})
			}
		}
	}
	writeJSON(rw, http.StatusOK, map[string]any{"user": me.user.Name, "services": services, "lastRun": w.S.Last()})
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
}

func writeJSON(rw http.ResponseWriter, code int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(code)
	json.NewEncoder(rw).Encode(v)
}
