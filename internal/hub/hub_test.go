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
	annID    = uuid.MustParse("cccccccc-0000-0000-0000-000000000001")
	familyID = uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000002")

	people = map[string]struct {
		id          uuid.UUID
		name, email string
	}{"bob": {bobID, "Bob", "bob@x.test"}, "ann": {annID, "Ann", "ann@x.test"}}
)

// fakeImmich is a small Immich: one signed-in person (bob) who can create and delete keys and workflows.
type fakeImmich struct {
	mu            sync.Mutex
	failWorkflows bool
	keys          map[string]string // secret -> key id
	owners        map[string]string // secret -> person
	deletedKeys   []string
	workflows     map[string]string // id -> name
	deletedFlows  []string
}

// person is who a request is from: a session cookie ("sess=bob") or one of the keys created for them.
// The caller holds f.mu.
func (f *fakeImmich) person(r *http.Request) string {
	if c := r.Header.Get("Cookie"); strings.HasPrefix(c, "sess=") {
		return strings.TrimPrefix(c, "sess=")
	}
	key := r.Header.Get("x-api-key")
	if _, alive := f.keys[key]; !alive {
		return "" // a deleted key is no longer accepted
	}
	return f.owners[key]
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
		who := f.person(r)
		if p, ok := people[who]; ok {
			j(w, 200, map[string]any{"id": p.id, "name": p.name, "email": p.email})
			return
		}
		http.Error(w, "no", 401)
	})
	mux.HandleFunc("GET /api/api-keys/me", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		_, alive := f.keys[r.Header.Get("x-api-key")]
		f.mu.Unlock()
		if !alive {
			http.Error(w, "no", 401)
			return
		}
		j(w, 200, map[string]any{"id": uuid.NewString(), "name": "k", "permissions": []string{"asset.read"}, "createdAt": ts, "updatedAt": ts})
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
		f.owners[secret] = f.person(r)
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
	ann   *immich.API // the owner of services created in the tests
	woken []string
	st    *store.Store
}

func setup(t *testing.T, allowJoin bool) *env {
	t.Helper()
	f := &fakeImmich{keys: map[string]string{}, owners: map[string]string{}, workflows: map[string]string{}}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	connect := func(key string) (*immich.API, error) { return immich.New(srv.URL, immich.Auth{APIKey: key}, nil) }
	sess, _ := immich.New(srv.URL, immich.Auth{Cookie: "sess=bob"}, nil)
	annSess, _ := immich.New(srv.URL, immich.Auth{Cookie: "sess=ann"}, nil)
	family, _ := connect("k-family")

	v, _ := vault.New("a-test-store-key-0123456789")
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	svc := &syncer.Service{Name: "family", Tag: "share", InboxTag: "inbox", RefusedTag: "share-duplicate", ExpiryDays: 30, AllowJoin: allowJoin,
		Receivers: []*syncer.Account{{Name: "family", API: family, UserID: familyID}}}
	e := &env{f: f, sess: sess, ann: annSess, st: st}
	e.h = &hub.Hub{Static: []*syncer.Service{svc}, Store: st, Connect: connect, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Options: hub.Options{InboxTag: "inbox", RefusedTag: "share-duplicate", ExpiryDays: 30},
		Hook:    syncer.Webhook{URL: "http://app/webhook/immich", Header: "X-Webhook-Secret", Secret: "s"},
		Wake:    func(reason string) { e.woken = append(e.woken, reason) }}
	if err := e.h.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e
}

var (
	bob = immich.User{ID: bobID, Name: "Bob", Email: "bob@x.test"}
	ann = immich.User{ID: annID, Name: "Ann", Email: "ann@x.test"}
)

func senders(e *env) int { return len(e.h.Services()[0].Senders) }

