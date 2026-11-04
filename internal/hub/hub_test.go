package hub_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
	"github.com/vfuoglio/immich-handoff/internal/vault"
)

const ts = "2025-01-02T03:04:05.000Z"

var (
	bobID    = uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000001")
	familyID = uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000002")
)

// fakeImmich is a small Immich: one signed-in person (bob) who can create and delete keys and workflows.
type fakeImmich struct {
	mu            sync.Mutex
	failWorkflows bool
	keys          map[string]string // secret -> key id
	deletedKeys   []string
	workflows     map[string]string // id -> name
	deletedFlows  []string
}

func (f *fakeImmich) handler() http.Handler {
	j := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/users/me", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_, keyOK := f.keys[r.Header.Get("x-api-key")]
		if r.Header.Get("Cookie") == "sess=bob" || keyOK {
			j(w, 200, map[string]any{"id": bobID, "name": "Bob", "email": "bob@x.test"})
			return
		}
		http.Error(w, "no", 401)
	})
	mux.HandleFunc("POST /api/api-keys", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body struct {
			Name        string   `json:"name"`
			Permissions []string `json:"permissions"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		id, secret := uuid.NewString(), "secret-"+uuid.NewString()
		f.keys[secret] = id
		j(w, 201, map[string]any{"id": id, "name": body.Name, "secret": secret, "permissions": body.Permissions, "createdAt": ts, "updatedAt": ts,
			"apiKey": map[string]any{"id": id, "name": body.Name, "permissions": body.Permissions, "createdAt": ts, "updatedAt": ts}})
	})
	mux.HandleFunc("DELETE /api/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.deletedKeys = append(f.deletedKeys, r.PathValue("id"))
		for s, id := range f.keys {
			if id == r.PathValue("id") {
				delete(f.keys, s)
			}
		}
		w.WriteHeader(204)
	})
	tag := map[string]any{"id": "33333333-3333-3333-3333-333333333333", "name": "share", "value": "share", "createdAt": ts, "updatedAt": ts}
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) { j(w, 200, []any{tag}) })
	mux.HandleFunc("GET /api/workflows", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []any{}
		for id, name := range f.workflows {
			if n := r.URL.Query().Get("name"); n == "" || n == name {
				out = append(out, map[string]any{"id": id, "name": name, "enabled": true, "trigger": "AssetTagged", "steps": []any{}, "createdAt": ts, "updatedAt": ts, "description": "", "logging": true})
			}
		}
		j(w, 200, out)
	})
	mux.HandleFunc("POST /api/workflows", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failWorkflows {
			http.Error(w, `{"message":"Unknown method"}`, 400)
			return
		}
		var body struct{ Name string }
		json.NewDecoder(r.Body).Decode(&body)
		id := uuid.NewString()
		f.workflows[id] = body.Name
		j(w, 201, map[string]any{"id": id, "name": body.Name, "enabled": true, "trigger": "AssetTagged", "steps": []any{}, "createdAt": ts, "updatedAt": ts, "description": "", "logging": true})
	})
	mux.HandleFunc("DELETE /api/workflows/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.deletedFlows = append(f.deletedFlows, r.PathValue("id"))
		delete(f.workflows, r.PathValue("id"))
		w.WriteHeader(204)
	})
	return mux
}

type env struct {
	h     *hub.Hub
	f     *fakeImmich
	sess  *immich.API
	woken []string
	st    *store.Store
}

func setup(t *testing.T, allowJoin bool) *env {
	t.Helper()
	f := &fakeImmich{keys: map[string]string{}, workflows: map[string]string{}}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	connect := func(key string) (*immich.API, error) { return immich.New(srv.URL, immich.Auth{APIKey: key}, nil) }
	sess, _ := immich.New(srv.URL, immich.Auth{Cookie: "sess=bob"}, nil)
	family, _ := connect("k-family")

	v, _ := vault.New("a-test-store-key-0123456789")
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	svc := &syncer.Service{Name: "family", Tag: "share", InboxTag: "inbox", RefusedTag: "share-duplicate", ExpiryDays: 30, AllowJoin: allowJoin,
		Receivers: []*syncer.Account{{Name: "family", API: family, UserID: familyID}}}
	e := &env{f: f, sess: sess, st: st}
	e.h = &hub.Hub{Static: []*syncer.Service{svc}, Store: st, Connect: connect, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Hook: syncer.Webhook{URL: "http://app/webhook/immich", Header: "X-Webhook-Secret", Secret: "s"},
		Wake: func(reason string) { e.woken = append(e.woken, reason) }}
	if err := e.h.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e
}

var bob = immich.User{ID: bobID, Name: "Bob", Email: "bob@x.test"}

func senders(e *env) int { return len(e.h.Services()[0].Senders) }

func TestJoinCreatesKeyWorkflowAndMembership(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	if j := e.h.Joinable(bobID); len(j) != 1 {
		t.Fatalf("bob should be offered the service: %v", j)
	}
	if err := e.h.Join(ctx, e.sess, bob, "family"); err != nil {
		t.Fatal(err)
	}
	if len(e.f.keys) != 1 || len(e.f.deletedKeys) != 0 {
		t.Errorf("one live key expected: keys=%d deleted=%v", len(e.f.keys), e.f.deletedKeys)
	}
	if len(e.f.workflows) != 1 {
		t.Errorf("workflow expected: %v", e.f.workflows)
	}
	if senders(e) != 1 || e.h.Services()[0].Senders[0].UserID != bobID {
		t.Errorf("bob must now be a sender")
	}
	links, _ := e.st.Links(ctx)
	if len(links) != 1 || !strings.HasPrefix(links[0].Key, "secret-") {
		t.Errorf("link not stored: %+v", links)
	}
	if len(e.woken) != 1 {
		t.Errorf("a pass must be requested after joining: %v", e.woken)
	}
	if j := e.h.Joinable(bobID); len(j) != 0 {
		t.Errorf("bob must no longer be offered the service")
	}
	if err := e.h.Join(ctx, e.sess, bob, "family"); !errors.Is(err, hub.ErrAlready) {
		t.Errorf("joining twice: %v", err)
	}
}

func TestJoinKeepsMembershipWhenTheWorkflowIsNotReadyYet(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	retried := make(chan struct{}, 1)
	e.h.Retry = func() { retried <- struct{}{} }
	e.f.failWorkflows = true // Immich has not finished loading its plugins
	if err := e.h.Join(ctx, e.sess, bob, "family"); err != nil {
		t.Fatalf("joining must still succeed: %v", err)
	}
	if len(e.f.keys) != 1 || len(e.f.deletedKeys) != 0 || senders(e) != 1 {
		t.Errorf("key and membership must be kept: keys=%d deleted=%v senders=%d", len(e.f.keys), e.f.deletedKeys, senders(e))
	}
	select {
	case <-retried:
	case <-time.After(2 * time.Second):
		t.Error("the workflow setup must be retried in the background")
	}
}

func TestJoinRollsBackWhenTheNewKeyDoesNotWork(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	other := immich.User{ID: uuid.New(), Name: "Someone else", Email: "o@x.test"} // the new key belongs to bob, not to this user
	if err := e.h.Join(ctx, e.sess, other, "family"); err == nil {
		t.Fatal("expected an error")
	}
	if len(e.f.keys) != 0 || len(e.f.deletedKeys) != 1 {
		t.Errorf("the key created for the failed join must be deleted again: keys=%d deleted=%v", len(e.f.keys), e.f.deletedKeys)
	}
	if links, _ := e.st.Links(ctx); len(links) != 0 || senders(e) != 0 {
		t.Errorf("no membership may remain")
	}
}

func TestJoinRefusals(t *testing.T) {
	ctx := context.Background()
	if err := setup(t, false).h.Join(ctx, setup(t, false).sess, bob, "family"); !errors.Is(err, hub.ErrNotJoinable) {
		t.Errorf("closed service: %v", err)
	}
	e := setup(t, true)
	if err := e.h.Join(ctx, e.sess, bob, "nope"); !errors.Is(err, hub.ErrNotJoinable) {
		t.Errorf("unknown service: %v", err)
	}
	familyUser := immich.User{ID: familyID, Name: "Family", Email: "f@x.test"}
	if err := e.h.Join(ctx, e.sess, familyUser, "family"); !errors.Is(err, hub.ErrAlready) {
		t.Errorf("a receiver must not also join as sender: %v", err)
	}
	if len(e.f.keys) != 0 {
		t.Errorf("a refused join must not create a key")
	}
}

func TestLeaveRemovesWorkflowKeyAndMembership(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	if err := e.h.Leave(ctx, e.sess, bob, "family"); !errors.Is(err, hub.ErrNotJoined) {
		t.Errorf("leaving without joining: %v", err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "family"); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Leave(ctx, e.sess, bob, "family"); err != nil {
		t.Fatal(err)
	}
	if len(e.f.workflows) != 0 || len(e.f.deletedFlows) != 1 {
		t.Errorf("workflow must be deleted: %v", e.f.workflows)
	}
	if len(e.f.keys) != 0 || len(e.f.deletedKeys) != 1 {
		t.Errorf("key must be deleted: keys=%d deleted=%v", len(e.f.keys), e.f.deletedKeys)
	}
	if links, _ := e.st.Links(ctx); len(links) != 0 || senders(e) != 0 {
		t.Errorf("membership must be gone")
	}
	if j := e.h.Joinable(bobID); len(j) != 1 {
		t.Errorf("bob can join again after leaving")
	}
}

func TestReloadKeepsMembersAcrossARestart(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	if err := e.h.Join(ctx, e.sess, bob, "family"); err != nil {
		t.Fatal(err)
	}
	fresh := &hub.Hub{Static: e.h.Static, Store: e.st, Connect: e.h.Connect, Log: e.h.Log}
	if err := fresh.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fresh.Services()[0].Senders; len(got) != 1 || got[0].UserID != bobID {
		t.Errorf("members must be restored from the store: %+v", got)
	}
}
