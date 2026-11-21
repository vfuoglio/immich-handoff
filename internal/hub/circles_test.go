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
	"github.com/vfuoglio/immich-handoff/internal/vault"
)

var (
	carolID = uuid.MustParse("c0000000-0000-4000-8000-000000000001")
	daveID  = uuid.MustParse("d0000000-0000-4000-8000-000000000002")
)

// cirFake is a small Immich with bob's albums, who is on each, and the keys.
type cirFake struct {
	mu      sync.Mutex
	albums  map[string]*cirAlbum
	keys    map[string][]string
	secrets map[string]string
}

type cirAlbum struct {
	name    string
	created time.Time
	users   map[string]string // user id -> role
}

func (f *cirFake) album(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := uuid.NewString()
	f.albums[id] = &cirAlbum{name: name, created: time.Now(), users: map[string]string{}}
	return id
}

func (f *cirFake) usersOf(id string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for k, v := range f.albums[id].users {
		out[k] = v
	}
	return out
}

func (f *cirFake) handler() http.Handler {
	j := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	can := func(r *http.Request, perm string) bool {
		if strings.HasPrefix(r.Header.Get("Cookie"), "sess=") {
			return true
		}
		id, ok := f.secrets[r.Header.Get("x-api-key")]
		if !ok {
			return false
		}
		for _, p := range f.keys[id] {
			if p == perm {
				return true
			}
		}
		return false
	}
	people := map[string]string{bobID.String(): "Bob", carolID.String(): "Carol", daveID.String(): "Dave"}
	user := func(id string) map[string]any {
		return map[string]any{"id": id, "name": people[id], "email": strings.ToLower(people[id]) + "@x.test", "avatarColor": "primary"}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/users/me", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !can(r, "user.read") {
			http.Error(w, "no", 401)
			return
		}
		j(w, 200, user(bobID.String()))
	})
	mux.HandleFunc("GET /api/users", func(w http.ResponseWriter, r *http.Request) {
		if !can(r, "user.read") {
			http.Error(w, "no", 401)
			return
		}
		out := []any{}
		for id := range people {
			out = append(out, user(id))
		}
		j(w, 200, out)
	})
	mux.HandleFunc("POST /api/api-keys", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name        string   `json:"name"`
			Permissions []string `json:"permissions"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		id, secret := uuid.NewString(), "secret-"+uuid.NewString()
		f.keys[id], f.secrets[secret] = body.Permissions, id
		j(w, 201, map[string]any{"id": id, "name": body.Name, "secret": secret, "permissions": body.Permissions, "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"})
	})
	mux.HandleFunc("DELETE /api/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := r.PathValue("id")
		if _, ok := f.keys[id]; !ok {
			http.Error(w, "gone", 404)
			return
		}
		delete(f.keys, id)
		for s, k := range f.secrets {
			if k == id {
				delete(f.secrets, s)
			}
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /api/albums", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !can(r, "album.read") {
			http.Error(w, "no", 403)
			return
		}
		out := []any{}
		for id, al := range f.albums {
			users := []any{map[string]any{"role": "owner", "user": user(bobID.String())}}
			for u, role := range al.users {
				users = append(users, map[string]any{"role": role, "user": user(u)})
			}
			out = append(out, map[string]any{"id": id, "albumName": al.name, "assetCount": 0, "createdAt": al.created.UTC().Format(time.RFC3339Nano), "albumUsers": users, "shared": len(al.users) > 0})
		}
		j(w, 200, out)
	})
	mux.HandleFunc("PUT /api/albums/{id}/users", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AlbumUsers []struct {
				UserId string `json:"userId"`
				Role   string `json:"role"`
			} `json:"albumUsers"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if !can(r, "albumUser.create") {
			http.Error(w, "no", 403)
			return
		}
		al, ok := f.albums[r.PathValue("id")]
		if !ok {
			http.Error(w, "gone", 404)
			return
		}
		for _, u := range body.AlbumUsers {
			al.users[u.UserId] = u.Role
		}
		j(w, 200, map[string]any{"id": r.PathValue("id"), "albumName": al.name})
	})
	mux.HandleFunc("DELETE /api/albums/{id}/user/{user}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !can(r, "albumUser.delete") {
			http.Error(w, "no", 403)
			return
		}
		if al, ok := f.albums[r.PathValue("id")]; ok {
			delete(al.users, r.PathValue("user"))
		}
		w.WriteHeader(200)
	})
	return mux
}

