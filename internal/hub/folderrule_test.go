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
	"sort"
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

// fldFake is a small Immich with a person's photos of an external library, their albums and keys.
type fldFake struct {
	mu       sync.Mutex
	assets   []*fldAsset
	albums   map[string]*fldAlbum
	keys     map[string][]string // key id -> permissions
	secrets  map[string]string   // secret -> key id
	searches int
}

type fldAsset struct {
	id, owner, path string
	created         time.Time
	trashed         bool
}

type fldAlbum struct {
	owner, name string
	members     map[string]bool
}

func (f *fldFake) photo(owner, path string, age time.Duration) *fldAsset {
	a := &fldAsset{id: uuid.NewString(), owner: owner, path: path, created: time.Now().Add(-age)}
	f.assets = append(f.assets, a)
	return a
}

func (f *fldFake) handler() http.Handler {
	j := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	who := func(r *http.Request) string {
		if strings.HasPrefix(r.Header.Get("Cookie"), "sess=") {
			return strings.TrimPrefix(r.Header.Get("Cookie"), "sess=")
		}
		if id, ok := f.secrets[r.Header.Get("x-api-key")]; ok {
			_ = id
			return "bob"
		}
		return ""
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
		if who(r) != "bob" || !can(r, "user.read") {
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
	mux.HandleFunc("POST /api/search/metadata", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OriginalPath string     `json:"originalPath"`
			CreatedAfter *time.Time `json:"createdAfter"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if !can(r, "asset.read") {
			http.Error(w, "no", 403)
			return
		}
		f.searches++
		items := []any{}
		for _, a := range f.assets {
			if a.owner != who(r) || !strings.Contains(a.path, body.OriginalPath) || (body.CreatedAfter != nil && !a.created.After(*body.CreatedAfter)) {
				continue
			}
			items = append(items, map[string]any{"id": a.id, "ownerId": bobID.String(), "originalPath": a.path, "isTrashed": a.trashed, "createdAt": a.created.UTC().Format(time.RFC3339),
				"type": "IMAGE", "originalFileName": filepath.Base(a.path), "checksum": "x", "hasMetadata": true, "fileCreatedAt": "2025-01-01T00:00:00Z", "fileModifiedAt": "2025-01-01T00:00:00Z"})
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
			if al.owner == who(r) && (r.URL.Query().Get("name") == "" || r.URL.Query().Get("name") == al.name) {
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
		f.albums[id] = &fldAlbum{owner: who(r), name: body.AlbumName, members: map[string]bool{}}
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
	return mux
}

// albumsOf says what albums bob has, by name, with how many photos each holds.
func (f *fldFake) albumsOf() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for _, al := range f.albums {
		out[al.name] += len(al.members)
	}
	return out
}

func fldSetup(t *testing.T) (*hub.Hub, *fldFake, *immich.API, *store.Store) {
	t.Helper()
	f := &fldFake{albums: map[string]*fldAlbum{}, keys: map[string][]string{}, secrets: map[string]string{}}
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

var root = hub.FolderSpec{Prefix: "/external/photos"}

func TestAFolderRuleOffersWhatIsThereAndMakesTheAlbumsOnlyWhenTheAnswerIsYes(t *testing.T) {
	h, f, sess, st := fldSetup(t)
	ctx := context.Background()
	f.photo("bob", "/external/photos/2025-02/paris/a.jpg", time.Hour)
	f.photo("bob", "/external/photos/2025-02/paris/b.jpg", time.Hour)
	f.photo("bob", "/external/photos/2025-02/rome/c.jpg", time.Hour)
	f.photo("bob", "/external/photos/loose.jpg", time.Hour)
	f.photo("bob", "/external/photos-old/2019/old.jpg", time.Hour)

	if _, err := h.FollowFolder(ctx, sess, bob, hub.FolderSpec{Prefix: "/nothing/here"}); !errors.Is(err, hub.ErrNoFolderPh) || !hub.IsUserError(err) {
		t.Fatalf("a folder with none of the person's photos: %v", err)
	}
	if len(f.keys) != 0 {
		t.Fatal("a refused rule makes no key")
	}
	r, err := h.FollowFolder(ctx, sess, bob, root)
	if err != nil || r.Kind != store.RuleKindFolder || r.Cond.Folder != "/external/photos" || r.Cond.Naming != hub.NameLast {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := h.FollowFolder(ctx, sess, bob, root); !errors.Is(err, hub.ErrFolderTwice) {
		t.Errorf("the same folder twice: %v", err)
	}
	k, ok, _ := st.FolderKey(ctx, bobID)
	if !ok {
		t.Fatal("the key was made")
	}
	var perms []string
	for _, p := range f.keys[k.KeyID.String()] {
		perms = append(perms, p)
	}
	sort.Strings(perms)
	if strings.Join(perms, " ") != "album.create album.read albumAsset.create asset.read user.read" {
		t.Errorf("the key can read the person's photos and make and fill albums, and nothing else: %v", perms)
	}

	n, err := h.LookRule(ctx, r)
	if err != nil || n != 3 {
		t.Fatalf("three photos are offered, the loose one and the sibling folder are not: %d %v", n, err)
	}
	if len(f.albumsOf()) != 0 {
		t.Fatal("nothing is made before the person says yes")
	}
	sg, _ := h.SuggestionOf(ctx, bobID, r.ID)
	why := map[string]int{}
	for _, p := range sg.Photos {
		why[p.Why]++
	}
	if why["paris"] != 2 || why["rome"] != 1 {
		t.Errorf("each photo says its album: %v", why)
	}
	if err := h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerSend, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.albumsOf(); len(got) != 2 || got["paris"] != 2 || got["rome"] != 1 {
		t.Fatalf("the albums, with their photos: %v", got)
	}
	if n, _ := h.LookRule(ctx, r); n != 0 {
		t.Errorf("what was answered is not offered again: %d", n)
	}
}

func TestADeclinedFolderIsLeftOutAndALaterPhotoIsOfferedWithTheRestAdditively(t *testing.T) {
	h, f, sess, st := fldSetup(t)
	ctx := context.Background()
	f.photo("bob", "/external/photos/paris/a.jpg", time.Hour)
	r, _ := h.FollowFolder(ctx, sess, bob, root)
	h.LookRule(ctx, r)
	if err := h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerDecline, nil); err != nil {
		t.Fatal(err)
	}
	if len(f.albumsOf()) != 0 {
		t.Fatal("Not these makes nothing")
	}
	// an album with that name already exists: the photos are added to it, nothing else is made
	f.mu.Lock()
	f.albums[uuid.NewString()] = &fldAlbum{owner: "bob", name: "paris", members: map[string]bool{uuid.NewString(): true}}
	f.mu.Unlock()
	r, _ = st.RuleByID(ctx, r.ID)
	f.photo("bob", "/external/photos/paris/b.jpg", 0)
	time.Sleep(5 * time.Millisecond)
	if n, _ := h.LookRule(ctx, r); n != 1 {
		t.Fatalf("the photo that came later is offered, the declined one is not: %d", n)
	}
	if err := h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerSend, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.albumsOf(); len(got) != 1 || got["paris"] != 2 {
		t.Errorf("the album that was there got the photo, and keeps what it had: %v", got)
	}
}

func TestAfterThreeAlwaysTheRuleMakesAlbumsByItselfForPhotosThatComeLater(t *testing.T) {
	h, f, sess, st := fldSetup(t)
	ctx := context.Background()
	f.photo("bob", "/external/photos/a/1.jpg", 2*time.Hour)
	r, _ := h.FollowFolder(ctx, sess, bob, root)
	if err := h.SetRuleAuto(ctx, bob, r.ID, true); !errors.Is(err, hub.ErrAutoTooSoon) {
		t.Fatalf("not yet: %v", err)
	}
	for i := 0; i < 3; i++ {
		r, _ = st.RuleByID(ctx, r.ID)
		h.LookRule(ctx, r)
		h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerAlways, nil)
		f.photo("bob", "/external/photos/a/more.jpg", 0)
		time.Sleep(5 * time.Millisecond)
	}
	if err := h.SetRuleAuto(ctx, bob, r.ID, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	fresh := f.photo("bob", "/external/photos/b/2.jpg", -2*time.Second) // Immich's times are to the second here: make it surely after the switch
	r, _ = st.RuleByID(ctx, r.ID)
	h.LookRule(ctx, r)
	if got := f.albumsOf(); got["b"] != 1 {
		t.Errorf("made without asking: %v", got)
	}
	sg, _ := h.SuggestionOf(ctx, bobID, r.ID)
	for _, p := range sg.Photos {
		if p.AssetID == fresh.id {
			t.Errorf("the new photo does not wait for an answer: %+v", p)
		}
	}
	if len(sg.Photos) != 1 || sg.Photos[0].Why != "a" {
		t.Errorf("what was waiting before the switch stays a suggestion: %+v", sg.Photos)
	}
}

func TestAFirstLookAtABigFolderIsSpreadOverPassesAndLaterPassesListOnlyWhatIsNew(t *testing.T) {
	h, f, sess, st := fldSetup(t)
	ctx := context.Background()
	old := hub.FolderPerPass
	hub.FolderPerPass = 2
	t.Cleanup(func() { hub.FolderPerPass = old })
	for i := 0; i < 5; i++ {
		f.photo("bob", "/external/photos/f/p"+string(rune('a'+i))+".jpg", time.Hour)
	}
	r, _ := h.FollowFolder(ctx, sess, bob, root)
	var total int
	for pass := 1; pass <= 3; pass++ {
		r, _ = st.RuleByID(ctx, r.ID)
		n, err := h.LookRule(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		total += n
		if want := map[int]int{1: 2, 2: 2, 3: 1}[pass]; n != want {
			t.Errorf("pass %d takes up %d photos, want %d", pass, n, want)
		}
	}
	if total != 5 {
		t.Fatalf("all five, once: %d", total)
	}
	r, _ = st.RuleByID(ctx, r.ID)
	if r.CaughtUp.IsZero() {
		t.Fatal("the first full look is done")
	}
	before := f.searches
	r, _ = st.RuleByID(ctx, r.ID)
	h.LookRule(ctx, r)
	if f.searches != before+1 {
		t.Errorf("one search per look: %d", f.searches-before)
	}
}

func TestEndingTheLastFolderRuleDeletesTheKeyAndOneStaysWhileAnotherRemains(t *testing.T) {
	h, f, sess, st := fldSetup(t)
	ctx := context.Background()
	f.photo("bob", "/external/photos/a/1.jpg", time.Hour)
	f.photo("bob", "/external/photos-old/b/2.jpg", time.Hour)
	r1, _ := h.FollowFolder(ctx, sess, bob, root)
	r2, err := h.FollowFolder(ctx, sess, bob, hub.FolderSpec{Prefix: "/external/photos-old"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.keys) != 1 {
		t.Fatalf("one key for both rules: %d", len(f.keys))
	}
	if err := h.DeleteRuleWith(ctx, sess, bob, r1.ID); err != nil || len(f.keys) != 1 {
		t.Fatalf("the key stays while a rule needs it: %v %d", err, len(f.keys))
	}
	if err := h.DeleteRuleWith(ctx, sess, bob, r2.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.keys) != 0 {
		t.Error("the key is deleted in the person's Immich")
	}
	if _, ok, _ := st.FolderKey(ctx, bobID); ok {
		t.Error("and forgotten")
	}
}

func TestAFolderRuleWhoseKeyIsGoneSaysSoAndLooksNoFurther(t *testing.T) {
	h, f, sess, st := fldSetup(t)
	ctx := context.Background()
	f.photo("bob", "/external/photos/a/1.jpg", time.Hour)
	r, _ := h.FollowFolder(ctx, sess, bob, root)
	k, _, _ := st.FolderKey(ctx, bobID)
	sess.DeleteAPIKey(ctx, k.KeyID) // bob deleted it in Immich
	if _, err := h.LookRule(ctx, r); err == nil {
		t.Fatal("the look fails")
	}
	got, _ := st.RuleByID(ctx, r.ID)
	if !strings.Contains(got.Problem, "your Immich does not accept") {
		t.Errorf("the problem is written on the rule: %q", got.Problem)
	}
}
