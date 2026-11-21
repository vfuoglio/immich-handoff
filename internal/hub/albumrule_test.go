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

// smartFake is a small Immich with one person's photos, each with a camera make, and their albums and keys.
type smartFake struct {
	mu      sync.Mutex
	assets  map[string]*smartAsset
	albums  map[string]*fldAlbum
	keys    map[string][]string
	secrets map[string]string
}

type smartAsset struct {
	id, make string
	created  time.Time
}

func (f *smartFake) photo(make string, age time.Duration) *smartAsset {
	a := &smartAsset{id: uuid.NewString(), make: make, created: time.Now().Add(-age)}
	f.mu.Lock()
	f.assets[a.id] = a
	f.mu.Unlock()
	return a
}

func (f *smartFake) handler() http.Handler {
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
	mux.HandleFunc("GET /api/users/me", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !can(r, "user.read") {
			http.Error(w, "no", 401)
			return
		}
		j(w, 200, map[string]any{"id": bobID.String(), "name": "Bob", "email": "bob@x.test"})
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
	mux.HandleFunc("GET /api/search/suggestions", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !can(r, "asset.read") {
			http.Error(w, "no", 403)
			return
		}
		out := []string{}
		seen := map[string]bool{}
		for _, a := range f.assets {
			if !seen[a.make] {
				seen[a.make] = true
				out = append(out, a.make)
			}
		}
		j(w, 200, out)
	})
	mux.HandleFunc("POST /api/search/metadata", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Make         string      `json:"make"`
			AlbumIds     []uuid.UUID `json:"albumIds"`
			CreatedAfter *time.Time  `json:"createdAfter"`
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
			if (body.Make != "" && a.make != body.Make) || (body.CreatedAfter != nil && !a.created.After(*body.CreatedAfter)) {
				continue
			}
			if len(body.AlbumIds) > 0 && !f.albums[body.AlbumIds[0].String()].members[a.id] {
				continue
			}
			items = append(items, map[string]any{"id": a.id, "ownerId": bobID.String(), "createdAt": a.created.UTC().Format(time.RFC3339Nano), "thumbhash": "abc",
				"type": "IMAGE", "originalFileName": "x.jpg", "originalPath": "/p/x.jpg", "checksum": "x", "hasMetadata": true, "fileCreatedAt": "2025-01-01T00:00:00Z", "fileModifiedAt": "2025-01-01T00:00:00Z"})
		}
		j(w, 200, map[string]any{"albums": map[string]any{"count": 0, "facets": []any{}, "items": []any{}, "total": 0},
			"assets": map[string]any{"count": len(items), "facets": []any{}, "items": items, "total": len(items)}})
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
			if r.URL.Query().Get("name") == "" || r.URL.Query().Get("name") == al.name {
				out = append(out, map[string]any{"id": id, "albumName": al.name, "assetCount": len(al.members)})
			}
		}
		j(w, 200, out)
	})
	mux.HandleFunc("POST /api/albums", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AlbumName string `json:"albumName"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if !can(r, "album.create") {
			http.Error(w, "no", 403)
			return
		}
		id := uuid.NewString()
		f.albums[id] = &fldAlbum{owner: "bob", name: body.AlbumName, members: map[string]bool{}}
		j(w, 201, map[string]any{"id": id, "albumName": body.AlbumName, "assetCount": 0})
	})
	mux.HandleFunc("PUT /api/albums/{id}/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Ids []string }
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if !can(r, "albumAsset.create") {
			http.Error(w, "no", 403)
			return
		}
		out := []any{}
		for _, id := range body.Ids {
			f.albums[r.PathValue("id")].members[id] = true
			out = append(out, map[string]any{"id": id, "success": true})
		}
		j(w, 200, out)
	})
	mux.HandleFunc("DELETE /api/albums/{id}/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Ids []string }
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if !can(r, "albumAsset.delete") {
			http.Error(w, "no", 403)
			return
		}
		out := []any{}
		for _, id := range body.Ids {
			delete(f.albums[r.PathValue("id")].members, id)
			out = append(out, map[string]any{"id": id, "success": true})
		}
		j(w, 200, out)
	})
	return mux
}

func (f *smartFake) inAlbum(name string) map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]bool{}
	for _, al := range f.albums {
		if al.name == name {
			for id := range al.members {
				out[id] = true
			}
		}
	}
	return out
}

func smartSetup(t *testing.T) (*hub.Hub, *smartFake, *immich.API, *store.Store) {
	t.Helper()
	f := &smartFake{assets: map[string]*smartAsset{}, albums: map[string]*fldAlbum{}, keys: map[string][]string{}, secrets: map[string]string{}}
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
	return h, f, sess, st
}

func canon(name string, stale bool) hub.AlbumParams {
	return hub.AlbumParams{RuleParams: hub.RuleParams{Make: "Canon"}, AlbumTarget: hub.AlbumTarget{New: name, RemoveStale: stale}}
}

func TestASmartAlbumOffersWhatMatchesAndFillsTheAlbumOnlyOnYes(t *testing.T) {
	h, f, sess, _ := smartSetup(t)
	ctx := context.Background()
	old := f.photo("Canon", 30*24*time.Hour) // there before the rule: only a look back offers it
	f.photo("Canon", time.Hour)
	r, err := h.FollowAlbumRule(ctx, sess, immich.User{ID: bobID, Name: "Bob"}, canon("Canon pictures", false))
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != store.RuleKindAlbum || r.DestAlbum != "Canon pictures" || r.Dest == "" {
		t.Fatalf("the rule is %+v", r)
	}
	f.photo("Nikon", 0)
	f.photo("Canon", 0) // after the rule
	time.Sleep(5 * time.Millisecond)
	n, err := h.LookRule(ctx, r)
	if err != nil || n != 1 {
		t.Fatalf("looked and found %d, %v; want the one Canon that came after the rule", n, err)
	}
	if got := len(f.inAlbum("Canon pictures")); got != 0 {
		t.Fatalf("%d photos in the album before the answer", got)
	}
	if err := h.AnswerSuggestion(ctx, immich.User{ID: bobID, Name: "Bob"}, r.ID, hub.AnswerSend, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(f.inAlbum("Canon pictures")); got != 1 {
		t.Fatalf("%d photos in the album after yes, want 1", got)
	}
	if n, _ := h.LookRule(ctx, r); n != 0 {
		t.Fatalf("a second look offered %d again", n)
	}
	// the older photo comes with a look back, which asks first too
	if n, err := h.LookBack(ctx, immich.User{ID: bobID, Name: "Bob"}, r.ID); err != nil || n != 2 {
		t.Fatalf("look back found %d, %v; want the two Canons from before the rule", n, err)
	}
	if f.inAlbum("Canon pictures")[old.id] {
		t.Fatal("the old photo was put in without asking")
	}
}

func TestASmartAlbumNeverOffersWhatIsInTheAlbumAndKeepsADeclinedPhotoOut(t *testing.T) {
	h, f, sess, _ := smartSetup(t)
	ctx := context.Background()
	user := immich.User{ID: bobID, Name: "Bob"}
	f.photo("Canon", time.Hour)
	r, err := h.FollowAlbumRule(ctx, sess, user, canon("C", false))
	if err != nil {
		t.Fatal(err)
	}
	mine := f.photo("Canon", 0)
	other := f.photo("Canon", 0)
	for id := range f.albums { // the person added one by hand
		f.albums[id].members[mine.id] = true
	}
	time.Sleep(5 * time.Millisecond)
	if n, _ := h.LookRule(ctx, r); n != 1 {
		t.Fatalf("offered %d, want only the one that is not in the album", n)
	}
	if err := h.AnswerSuggestion(ctx, user, r.ID, hub.AnswerDecline, nil); err != nil {
		t.Fatal(err)
	}
	if n, _ := h.LookRule(ctx, r); n != 0 || f.inAlbum("C")[other.id] {
		t.Fatalf("a declined photo came back (%d) or went in", n)
	}
}

func TestASmartAlbumFillsByItselfAfterThreeAlwaysAndOnlyWithNewPhotos(t *testing.T) {
	h, f, sess, _ := smartSetup(t)
	ctx := context.Background()
	user := immich.User{ID: bobID, Name: "Bob"}
	f.photo("Canon", time.Hour)
	r, err := h.FollowAlbumRule(ctx, sess, user, canon("Auto", false))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.SetRuleAuto(ctx, user, r.ID, true); !errors.Is(err, hub.ErrAutoTooSoon) {
		t.Fatalf("auto at once: %v", err)
	}
	for i := 0; i < 3; i++ {
		f.photo("Canon", 0)
		time.Sleep(5 * time.Millisecond)
		h.LookRule(ctx, r)
		if err := h.AnswerSuggestion(ctx, user, r.ID, hub.AnswerAlways, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.SetRuleAuto(ctx, user, r.ID, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	f.photo("Canon", 0)
	time.Sleep(30 * time.Millisecond)
	r, _ = h.Store.RuleByID(ctx, r.ID)
	if _, err := h.LookRule(ctx, r); err != nil {
		t.Fatal(err)
	}
	if got := len(f.inAlbum("Auto")); got != 4 {
		t.Fatalf("%d photos in the album, want 4 (three answered, one by itself)", got)
	}
}

func TestASmartAlbumTakesOutOnlyWhatItPutThereWhenAskedTo(t *testing.T) {
	h, f, sess, _ := smartSetup(t)
	ctx := context.Background()
	user := immich.User{ID: bobID, Name: "Bob"}
	f.photo("Canon", time.Hour)
	r, err := h.FollowAlbumRule(ctx, sess, user, canon("Stale", true))
	if err != nil {
		t.Fatal(err)
	}
	a := f.photo("Canon", 0)
	hand := f.photo("Nikon", 0) // a photo the person put there themself
	for id := range f.albums {
		f.albums[id].members[hand.id] = true
	}
	time.Sleep(5 * time.Millisecond)
	h.LookRule(ctx, r)
	if err := h.AnswerSuggestion(ctx, user, r.ID, hub.AnswerSend, nil); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	a.make = "Nikon" // it stops matching
	f.mu.Unlock()
	if _, err := h.LookRule(ctx, r); err != nil {
		t.Fatal(err)
	}
	in := f.inAlbum("Stale")
	if in[a.id] || !in[hand.id] {
		t.Fatalf("album holds %v: the rule's photo must go, the one added by hand must stay", in)
	}
	if n, _ := h.LookRule(ctx, r); n != 0 {
		t.Fatalf("the photo taken out was offered again (%d)", n)
	}
}

func TestASmartAlbumRefusesWhatItCannotDoAndDeletesItsKeyWithTheLastRule(t *testing.T) {
	h, f, sess, st := smartSetup(t)
	ctx := context.Background()
	user := immich.User{ID: bobID, Name: "Bob"}
	f.photo("Canon", time.Hour)
	if _, err := h.FollowAlbumRule(ctx, sess, user, hub.AlbumParams{AlbumTarget: hub.AlbumTarget{New: "x"}}); !errors.Is(err, hub.ErrRuleEmpty) {
		t.Fatalf("a rule with nothing to look for: %v", err)
	}
	if _, err := h.FollowAlbumRule(ctx, sess, user, hub.AlbumParams{RuleParams: hub.RuleParams{Make: "Leica"}, AlbumTarget: hub.AlbumTarget{New: "x"}}); !errors.Is(err, hub.ErrRuleCamera) {
		t.Fatalf("a camera the library does not hold: %v", err)
	}
	if _, err := h.FollowAlbumRule(ctx, sess, user, canon("", false)); !errors.Is(err, hub.ErrBadAlbumName) {
		t.Fatalf("no album: %v", err)
	}
	p := canon("x", true)
	p.Phrase = "a beach"
	if _, err := h.FollowAlbumRule(ctx, sess, user, p); !errors.Is(err, hub.ErrStalePhrase) {
		t.Fatalf("taking out with a phrase: %v", err)
	}
	if _, ok, _ := st.AlbumKey(ctx, bobID); ok {
		t.Fatal("a key was kept although no rule was made")
	}
	r, err := h.FollowAlbumRule(ctx, sess, user, canon("Keep", false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.FollowAlbumRule(ctx, sess, user, canon("Keep", false)); !errors.Is(err, hub.ErrAlbumTwice) {
		t.Fatalf("the same rule twice: %v", err)
	}
	if _, ok, _ := st.AlbumKey(ctx, bobID); !ok || len(f.keys) != 1 {
		t.Fatalf("key kept %v, keys in Immich %d", ok, len(f.keys))
	}
	if err := h.DeleteRuleWith(ctx, sess, user, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.AlbumKey(ctx, bobID); ok || len(f.keys) != 0 {
		t.Fatalf("the key outlived the last rule (store %v, Immich %d)", ok, len(f.keys))
	}
}
