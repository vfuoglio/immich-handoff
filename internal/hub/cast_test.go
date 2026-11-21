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

// castFake is a small Immich with bob's photos, albums, people and a memory, and keys whose permissions it checks.
type castFake struct {
	mu      sync.Mutex
	assets  map[string]*castAsset
	albums  map[string]map[string]bool // album id -> asset ids
	named   map[string]string          // album id -> name
	people  map[string]string          // person id -> name
	keys    map[string][]string
	secrets map[string]string
	memory  []string // the asset ids of one memory
}

type castAsset struct {
	id, kind string
	created  time.Time
	persons  map[string]bool
}

func (f *castFake) photo(kind string, age time.Duration, persons ...string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := &castAsset{id: uuid.NewString(), kind: kind, created: time.Now().Add(-age), persons: map[string]bool{}}
	for _, p := range persons {
		a.persons[p] = true
	}
	f.assets[a.id] = a
	return a.id
}

func (f *castFake) album(name string, ids ...string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := uuid.NewString()
	f.named[id], f.albums[id] = name, map[string]bool{}
	for _, a := range ids {
		f.albums[id][a] = true
	}
	return id
}

func (f *castFake) keyPerms() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, p := range f.keys {
		out = append(out, p)
	}
	return out
}

func (f *castFake) handler() http.Handler {
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
	mux := http.NewServeMux()
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
	mux.HandleFunc("GET /api/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		p, ok := f.keys[r.PathValue("id")]
		if !ok {
			http.Error(w, "gone", 404)
			return
		}
		j(w, 200, map[string]any{"id": r.PathValue("id"), "name": "k", "permissions": p, "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"})
	})
	mux.HandleFunc("PUT /api/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Permissions []string `json:"permissions"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.keys[r.PathValue("id")] = body.Permissions
		j(w, 200, map[string]any{"id": r.PathValue("id"), "name": "k", "permissions": body.Permissions, "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"})
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
		out := []any{}
		if r.URL.Query().Get("isOwned") != "false" {
			for id, name := range f.named {
				out = append(out, map[string]any{"id": id, "albumName": name, "assetCount": len(f.albums[id]), "createdAt": "2026-01-01T00:00:00Z"})
			}
		}
		j(w, 200, out)
	})
	mux.HandleFunc("GET /api/people", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		ps := []any{}
		for id, n := range f.people {
			ps = append(ps, map[string]any{"id": id, "name": n, "isHidden": false, "thumbnailPath": "x"})
		}
		j(w, 200, map[string]any{"hasNextPage": false, "total": len(ps), "hidden": 0, "people": ps})
	})
	mux.HandleFunc("POST /api/search/metadata", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AlbumIds     []string   `json:"albumIds"`
			PersonIds    []string   `json:"personIds"`
			CreatedAfter *time.Time `json:"createdAfter"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if !can(r, "asset.read") {
			http.Error(w, "no", 403)
			return
		}
		items := []any{}
		for _, a := range f.assets {
			if len(body.AlbumIds) > 0 && !f.albums[body.AlbumIds[0]][a.id] {
				continue
			}
			if len(body.PersonIds) > 0 && !a.persons[body.PersonIds[0]] {
				continue
			}
			if body.CreatedAfter != nil && !a.created.After(*body.CreatedAfter) {
				continue
			}
			items = append(items, map[string]any{"id": a.id, "ownerId": bobID.String(), "createdAt": a.created.UTC().Format(time.RFC3339Nano), "fileCreatedAt": a.created.UTC().Format(time.RFC3339Nano),
				"type": a.kind, "originalFileName": "x.jpg", "originalPath": "/p/x.jpg", "checksum": "x", "hasMetadata": true, "fileModifiedAt": "2025-01-01T00:00:00Z"})
		}
		j(w, 200, map[string]any{"albums": map[string]any{"count": 0, "facets": []any{}, "items": []any{}, "total": 0},
			"assets": map[string]any{"count": len(items), "facets": []any{}, "items": items, "total": len(items)}})
	})
	mux.HandleFunc("GET /api/assets/{id}/thumbnail", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !can(r, "asset.view") {
			http.Error(w, "no", 403)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("jpeg-of-" + r.PathValue("id")))
	})
	mux.HandleFunc("GET /api/memories", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !can(r, "memory.read") {
			http.Error(w, "no", 403)
			return
		}
		assets := []any{}
		for _, id := range f.memory {
			assets = append(assets, map[string]any{"id": id})
		}
		j(w, 200, []any{map[string]any{"id": uuid.NewString(), "type": "on_this_day", "data": map[string]any{"year": 2020}, "assets": assets,
			"memoryAt": "2026-03-04T00:00:00Z", "createdAt": "2026-03-04T00:00:00Z", "isSaved": false, "ownerId": bobID.String()}})
	})
	return mux
}

func castSetup(t *testing.T) (*hub.Hub, *castFake, *immich.API) {
	t.Helper()
	f := &castFake{assets: map[string]*castAsset{}, albums: map[string]map[string]bool{}, named: map[string]string{}, people: map[string]string{}, keys: map[string][]string{}, secrets: map[string]string{}}
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

var castBob = immich.User{ID: bobID, Name: "Bob"}

func TestATVIsPairedByACodeAndGetsOnlyThePhotosOfTheChosenAlbumByOpaqueIds(t *testing.T) {
	h, f, sess := castSetup(t)
	ctx := context.Background()
	a, b := f.photo("IMAGE", time.Hour), f.photo("IMAGE", 2*time.Hour)
	f.photo("IMAGE", time.Hour) // not in the album
	video := f.photo("VIDEO", time.Hour)
	album := f.album("Beach", a, b, video)
	secret, code, _, err := h.NewTV("1.2.3.4")
	if err != nil || len(code) != 6 {
		t.Fatalf("a TV asks for a code: %q %v", code, err)
	}
	if st := h.CastState(ctx, secret); st.State != "waiting" {
		t.Fatalf("before the claim: %+v", st)
	}
	s, err := h.ClaimCast(ctx, sess, castBob, strings.ToUpper(code[:3])+" - "+code[3:], hub.CastSource{Kind: store.CastAlbum, ID: uuid.MustParse(album), Seconds: 20, FX: "zoom"})
	if err != nil {
		t.Fatal(err)
	}
	if s.SourceName != "Beach" || s.Seconds != 20 || s.FX != "zoom" {
		t.Fatalf("the session: %+v", s)
	}
	if st := h.CastState(ctx, secret); st.State != "live" || st.Seconds != 20 || st.FX != "zoom" || st.Paused {
		t.Fatalf("after the claim: %+v", st)
	}
	if _, err := h.ClaimCast(ctx, sess, castBob, code, hub.CastSource{Kind: store.CastNew}); !errors.Is(err, hub.ErrBadCastCode) {
		t.Fatalf("a code works once: %v", err)
	}
	feed, err := h.CastFeed(ctx, secret)
	if err != nil || len(feed) != 2 {
		t.Fatalf("the feed holds the album's two photos, not the video or the other: %v %v", feed, err)
	}
	for _, it := range feed {
		if it.ID == a || it.ID == b || len(it.ID) != 16 {
			t.Fatalf("an id is not opaque: %q", it.ID)
		}
		if data, kind, err := h.CastImage(ctx, secret, it.ID); err != nil || kind != "image/jpeg" || !strings.HasPrefix(string(data), "jpeg-of-") {
			t.Fatalf("the picture: %q %v", kind, err)
		}
	}
	if _, _, err := h.CastImage(ctx, secret, a); !errors.Is(err, hub.ErrDropGone) {
		t.Fatalf("an Immich id is not a way in: %v", err)
	}
	if _, err := h.CastFeed(ctx, strings.Repeat("0", 64)); !errors.Is(err, hub.ErrDropGone) {
		t.Fatalf("an unknown TV: %v", err)
	}
	if perms := f.keyPerms(); len(perms) != 1 || strings.Join(perms[0], ",") != "asset.view,asset.read" {
		t.Fatalf("one key with the two permissions: %v", perms)
	}
}

func TestAPersonWhoTypesFiveWrongCodesIsSilencedAndACodeExpires(t *testing.T) {
	h, f, sess := castSetup(t)
	ctx := context.Background()
	a := f.photo("IMAGE", time.Hour)
	album := f.album("A", a)
	_, code, _, _ := h.NewTV("1.2.3.4")
	src := hub.CastSource{Kind: store.CastAlbum, ID: uuid.MustParse(album)}
	for i := 0; i < 5; i++ {
		if _, err := h.ClaimCast(ctx, sess, castBob, "zzzzzz", src); !errors.Is(err, hub.ErrBadCastCode) {
			t.Fatalf("a wrong code: %v", err)
		}
	}
	if _, err := h.ClaimCast(ctx, sess, castBob, code, src); !errors.Is(err, hub.ErrCastBusy) {
		t.Fatalf("even the right code waits after five wrong ones: %v", err)
	}
	if _, ok, _ := h.Store.CastKey(ctx, bobID); ok {
		t.Fatal("a key was made although nothing was paired")
	}
}

func TestACastSourceMustBeThePersonsAndHaveAPhoto(t *testing.T) {
	h, f, sess := castSetup(t)
	ctx := context.Background()
	f.people[uuid.NewString()] = "Nora"
	var nora string
	for id := range f.people {
		nora = id
	}
	f.photo("IMAGE", time.Hour, nora)
	f.photo("IMAGE", 40*24*time.Hour, nora)
	empty := f.album("Empty")
	try := func(src hub.CastSource) (store.CastSession, error) {
		_, code, _, err := h.NewTV("5.5.5.5")
		if err != nil {
			t.Fatal(err)
		}
		return h.ClaimCast(ctx, sess, castBob, code, src)
	}
	if _, err := try(hub.CastSource{Kind: store.CastAlbum, ID: uuid.New()}); !errors.Is(err, hub.ErrNoAlbum) {
		t.Fatalf("an album that is not theirs: %v", err)
	}
	if _, err := try(hub.CastSource{Kind: store.CastPerson, ID: uuid.New()}); !errors.Is(err, hub.ErrRulePerson) {
		t.Fatalf("a person who is not named: %v", err)
	}
	if _, err := try(hub.CastSource{Kind: "everything"}); !errors.Is(err, hub.ErrCastBadSrc) {
		t.Fatalf("a kind that does not exist: %v", err)
	}
	if _, err := try(hub.CastSource{Kind: store.CastMemory, Day: "tomorrow"}); !errors.Is(err, hub.ErrBadCastDay) {
		t.Fatalf("a day that is not a date: %v", err)
	}
	if _, err := try(hub.CastSource{Kind: store.CastAlbum, ID: uuid.MustParse(empty)}); !errors.Is(err, hub.ErrCastEmpty) {
		t.Fatalf("an empty album: %v", err)
	}
	if _, err := try(hub.CastSource{Kind: store.CastNew, Seconds: 2}); !errors.Is(err, hub.ErrBadCastHold) {
		t.Fatalf("a photo that stays 2 seconds: %v", err)
	}
	if _, ok, _ := h.Store.CastKey(ctx, bobID); ok || len(f.keyPerms()) != 0 {
		t.Fatal("a key was kept although nothing was paired")
	}
	if s, err := try(hub.CastSource{Kind: store.CastPerson, ID: uuid.MustParse(nora)}); err != nil || s.SourceName != "Nora" {
		t.Fatalf("a person: %v %v", s, err)
	}
	secret := ""
	sessions, _ := h.CastsOf(ctx, bobID)
	secret = sessions[0].ID
	if feed, _ := h.CastFeed(ctx, secret); len(feed) != 2 {
		t.Fatalf("both photos of Nora: %v", feed)
	}
	if s, err := try(hub.CastSource{Kind: store.CastNew}); err != nil {
		t.Fatal(err)
	} else if feed, _ := h.CastFeed(ctx, s.ID); len(feed) != 1 {
		t.Fatalf("everything new holds only the last 30 days: %v", feed)
	}
}

func TestAMemoryNeedsMemoryReadOnTheKeyAndItIsGivenWithThePersonsSession(t *testing.T) {
	h, f, sess := castSetup(t)
	ctx := context.Background()
	m := f.photo("IMAGE", 24*time.Hour)
	f.mu.Lock()
	f.memory = []string{m}
	f.mu.Unlock()
	f.album("x", f.photo("IMAGE", time.Hour))
	_, code, _, _ := h.NewTV("6.6.6.6")
	s, err := h.ClaimCast(ctx, sess, castBob, code, hub.CastSource{Kind: store.CastMemory, Day: "2026-03-04"})
	if err != nil {
		t.Fatal(err)
	}
	if perms := f.keyPerms(); len(perms) != 1 || !strings.Contains(strings.Join(perms[0], ","), "memory.read") {
		t.Fatalf("memory.read was added: %v", perms)
	}
	if feed, _ := h.CastFeed(ctx, s.ID); len(feed) != 1 {
		t.Fatalf("the memory's photo: %v", feed)
	}
}

func TestAPersonControlsATVAndStoppingItEndsItAndDeletesTheKey(t *testing.T) {
	h, f, sess := castSetup(t)
	ctx := context.Background()
	album := f.album("Beach", f.photo("IMAGE", time.Hour))
	_, code, _, _ := h.NewTV("7.7.7.7")
	s, err := h.ClaimCast(ctx, sess, castBob, code, hub.CastSource{Kind: store.CastAlbum, ID: uuid.MustParse(album)})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.CastControl(ctx, sess, castBob, s.ID, hub.CastPause); err != nil {
		t.Fatal(err)
	}
	if st := h.CastState(ctx, s.ID); !st.Paused {
		t.Fatalf("paused: %+v", st)
	}
	h.CastControl(ctx, sess, castBob, s.ID, hub.CastResume)
	h.CastControl(ctx, sess, castBob, s.ID, hub.CastNext)
	st := h.CastState(ctx, s.ID)
	if st.Paused || st.Seq != 1 || st.Cmd != "next" {
		t.Fatalf("next: %+v", st)
	}
	h.CastControl(ctx, sess, castBob, s.ID, hub.CastPrev)
	if st := h.CastState(ctx, s.ID); st.Seq != 2 || st.Cmd != "prev" {
		t.Fatalf("previous: %+v", st)
	}
	if err := h.CastControl(ctx, sess, castBob, s.ID, "explode"); !errors.Is(err, hub.ErrBadCastCmd) {
		t.Fatalf("a command that does not exist: %v", err)
	}
	if err := h.CastControl(ctx, sess, immich.User{ID: carolID, Name: "Carol"}, s.ID, hub.CastStop); !errors.Is(err, hub.ErrNoCast) {
		t.Fatalf("somebody else's TV: %v", err)
	}
	other := f.album("Hills", f.photo("IMAGE", time.Hour), f.photo("IMAGE", time.Hour))
	if err := h.ChangeCast(ctx, sess, castBob, s.ID, hub.CastSource{Kind: store.CastAlbum, ID: uuid.MustParse(other), Seconds: 30, FX: "rise"}); err != nil {
		t.Fatal(err)
	}
	if st := h.CastState(ctx, s.ID); st.Seq != 3 || st.Cmd != "source" || st.Seconds != 30 || st.FX != "rise" {
		t.Fatalf("a new source: %+v", st)
	}
	if feed, _ := h.CastFeed(ctx, s.ID); len(feed) != 2 {
		t.Fatalf("the new album's photos: %v", feed)
	}
	if err := h.CastControl(ctx, sess, castBob, s.ID, hub.CastStop); err != nil {
		t.Fatal(err)
	}
	if st := h.CastState(ctx, s.ID); st.State != "ended" {
		t.Fatalf("stopped: %+v", st)
	}
	if _, err := h.CastFeed(ctx, s.ID); !errors.Is(err, hub.ErrDropGone) {
		t.Fatalf("an ended TV gets nothing: %v", err)
	}
	if _, ok, _ := h.Store.CastKey(ctx, bobID); ok || len(f.keyPerms()) != 0 {
		t.Fatal("the key outlived the last TV")
	}
}

func TestATVThatWentQuietEndsAndTheKeyGoesWhenThePersonNextOpensTheirPage(t *testing.T) {
	h, f, sess := castSetup(t)
	ctx := context.Background()
	album := f.album("Beach", f.photo("IMAGE", time.Hour))
	_, code, _, _ := h.NewTV("8.8.8.8")
	s, _ := h.ClaimCast(ctx, sess, castBob, code, hub.CastSource{Kind: store.CastAlbum, ID: uuid.MustParse(album)})
	h.Store.TouchCast(ctx, s.ID, time.Now().Add(-time.Hour))
	h.CastMaintenance(ctx)
	if st := h.CastState(ctx, s.ID); st.State != "ended" {
		t.Fatalf("a TV quiet for an hour: %+v", st)
	}
	if len(f.keyPerms()) != 1 {
		t.Fatal("without their session the key cannot be deleted yet")
	}
	h.CastHousekeeping(ctx, sess, bobID)
	if len(f.keyPerms()) != 0 {
		t.Fatal("their next visit deletes the key")
	}
}

func TestAnAddressCannotUseUpTheCodesAndAnOldCodeExpires(t *testing.T) {
	h, _, _ := castSetup(t)
	ctx := context.Background()
	seen := map[string]bool{}
	var first string
	for i := 0; i < 20; i++ {
		s, code, _, err := h.NewTV("9.9.9.9")
		if err != nil || seen[code] {
			t.Fatalf("code %d: %q %v", i, code, err)
		}
		if i == 0 {
			first = s
		}
		seen[code] = true
	}
	if _, _, _, err := h.NewTV("9.9.9.9"); !errors.Is(err, hub.ErrCastAddress) {
		t.Fatalf("the 21st in an hour: %v", err)
	}
	if _, _, _, err := h.NewTV("8.8.4.4"); err != nil {
		t.Fatalf("another address: %v", err)
	}
	h.Now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	if st := h.CastState(ctx, first); st.State != "expired" {
		t.Fatalf("a code after 11 minutes: %+v", st)
	}
}