func cirSetup(t *testing.T) (*hub.Hub, *cirFake, *immich.API) {
	t.Helper()
	f := &cirFake{albums: map[string]*cirAlbum{}, keys: map[string][]string{}, secrets: map[string]string{}}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	connect := func(key string) (*immich.API, error) {
		if key == "" {
			return immich.New(srv.URL, immich.Auth{Cookie: "sess=bob"}, nil)
		}
		return immich.New(srv.URL, immich.Auth{APIKey: key}, nil)
	}
	sess, _ := connect("")
	v, _ := vault.New("a-test-store-key-0123456789")
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := &hub.Hub{Store: st, Connect: connect, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), RulesSettle: time.Millisecond}
	return h, f, sess
}

var cirBob = immich.User{ID: bobID, Name: "Bob"}

func TestACircleOffersNewAlbumsSharesOnYesAndTakesAPersonOffOnlyWhatItShared(t *testing.T) {
	h, f, sess := cirSetup(t)
	ctx := context.Background()
	before := f.album("Before the circle")
	c, err := h.AddCircle(ctx, sess, cirBob, hub.CircleParams{Name: "Family", Emails: []string{"carol@x.test", "dave@x.test"}})
	if err != nil {
		t.Fatal(err)
	}
	trip := f.album("Trip 2026")
	byHand := f.album("Shared by hand")
	f.mu.Lock()
	f.albums[byHand].users[carolID.String()] = "editor" // carol is on it already
	f.mu.Unlock()
	time.Sleep(10 * time.Millisecond)
	h.CirclesMaintenance(ctx)
	if len(f.usersOf(trip)) != 0 {
		t.Fatal("an album was shared before the owner answered")
	}
	sug, _ := h.CircleSuggestions(ctx, bobID)
	if len(sug) != 1 || len(sug[0].Albums) != 2 {
		t.Fatalf("suggestions %+v: the two new albums are offered, not the one from before", sug)
	}
	if err := h.AnswerCircle(ctx, cirBob, c.ID, hub.AnswerSend); err != nil {
		t.Fatal(err)
	}
	if u := f.usersOf(trip); len(u) != 2 || u[carolID.String()] != "viewer" || u[daveID.String()] != "viewer" {
		t.Fatalf("the trip album is shared with both as viewers: %v", u)
	}
	if u := f.usersOf(byHand); len(u) != 2 || u[carolID.String()] != "editor" {
		t.Fatalf("carol keeps her own role and dave is added: %v", u)
	}
	if len(f.usersOf(before)) != 0 {
		t.Fatal("an album from before the circle was shared")
	}
	// taking carol out undoes only what the app shared
	if err := h.RemoveCircleMemberFrom(ctx, cirBob, c.ID, carolID); err != nil {
		t.Fatal(err)
	}
	if _, on := f.usersOf(trip)[carolID.String()]; on {
		t.Fatal("carol is still on the album the circle shared")
	}
	if _, on := f.usersOf(byHand)[carolID.String()]; !on {
		t.Fatal("carol was taken off an album she was on before the circle")
	}
	if _, on := f.usersOf(trip)[daveID.String()]; !on {
		t.Fatal("dave was taken off too")
	}
	// the catch-up offers what was there before, and only offers
	n, err := h.CatchUpCircle(ctx, cirBob, c.ID)
	if err != nil || n == 0 {
		t.Fatalf("catch up: %d %v", n, err)
	}
	if len(f.usersOf(before)) != 0 {
		t.Fatal("the catch-up shared an album without asking")
	}
}