func TestJoinCreatesKeyWorkflowAndMembership(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	if j := e.h.Joinable(bobID); len(j) != 1 {
		t.Fatalf("bob should be offered the service: %v", j)
	}
	if err := e.h.Join(ctx, e.sess, bob, "family", ""); err != nil {
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
	if err := e.h.Join(ctx, e.sess, bob, "family", ""); !errors.Is(err, hub.ErrAlready) {
		t.Errorf("joining twice: %v", err)
	}
}

func TestJoinKeepsMembershipWhenTheWorkflowIsNotReadyYet(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	retried := make(chan struct{}, 1)
	e.h.Retry = func() { retried <- struct{}{} }
	e.f.failWorkflows = true // Immich has not finished loading its plugins
	if err := e.h.Join(ctx, e.sess, bob, "family", ""); err != nil {
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
	if err := e.h.Join(ctx, e.sess, other, "family", ""); err == nil {
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
	if err := setup(t, false).h.Join(ctx, setup(t, false).sess, bob, "family", ""); !errors.Is(err, hub.ErrNotJoinable) {
		t.Errorf("closed service: %v", err)
	}
	e := setup(t, true)
	if err := e.h.Join(ctx, e.sess, bob, "nope", ""); !errors.Is(err, hub.ErrNotJoinable) {
		t.Errorf("unknown service: %v", err)
	}
	familyUser := immich.User{ID: familyID, Name: "Family", Email: "f@x.test"}
	if err := e.h.Join(ctx, e.sess, familyUser, "family", ""); !errors.Is(err, hub.ErrAlready) {
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
	if err := e.h.Join(ctx, e.sess, bob, "family", ""); err != nil {
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
	if err := e.h.Join(ctx, e.sess, bob, "family", ""); err != nil {
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

func service(e *env, name string) *syncer.Service {
	for _, s := range e.h.Services() {
		if s.Label() == name {
			return s
		}
	}
	return nil
}

func TestOwnerCreatesAServiceAndReceiverKeyStaysHidden(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	if err := e.h.CreateService(ctx, e.ann, ann, "holiday", "hol", "approval", ""); err != nil {
		t.Fatal(err)
	}
	svc := service(e, "holiday")
	if svc == nil || svc.OwnerID != annID || !svc.NeedsApproval || svc.AllowJoin || len(svc.Receivers) != 1 || svc.Receivers[0].UserID != annID {
		t.Fatalf("service: %+v", svc)
	}
	if len(e.f.keys) != 1 {
		t.Errorf("one receiver key expected, got %d", len(e.f.keys))
	}
	owned, _ := e.h.Owned(ctx, annID)
	if len(owned) != 1 || owned[0].Service.Label() != "holiday" {
		t.Errorf("owner view: %+v", owned)
	}
	for _, bad := range [][2]string{{"x", "t"}, {"ok name", "a/b"}, {"default", "t"}, {"family", "t"}, {"holiday", "t"}} {
		if err := e.h.CreateService(ctx, e.ann, ann, bad[0], bad[1], "open", ""); err == nil {
			t.Errorf("service %q with tag %q must be refused", bad[0], bad[1])
		}
	}
	if len(e.f.keys) != 1 {
		t.Errorf("refused services must not leave keys behind: %d", len(e.f.keys))
	}
}

func TestApprovalFlowBeforeASenderCanActivate(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	if err := e.h.CreateService(ctx, e.ann, ann, "holiday", "hol", "approval", ""); err != nil {
		t.Fatal(err)
	}
	if offers, _ := e.h.Offers(ctx, bobID); len(offers) != 1 || offers[0].State != "request" {
		t.Fatalf("bob should be able to ask: %+v", offers)
	}
	if err := e.h.Join(ctx, e.sess, bob, "holiday", ""); !errors.Is(err, hub.ErrNeedsApproval) {
		t.Fatalf("joining without approval: %v", err)
	}
	if len(e.f.keys) != 1 {
		t.Fatalf("no sender key may be made before approval (keys=%d)", len(e.f.keys))
	}
	if err := e.h.Ask(ctx, bob, "holiday", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Ask(ctx, bob, "holiday", ""); err != nil {
		t.Errorf("asking twice is harmless: %v", err)
	}
	owned, _ := e.h.Owned(ctx, annID)
	if len(owned[0].Requests) != 1 {
		t.Fatalf("owner must see one pending request: %+v", owned[0].Requests)
	}
	if err := e.h.Decide(ctx, bob, "holiday", owned[0].Requests[0].ID, true); !errors.Is(err, hub.ErrNotOwner) {
		t.Errorf("a non-owner must not decide: %v", err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "holiday", ""); !errors.Is(err, hub.ErrNeedsApproval) {
		t.Errorf("still pending: %v", err)
	}
	if err := e.h.Decide(ctx, ann, "holiday", owned[0].Requests[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if offers, _ := e.h.Offers(ctx, bobID); offers[0].State != "approved" {
		t.Errorf("bob should be able to activate: %+v", offers)
	}
	if err := e.h.Join(ctx, e.sess, bob, "holiday", ""); err != nil {
		t.Fatal(err)
	}
	if got := service(e, "holiday").Senders; len(got) != 1 || got[0].UserID != bobID {
		t.Errorf("bob must be a sender now")
	}
	if left, _ := e.st.Requests(ctx); len(left) != 0 {
		t.Errorf("the used approval must be gone")
	}
}

func TestDeclinedRequestCannotActivate(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, "holiday", "hol", "approval", "")
	e.h.Ask(ctx, bob, "holiday", "")
	owned, _ := e.h.Owned(ctx, annID)
	if err := e.h.Decide(ctx, ann, "holiday", owned[0].Requests[0].ID, false); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "holiday", ""); !errors.Is(err, hub.ErrNeedsApproval) {
		t.Errorf("declined: %v", err)
	}
	if offers, _ := e.h.Offers(ctx, bobID); offers[0].State != "declined" {
		t.Errorf("state: %+v", offers)
	}
}

func TestOpenServiceNeedsNoApprovalAndSameTagIsRefused(t *testing.T) {
	e, ctx := setup(t, true), context.Background() // the static "family" service is open and uses tag "share"
	if err := e.h.CreateService(ctx, e.ann, ann, "everyone", "SHARE", "open", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "family", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "everyone", ""); !errors.Is(err, hub.ErrTagInUse) {
		t.Errorf("bob already sends with the tag 'share' to another service: %v", err)
	}
}

func TestDeletingAServiceLeavesMembersAbleToCleanUp(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, "party", "party", "open", "")
	if err := e.h.Join(ctx, e.sess, bob, "party", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.h.DeleteService(ctx, e.sess, bob, "party"); !errors.Is(err, hub.ErrNotOwner) {
		t.Errorf("only the owner may delete: %v", err)
	}
	if err := e.h.DeleteService(ctx, e.ann, ann, "party"); err != nil {
		t.Fatal(err)
	}
	if service(e, "party") != nil {
		t.Errorf("the service must be gone")
	}
	if len(e.f.deletedKeys) != 1 {
		t.Errorf("the receiver key must be deleted: %v", e.f.deletedKeys)
	}
	if o, _ := e.h.Orphaned(ctx, bobID); len(o) != 1 || o[0] != "party" {
		t.Fatalf("bob must be told the service is gone: %v", o)
	}
	if err := e.h.Leave(ctx, e.sess, bob, "party"); err != nil {
		t.Fatalf("bob can still clean up: %v", err)
	}
	if len(e.f.keys) != 0 {
		t.Errorf("every key must be deleted after cleaning up, %d left", len(e.f.keys))
	}
	if o, _ := e.h.Orphaned(ctx, bobID); len(o) != 0 {
		t.Errorf("nothing left to clean up: %v", o)
	}
}

// deleteKeyOf simulates a person deleting their handoff key in Immich.
func (f *fakeImmich) deleteKeyOf(person string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for secret, who := range f.owners {
		if who == person {
			delete(f.keys, secret)
		}
	}
}

func TestDeletedSenderKeyPausesTheLinkAndTellsPeople(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	var told []string
	e.h.Notify = func(m string) { told = append(told, m) }
	e.h.CreateService(ctx, e.ann, ann, "party", "party", "open", "")
	if err := e.h.Join(ctx, e.sess, bob, "party", ""); err != nil {
		t.Fatal(err)
	}
	e.h.CheckKeys(ctx)
	if senders := len(service(e, "party").Senders); senders != 1 {
		t.Fatalf("healthy keys must not be touched, senders=%d", senders)
	}

	e.f.deleteKeyOf("bob") // bob deletes his key in Immich (his session still works; only the key is gone)
	e.h.CheckKeys(ctx)
	if got := service(e, "party").Senders; len(got) != 0 {
		t.Errorf("a broken link must stop being used: %d senders left", len(got))
	}
	if b, _ := e.h.BrokenFor(ctx, bobID); len(b) != 1 || b[0].Service != "party" {
		t.Errorf("bob must be told: %+v", b)
	}
	if len(told) != 1 || !strings.Contains(told[0], "Bob") {
		t.Errorf("an admin notification is expected: %v", told)
	}
	for _, who := range []uuid.UUID{bobID, annID} {
		acts, _ := e.h.Activity(ctx, who, 10)
		if len(acts) == 0 || !strings.Contains(acts[0].Text, "cut") {
			t.Errorf("both sides see it in the activity (%s): %+v", who, acts)
		}
	}
	members, _ := e.h.MembersOf(ctx, "party")
	if len(members) != 1 || members[0].State != "broken" {
		t.Errorf("the owner sees bob as disconnected: %+v", members)
	}
	// nothing more is attempted, and checking again changes nothing
	e.h.CheckKeys(ctx)
	if len(told) != 1 {
		t.Errorf("a broken link must not be reported over and over: %v", told)
	}
	// bob cleans up and can join again
	if err := e.h.Leave(ctx, e.sess, bob, "party"); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "party", ""); err != nil {
		t.Fatalf("rejoining after clean up: %v", err)
	}
	if len(service(e, "party").Senders) != 1 {
		t.Error("bob is a sender again")
	}
}

func TestDeletedReceiverKeyPausesTheServiceUntilTheOwnerReconnects(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, "party", "party", "open", "")
	e.h.Join(ctx, e.sess, bob, "party", "")

	e.f.deleteKeyOf("ann")
	e.h.CheckKeys(ctx)
	svc := service(e, "party")
	if svc.Broken == "" || len(svc.Receivers) != 0 {
		t.Fatalf("the service must be paused with no receiver: %+v", svc)
	}
	if len(svc.Senders) != 1 {
		t.Errorf("senders stay members while the service is paused")
	}
	if offers, _ := e.h.Offers(ctx, annID); len(offers) != 0 {
		t.Errorf("the owner must not be offered their own paused service: %+v", offers)
	}
	if err := e.h.ReconnectService(ctx, e.sess, bob, "party"); !errors.Is(err, hub.ErrNotOwner) {
		t.Errorf("only the owner reconnects: %v", err)
	}
	woken := len(e.woken)
	if err := e.h.ReconnectService(ctx, e.ann, ann, "party"); err != nil {
		t.Fatal(err)
	}
	svc = service(e, "party")
	if svc.Broken != "" || len(svc.Receivers) != 1 || len(svc.Senders) != 1 {
		t.Errorf("after reconnecting the service works again: %+v", svc)
	}
	if len(e.woken) != woken+1 {
		t.Errorf("a pass must run right after reconnecting")
	}
}

func TestRejectedKeyFromTheConfigurationIsLeftToThePass(t *testing.T) {
	e := setup(t, false)
	e.h.KeyRejected(e.h.Static[0].Receivers[0]) // not one of the stored accounts: must not panic or change anything
	if len(e.h.Services()) != 1 {
		t.Error("services must be unchanged")
	}
}

func TestInviteOnlyServiceIsHiddenAndReachedWithTheLink(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	if err := e.h.CreateService(ctx, e.ann, ann, "secret club", "club", "open", "invite"); err != nil {
		t.Fatal(err)
	}
	svc := service(e, "secret club")
	token := e.h.InviteOf(svc)
	if token == "" || !svc.Unlisted {
		t.Fatalf("an invite-only service needs a link: %+v", svc)
	}
	if offers, _ := e.h.Offers(ctx, bobID); len(offers) != 0 {
		t.Errorf("it must not appear in the directory: %+v", offers)
	}
	if err := e.h.Join(ctx, e.sess, bob, "secret club", ""); !errors.Is(err, hub.ErrNotJoinable) {
		t.Errorf("joining by name without the link: %v", err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "secret club", "wrong"); !errors.Is(err, hub.ErrNotJoinable) {
		t.Errorf("a wrong link: %v", err)
	}
	if len(e.f.keys) != 1 {
		t.Errorf("a refused join must not create a key, got %d", len(e.f.keys))
	}
	if o, err := e.h.InviteOffer(ctx, bobID, token); err != nil || o.Service.Label() != "secret club" || o.State != "join" {
		t.Fatalf("the link leads to the service: %+v %v", o, err)
	}
	if _, err := e.h.InviteOffer(ctx, bobID, "wrong"); !errors.Is(err, hub.ErrNoInvite) {
		t.Errorf("a wrong link: %v", err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "secret club", token); err != nil {
		t.Fatalf("joining with the link: %v", err)
	}
	if _, err := e.h.InviteOffer(ctx, bobID, token); !errors.Is(err, hub.ErrAlready) {
		t.Errorf("a member following the link again: %v", err)
	}
}

func TestNewInviteRevokesTheOldLinkAndListingRemovesTheNeedForOne(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, "club", "club", "open", "invite")
	old := e.h.InviteOf(service(e, "club"))
	if err := e.h.NewInvite(ctx, bob, "club"); !errors.Is(err, hub.ErrNotOwner) {
		t.Errorf("only the owner makes links: %v", err)
	}
	if err := e.h.NewInvite(ctx, ann, "club"); err != nil {
		t.Fatal(err)
	}
	fresh := e.h.InviteOf(service(e, "club"))
	if fresh == "" || fresh == old {
		t.Fatalf("a new link is expected: %q vs %q", fresh, old)
	}
	if _, err := e.h.InviteOffer(ctx, bobID, old); !errors.Is(err, hub.ErrNoInvite) {
		t.Errorf("the old link must stop working: %v", err)
	}
	if err := e.h.SetVisibility(ctx, ann, "club", "listed"); err != nil {
		t.Fatal(err)
	}
	if offers, _ := e.h.Offers(ctx, bobID); len(offers) != 1 {
		t.Errorf("a listed service appears in the directory: %+v", offers)
	}
	if err := e.h.Join(ctx, e.sess, bob, "club", ""); err != nil {
		t.Errorf("a listed service needs no link: %v", err)
	}
}

func TestInviteWithApprovalNeedsTheLinkOnlyToAsk(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, "club", "club", "approval", "invite")
	token := e.h.InviteOf(service(e, "club"))
	if err := e.h.Ask(ctx, bob, "club", ""); !errors.Is(err, hub.ErrNotJoinable) {
		t.Errorf("asking without the link: %v", err)
	}
	if err := e.h.Ask(ctx, bob, "club", token); err != nil {
		t.Fatal(err)
	}
	owned, _ := e.h.Owned(ctx, annID)
	if err := e.h.Decide(ctx, ann, "club", owned[0].Requests[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if err := e.h.NewInvite(ctx, ann, "club"); err != nil { // the link changes, the approval stays valid
		t.Fatal(err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "club", ""); err != nil {
		t.Errorf("an approved person activates without the link: %v", err)
	}
}
