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

// stkFake is a small Immich with a person's photos (names, EXIF, stacks) and the calls stacking makes.
type stkFake struct {
	mu       sync.Mutex
	photos   []*stkPhoto
	stacks   map[string][]string // stack id -> photos, the cover first
	keys     map[string][]string
	secrets  map[string]string
	searches int
	refuse   bool // creating a stack answers 400
}

type stkPhoto struct {
	id, path, make, model string
	taken, created        time.Time
	stack                 string
}

func (f *stkFake) add(path string, taken, created time.Time, cam ...string) *stkPhoto {
	p := &stkPhoto{id: uuid.NewString(), path: path, taken: taken, created: created}
	if len(cam) == 2 {
		p.make, p.model = cam[0], cam[1]
	}
	f.photos = append(f.photos, p)
	return p
}

func (f *stkFake) stackOf(p *stkPhoto) string { return p.stack }

func (f *stkFake) handler() http.Handler {
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
	mux.HandleFunc("POST /api/search/metadata", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OriginalPath string     `json:"originalPath"`
			CreatedAfter *time.Time `json:"createdAfter"`
			WithStacked  *bool      `json:"withStacked"`
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
		for _, p := range f.photos {
			if !strings.Contains(p.path, body.OriginalPath) || (body.CreatedAfter != nil && !p.created.After(*body.CreatedAfter)) {
				continue
			}
			if body.WithStacked != nil && !*body.WithStacked && p.stack != "" { // a search that does not include stacked photos leaves them all out
				continue
			}
			exif := map[string]any{"dateTimeOriginal": p.taken.UTC().Format(time.RFC3339)}
			if p.make != "" {
				exif["make"], exif["model"] = p.make, p.model
			}
			items = append(items, map[string]any{"id": p.id, "ownerId": bobID.String(), "originalPath": p.path, "isTrashed": false, "createdAt": p.created.UTC().Format(time.RFC3339),
				"type": "IMAGE", "originalFileName": filepath.Base(p.path), "checksum": "x", "hasMetadata": true, "fileCreatedAt": p.taken.UTC().Format(time.RFC3339),
				"fileModifiedAt": "2025-01-01T00:00:00Z", "exifInfo": exif})
		}
		j(w, 200, map[string]any{"albums": map[string]any{"count": 0, "facets": []any{}, "items": []any{}, "total": 0},
			"assets": map[string]any{"count": len(items), "facets": []any{}, "items": items, "total": len(items)}})
	})
	mux.HandleFunc("POST /api/stacks", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AssetIds []string `json:"assetIds"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if !can(r, "stack.create") {
			http.Error(w, "no", 403)
			return
		}
		if f.refuse {
			http.Error(w, "no", 400)
			return
		}
		id := uuid.NewString()
		f.stacks[id] = body.AssetIds
		assets := []any{}
		for _, p := range f.photos {
			for _, a := range body.AssetIds {
				if p.id == a {
					p.stack = id
					assets = append(assets, map[string]any{"id": a, "ownerId": bobID.String(), "originalPath": p.path, "type": "IMAGE", "createdAt": "2025-01-01T00:00:00Z", "fileCreatedAt": "2025-01-01T00:00:00Z", "fileModifiedAt": "2025-01-01T00:00:00Z", "localDateTime": "2025-01-01T00:00:00Z", "originalFileName": "x", "checksum": "x"})
				}
			}
		}
		j(w, 201, map[string]any{"id": id, "primaryAssetId": body.AssetIds[0], "assets": assets})
	})
	mux.HandleFunc("GET /api/stacks/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !can(r, "stack.read") {
			http.Error(w, "no", 403)
			return
		}
		ids, ok := f.stacks[r.PathValue("id")]
		if !ok {
			http.Error(w, "no", 404)
			return
		}
		assets := []any{}
		for _, a := range ids {
			assets = append(assets, map[string]any{"id": a, "ownerId": bobID.String(), "type": "IMAGE", "createdAt": "2025-01-01T00:00:00Z", "fileCreatedAt": "2025-01-01T00:00:00Z", "fileModifiedAt": "2025-01-01T00:00:00Z", "localDateTime": "2025-01-01T00:00:00Z", "originalFileName": "x", "originalPath": "x", "checksum": "x"})
		}
		j(w, 200, map[string]any{"id": r.PathValue("id"), "primaryAssetId": ids[0], "assets": assets})
	})
	mux.HandleFunc("DELETE /api/stacks/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !can(r, "stack.delete") {
			http.Error(w, "no", 403)
			return
		}
		id := r.PathValue("id")
		if _, ok := f.stacks[id]; !ok {
			http.Error(w, "no", 404)
			return
		}
		delete(f.stacks, id)
		for _, p := range f.photos {
			if p.stack == id {
				p.stack = ""
			}
		}
		w.WriteHeader(204)
	})
	return mux
}

func (f *stkFake) stackNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	byID := map[string]*stkPhoto{}
	for _, p := range f.photos {
		byID[p.id] = p
	}
	var out []string
	for _, ids := range f.stacks {
		var names []string
		for _, id := range ids {
			names = append(names, filepath.Base(byID[id].path))
		}
		out = append(out, strings.Join(names, ","))
	}
	sort.Strings(out)
	return out
}

func stkSetup(t *testing.T) (*hub.Hub, *stkFake, *immich.API, *store.Store, *time.Time) {
	t.Helper()
	f := &stkFake{stacks: map[string][]string{}, keys: map[string][]string{}, secrets: map[string]string{}}
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
	clk := time.Now()
	h := &hub.Hub{Store: st, Connect: connect, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return clk }}
	return h, f, sess, st, &clk
}

var allSignals = hub.StackRuleParams{Pair: true, Edit: true, Burst: true}

// a library: a pair, an edit and a burst
func (f *stkFake) library(now time.Time) {
	t := time.Date(2025, 7, 4, 12, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	f.add("/lib/trip/IMG_1.CR2", t, old, "Canon", "R5")
	f.add("/lib/trip/IMG_1.JPG", t, old, "Canon", "R5")
	f.add("/lib/trip/IMG_2.jpg", t.Add(time.Minute), old, "Canon", "R5")
	f.add("/lib/trip/IMG_2-edited.jpg", t.Add(time.Minute), old, "Canon", "R5")
	for i := 0; i < 3; i++ {
		f.add("/lib/burst/DSC_010"+string(rune('1'+i))+".jpg", t.Add(time.Hour+time.Duration(i)*time.Second), old, "Nikon", "Z6")
	}
	f.add("/lib/alone/IMG_9.jpg", t.Add(3*time.Hour), old, "Canon", "R5")
}

func TestAStackingRuleOffersTheGroupsAndStacksOnlyWhenTheAnswerIsYesWithTheCoverFirst(t *testing.T) {
	h, f, sess, st, clk := stkSetup(t)
	ctx := context.Background()
	f.library(*clk)

	for name, p := range map[string]hub.StackRuleParams{"no signal": {}, "bad cover": {Pair: true, Cover: "middle"}, "bad folder": {Pair: true, Folder: "lib"}} {
		if _, err := h.FollowStacking(ctx, sess, bob, p); err == nil || !hub.IsUserError(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(f.keys) != 0 {
		t.Fatal("a refused rule makes no key")
	}
	r, err := h.FollowStacking(ctx, sess, bob, hub.StackRuleParams{Pair: true, Edit: true, Burst: true, Folder: "/lib/"})
	if err != nil || r.Kind != store.RuleKindStack || r.Cond.Folder != "/lib" || strings.Join(r.Cond.Signals, ",") != "pair,edit,burst" || r.Cond.Cover != hub.CoverJPEG {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := h.FollowStacking(ctx, sess, bob, hub.StackRuleParams{Pair: true, Edit: true, Burst: true, Folder: "/lib"}); !errors.Is(err, hub.ErrStackTwice) {
		t.Errorf("the same twice: %v", err)
	}
	k, ok, _ := st.StackKey(ctx, bobID)
	if !ok {
		t.Fatal("the key was made")
	}
	perms := append([]string(nil), f.keys[k.KeyID.String()]...)
	sort.Strings(perms)
	if strings.Join(perms, " ") != "asset.read stack.create stack.delete stack.read stack.update user.read" {
		t.Errorf("the key can search, stack and unstack, and nothing else: %v", perms)
	}

	n, err := h.LookRule(ctx, r)
	if err != nil || n != 3 {
		t.Fatalf("three groups: %d %v", n, err)
	}
	if len(f.stackNames()) != 0 {
		t.Fatal("nothing is stacked before the person says yes")
	}
	if err := h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerSend, nil); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(f.stackNames(), " | ")
	if !strings.Contains(got, "IMG_1.JPG,IMG_1.CR2") || !strings.Contains(got, "IMG_2.jpg,IMG_2-edited.jpg") || !strings.Contains(got, "DSC_0101.jpg,DSC_0102.jpg,DSC_0103.jpg") || len(f.stackNames()) != 3 {
		t.Errorf("three stacks, the JPEG as the cover of the pair: %s", got)
	}
	if n, _ := h.LookRule(ctx, r); n != 0 {
		t.Errorf("what was answered is not offered again: %d", n)
	}
}

func TestAnswersNotTheseAndTheCoverRawAndAPhotoAlreadyInAStackIsLeftAlone(t *testing.T) {
	h, f, sess, st, clk := stkSetup(t)
	ctx := context.Background()
	f.library(*clk)
	// the person stacked the pair by hand
	pair := []string{}
	for _, p := range f.photos {
		if strings.Contains(p.path, "IMG_1.") {
			pair = append(pair, p.id)
		}
	}
	by := uuid.NewString()
	f.stacks[by] = pair
	for _, p := range f.photos {
		if strings.Contains(p.path, "IMG_1.") {
			p.stack = by
		}
	}
	r, _ := h.FollowStacking(ctx, sess, bob, hub.StackRuleParams{Pair: true, Edit: true, Cover: hub.CoverRAW})
	if n, _ := h.LookRule(ctx, r); n != 1 {
		t.Fatalf("only the edit: the pair is already stacked, the burst was not asked for: %d", n)
	}
	if err := h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerDecline, nil); err != nil {
		t.Fatal(err)
	}
	if len(f.stackNames()) != 1 {
		t.Errorf("Not these stacks nothing: %v", f.stackNames())
	}
	r, _ = st.RuleByID(ctx, r.ID)
	if n, _ := h.LookRule(ctx, r); n != 0 {
		t.Errorf("a group that was declined is not offered again: %d", n)
	}
}

func TestAfterThreeAlwaysPairsAndEditsGoByThemselvesAndBurstsAlwaysAsk(t *testing.T) {
	h, f, sess, st, clk := stkSetup(t)
	ctx := context.Background()
	r, _ := h.FollowStacking(ctx, sess, bob, allSignals)
	if err := h.SetRuleAuto(ctx, bob, r.ID, true); !errors.Is(err, hub.ErrAutoTooSoon) {
		t.Fatalf("not yet: %v", err)
	}
	t0 := time.Date(2025, 8, 1, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		f.add("/lib/a/P"+string(rune('0'+i))+".CR2", t0.Add(time.Duration(i)*time.Hour), clk.Add(-time.Minute))
		f.add("/lib/a/P"+string(rune('0'+i))+".JPG", t0.Add(time.Duration(i)*time.Hour), clk.Add(-time.Minute))
		r, _ = st.RuleByID(ctx, r.ID)
		h.LookRule(ctx, r)
		if err := h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerAlways, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.SetRuleAuto(ctx, bob, r.ID, true); err != nil {
		t.Fatal(err)
	}
	*clk = clk.Add(time.Hour)
	// a new pair and a new burst arrive after the switch
	f.add("/lib/b/N1.CR2", t0.Add(10*time.Hour), clk.Add(time.Minute))
	f.add("/lib/b/N1.JPG", t0.Add(10*time.Hour), clk.Add(time.Minute))
	for i := 0; i < 3; i++ {
		f.add("/lib/c/B"+string(rune('1'+i))+".jpg", t0.Add(12*time.Hour+time.Duration(i)*time.Second), clk.Add(time.Minute), "Sony", "A7")
	}
	r, _ = st.RuleByID(ctx, r.ID)
	h.LookRule(ctx, r)
	names := strings.Join(f.stackNames(), " | ")
	if !strings.Contains(names, "N1.JPG,N1.CR2") || strings.Contains(names, "B1.jpg") {
		t.Errorf("the pair was stacked without asking, the burst was not: %s", names)
	}
	sg, _ := h.SuggestionOf(ctx, bobID, r.ID)
	if len(sg.Photos) != 3 || !strings.HasPrefix(sg.Photos[0].Why, "burst:") {
		t.Errorf("the burst waits for an answer: %+v", sg.Photos)
	}
}

func TestARuleThatOnlyStacksBurstsNeverGoesByItself(t *testing.T) {
	h, _, sess, st, _ := stkSetup(t)
	ctx := context.Background()
	r, _ := h.FollowStacking(ctx, sess, bob, hub.StackRuleParams{Burst: true})
	for i := 0; i < 3; i++ {
		st.AddRuleAlways(ctx, r.ID)
	}
	r, _ = st.RuleByID(ctx, r.ID)
	if hub.CanAuto(r) {
		t.Error("the page must not offer it")
	}
	if err := h.SetRuleAuto(ctx, bob, r.ID, true); !errors.Is(err, hub.ErrBurstOnly) || !hub.IsUserError(err) {
		t.Errorf("%v", err)
	}
}

func TestAStackTheAppMadeCanBeTakenApartForFifteenMinutesAndOnlyIfNobodyChangedIt(t *testing.T) {
	h, f, sess, st, clk := stkSetup(t)
	ctx := context.Background()
	f.library(*clk)
	r, _ := h.FollowStacking(ctx, sess, bob, allSignals)
	h.LookRule(ctx, r)
	if err := h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerSend, nil); err != nil {
		t.Fatal(err)
	}
	if h.StackUndoable(ctx, bobID, r.ID) != 3 {
		t.Fatalf("three stacks to take apart: %d", h.StackUndoable(ctx, bobID, r.ID))
	}
	// the person adds a photo to the pair's stack by hand: it is theirs now
	var pairStack, extra string
	for id, ids := range f.stacks {
		if len(ids) == 2 && strings.Contains(f.pathOf(ids[0]), "IMG_1.") {
			pairStack = id
		}
	}
	extra = f.photos[len(f.photos)-1].id // the lone photo
	f.stacks[pairStack] = append(f.stacks[pairStack], extra)
	n, err := h.UndoStacks(ctx, bob, r.ID)
	if err != nil || n != 2 {
		t.Fatalf("the edit and the burst are taken apart, the changed stack is left: %d %v", n, err)
	}
	if got := f.stackNames(); len(got) != 1 || !strings.Contains(got[0], "IMG_1.JPG") {
		t.Errorf("only the stack the person changed remains: %v", got)
	}
	if _, err := h.UndoStacks(ctx, bob, r.ID); !errors.Is(err, hub.ErrStackNothing) {
		t.Errorf("nothing left to undo: %v", err)
	}
	// what was taken apart is not offered again
	r, _ = st.RuleByID(ctx, r.ID)
	if n, _ := h.LookRule(ctx, r); n != 0 {
		t.Errorf("not offered again: %d", n)
	}
	// after the window nothing can be undone
	h2, f2, sess2, _, clk2 := stkSetup(t)
	f2.library(*clk2)
	r2, _ := h2.FollowStacking(ctx, sess2, bob, hub.StackRuleParams{Pair: true})
	h2.LookRule(ctx, r2)
	h2.AnswerSuggestion(ctx, bob, r2.ID, hub.AnswerSend, nil)
	*clk2 = clk2.Add(16 * time.Minute)
	if _, err := h2.UndoStacks(ctx, bob, r2.ID); !errors.Is(err, hub.ErrStackNothing) {
		t.Errorf("after 15 minutes: %v", err)
	}
	if len(f2.stackNames()) != 1 {
		t.Error("the stack stays")
	}
}

func (f *stkFake) pathOf(id string) string {
	for _, p := range f.photos {
		if p.id == id {
			return p.path
		}
	}
	return ""
}

func TestALaterPartnerIsPairedWithAnOlderPhotoAndALaterLookListsOnlyWhatIsNew(t *testing.T) {
	h, f, sess, st, clk := stkSetup(t)
	ctx := context.Background()
	t0 := time.Date(2025, 8, 1, 10, 0, 0, 0, time.UTC)
	f.add("/lib/x/IMG_5.JPG", t0, clk.Add(-2*time.Hour)) // the JPEG came first
	r, _ := h.FollowStacking(ctx, sess, bob, hub.StackRuleParams{Pair: true})
	h.LookRule(ctx, r) // the first, full look: nothing to pair yet
	r, _ = st.RuleByID(ctx, r.ID)
	if r.CaughtUp.IsZero() {
		t.Fatal("the first look is done")
	}
	*clk = clk.Add(time.Hour)
	f.add("/lib/x/IMG_5.CR2", t0, clk.Add(-time.Minute)) // the RAW arrives an hour later
	for i := 0; i < 5; i++ {
		f.add("/lib/y/F"+string(rune('a'+i))+".jpg", t0.Add(time.Duration(i)*time.Hour), clk.Add(-2*time.Hour))
	}
	before := f.searches
	n, err := h.LookRule(ctx, r)
	if err != nil || n != 1 {
		t.Fatalf("the late RAW finds its JPEG: %d %v", n, err)
	}
	if got := f.searches - before; got > 8 {
		t.Errorf("a later look lists what is new and the folder of each new photo, not the whole library: %d searches", got)
	}
}

func TestStackingInPiecesTheFirstLookSpreadsOverPassesAndAnImmichThatRefusesLeavesTheGroup(t *testing.T) {
	h, f, sess, st, clk := stkSetup(t)
	ctx := context.Background()
	old := hub.StackPerPass
	hub.StackPerPass = 4
	t.Cleanup(func() { hub.StackPerPass = old })
	t0 := time.Date(2025, 8, 1, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ { // five pairs, ten photos
		n := string(rune('a' + i))
		f.add("/lib/"+n+"/IMG.CR2", t0.Add(time.Duration(i)*time.Hour), clk.Add(-time.Hour))
		f.add("/lib/"+n+"/IMG.JPG", t0.Add(time.Duration(i)*time.Hour), clk.Add(-time.Hour))
	}
	r, _ := h.FollowStacking(ctx, sess, bob, hub.StackRuleParams{Pair: true})
	total := 0
	for pass := 0; pass < 4; pass++ {
		r, _ = st.RuleByID(ctx, r.ID)
		n, err := h.LookRule(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		total += n
	}
	if total != 5 {
		t.Fatalf("all five groups, once, over passes of at most four photos: %d", total)
	}
	f.refuse = true
	if err := h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerSend, nil); err == nil {
		t.Fatal("Immich refused: the answer says so")
	}
	if sg, _ := h.SuggestionOf(ctx, bobID, r.ID); len(sg.Photos) != 10 {
		t.Errorf("the groups stay as suggestions: %d photos", len(sg.Photos))
	}
	f.refuse = false
	if err := h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerSend, nil); err != nil || len(f.stackNames()) != 5 {
		t.Errorf("and can be answered later: %v %v", err, f.stackNames())
	}
}

func TestEndingTheLastStackingRuleDeletesTheKey(t *testing.T) {
	h, f, sess, st, _ := stkSetup(t)
	ctx := context.Background()
	r1, _ := h.FollowStacking(ctx, sess, bob, hub.StackRuleParams{Pair: true})
	r2, _ := h.FollowStacking(ctx, sess, bob, hub.StackRuleParams{Edit: true})
	if len(f.keys) != 1 {
		t.Fatalf("one key for both: %d", len(f.keys))
	}
	if err := h.DeleteRuleWith(ctx, sess, bob, r1.ID); err != nil || len(f.keys) != 1 {
		t.Fatalf("it stays while a rule needs it: %v %d", err, len(f.keys))
	}
	if err := h.DeleteRuleWith(ctx, sess, bob, r2.ID); err != nil || len(f.keys) != 0 {
		t.Fatalf("%v %d", err, len(f.keys))
	}
	if _, ok, _ := st.StackKey(ctx, bobID); ok {
		t.Error("and forgotten")
	}
}