func TestACircleNeverAddsAPersonWhoLeftOrWasDeclinedAndSharesByItselfAfterThreeAlwaysOnlyForLaterAlbums(t *testing.T) {
	h, f, sess := cirSetup(t)
	ctx := context.Background()
	c, err := h.AddCircle(ctx, sess, cirBob, hub.CircleParams{Name: "Friends", Role: "editor", Prefix: "pub ", Emails: []string{"carol@x.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.SetCircleAuto(ctx, cirBob, c.ID, true); !errors.Is(err, hub.ErrCircleAutoSoon) {
		t.Fatalf("auto at once: %v", err)
	}
	private := f.album("private")
	one := f.album("pub one")
	time.Sleep(10 * time.Millisecond)
	h.CirclesMaintenance(ctx)
	if err := h.AnswerCircle(ctx, cirBob, c.ID, hub.AnswerAlways); err != nil {
		t.Fatal(err)
	}
	if f.usersOf(one)[carolID.String()] != "editor" || len(f.usersOf(private)) != 0 {
		t.Fatalf("the name filter and the role: %v %v", f.usersOf(one), f.usersOf(private))
	}
	// carol leaves the album herself: she is not added again
	f.mu.Lock()
	delete(f.albums[one].users, carolID.String())
	f.mu.Unlock()
	h.CirclesMaintenance(ctx)
	h.CirclesMaintenance(ctx)
	if len(f.usersOf(one)) != 0 {
		t.Fatal("a person who left was added again")
	}
	// declined albums are not offered again
	two := f.album("pub two")
	time.Sleep(10 * time.Millisecond)
	h.CirclesMaintenance(ctx)
	if err := h.AnswerCircle(ctx, cirBob, c.ID, hub.AnswerDecline); err != nil {
		t.Fatal(err)
	}
	h.CirclesMaintenance(ctx)
	if sug, _ := h.CircleSuggestions(ctx, bobID); len(sug) != 0 || len(f.usersOf(two)) != 0 {
		t.Fatalf("a declined album came back: %+v", sug)
	}
	for i := 0; i < 2; i++ {
		f.album("pub more")
		time.Sleep(10 * time.Millisecond)
		h.CirclesMaintenance(ctx)
		if err := h.AnswerCircle(ctx, cirBob, c.ID, hub.AnswerAlways); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.SetCircleAuto(ctx, cirBob, c.ID, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	late := f.album("pub late")
	time.Sleep(30 * time.Millisecond)
	h.CirclesMaintenance(ctx)
	if f.usersOf(late)[carolID.String()] != "editor" {
		t.Fatalf("an album made after the switch was not shared by itself: %v", f.usersOf(late))
	}
}

func TestACircleChecksItsPeopleAndTheAdministratorsChoiceOfHowTheyArePicked(t *testing.T) {
	h, f, sess := cirSetup(t)
	ctx := context.Background()
	if _, err := h.AddCircle(ctx, sess, cirBob, hub.CircleParams{Name: "x", Emails: []string{"nobody@x.test"}}); !errors.Is(err, hub.ErrNoSuchPerson) {
		t.Fatalf("an email nobody has: %v", err)
	}
	if _, err := h.AddCircle(ctx, sess, cirBob, hub.CircleParams{Name: "x", Emails: []string{"bob@x.test"}}); !errors.Is(err, hub.ErrCircleSelf) {
		t.Fatalf("oneself: %v", err)
	}
	if _, err := h.AddCircle(ctx, sess, cirBob, hub.CircleParams{Name: "", Emails: []string{"carol@x.test"}}); !errors.Is(err, hub.ErrBadCircleName) {
		t.Fatalf("no name: %v", err)
	}
	if _, err := h.AddCircle(ctx, sess, cirBob, hub.CircleParams{Name: "x"}); !errors.Is(err, hub.ErrCircleNobody) {
		t.Fatalf("nobody: %v", err)
	}
	if _, err := h.AddCircle(ctx, sess, cirBob, hub.CircleParams{Name: "x", Role: "owner", Emails: []string{"carol@x.test"}}); !errors.Is(err, hub.ErrBadCircleRole) {
		t.Fatalf("a role that cannot be given: %v", err)
	}
	if _, ok, _ := h.Store.CircleKey(ctx, bobID); ok || len(f.keys) != 0 {
		t.Fatal("a key was kept although no circle was made")
	}
	if _, err := h.CircleUsers(ctx, sess, bobID); !errors.Is(err, hub.ErrCircleMethod) {
		t.Fatalf("the list of accounts is not offered by default: %v", err)
	}
	if err := h.SetCircleMethod(ctx, 2); err != nil {
		t.Fatal(err)
	}
	users, err := h.CircleUsers(ctx, sess, bobID)
	if err != nil || len(users) != 2 {
		t.Fatalf("method 2 lists the other accounts: %v %v", users, err)
	}
	if _, err := h.AddCircle(ctx, sess, cirBob, hub.CircleParams{Name: "x", Emails: []string{"carol@x.test"}}); !errors.Is(err, hub.ErrCircleNobody) {
		t.Fatalf("an email is not taken once people are picked from the list: %v", err)
	}
	c, err := h.AddCircle(ctx, sess, cirBob, hub.CircleParams{Name: "Picked", Users: []uuid.UUID{carolID, carolID}})
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := h.Store.CircleMembers(ctx, c.ID); len(m) != 1 {
		t.Fatalf("a person once: %v", m)
	}
	if err := h.DeleteCircleWith(ctx, sess, cirBob, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := h.Store.CircleKey(ctx, bobID); ok || len(f.keys) != 0 {
		t.Fatalf("the key outlived the last circle")
	}
}
