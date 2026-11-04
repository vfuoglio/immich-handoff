// Package web serves the webhook receiver and the status page.
// Authentication for the page is Immich's own session ("option A"): the browser's cookie is
// forwarded to /api/users/me and only the family accounts are accepted.
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
	sourceIDs     map[uuid.UUID]*immich.API
	destID        uuid.UUID
}

// Init resolves which Immich users are part of the handoff (once, at startup).
func (w *Server) Init(ctx context.Context) error {
	w.sourceIDs = map[uuid.UUID]*immich.API{}
	for _, src := range w.S.Sources {
		u, err := src.Me(ctx)
		if err != nil {
			return err
		}
		w.sourceIDs[u.ID] = src
	}
	d, err := w.S.Dest.Me(ctx)
	w.destID = d.ID
	return err
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

// The payload is deliberately ignored: the webhook only wakes the importer.
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
			w.S.AddHint(ev.Data.Asset.ID) // only a hint: verified against Immich before use
		}
	}
	go w.S.Run(w.BackgroundCtx, "webhook", false)
	rw.WriteHeader(http.StatusAccepted)
}

type who struct {
	user immich.User
	role string // "source" | "destination"
	api  *immich.API
}

func (w *Server) authenticate(r *http.Request) (*who, int, string) {
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
	if u.ID == w.destID {
		return &who{u, "destination", w.S.Dest}, 0, ""
	}
	if src, ok := w.sourceIDs[u.ID]; ok {
		return &who{u, "source", src}, 0, ""
	}
	return nil, http.StatusForbidden, "this Immich account is not part of the handoff"
}

func (w *Server) status(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		writeJSON(rw, code, map[string]string{"detail": msg})
		return
	}
	out := map[string]any{"user": me.user.Name, "role": me.role, "lastRun": w.S.Last()}
	tagName := w.S.Cfg.ShareTag
	key := "pendingShare"
	if me.role == "destination" {
		tagName, key = w.S.Cfg.InboxTag, "inbox"
	}
	n := 0
	if tag, err := me.api.FindTag(r.Context(), tagName, false); err == nil && tag != nil {
		if assets, err := me.api.Search(r.Context(), immich.MetadataSearchDto{TagIds: &[]uuid.UUID{*tag}}); err == nil {
			n = len(assets)
		}
	}
	out[key] = n
	writeJSON(rw, http.StatusOK, out)
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
