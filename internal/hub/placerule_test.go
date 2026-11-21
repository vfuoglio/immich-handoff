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

// plcFake is a small Immich with bob's photos (camera, time, maybe a place) and a PUT /assets that records the places written.
type plcFake struct {
	mu      sync.Mutex
	assets  []*plcAsset
	keys    map[string][]string
	secrets map[string]string
	writes  []string
	forget  bool // Immich says yes and keeps nothing (a photo of an external library)
}

type plcAsset struct {
	id, owner, kind, make, model string
	taken, created               time.Time
	lat, lon                     *float64
	trashed, archived            bool
}

var plcDay = time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC)

func (f *plcFake) photo(minutes int, make string, lat, lon *float64) *plcAsset {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := &plcAsset{id: uuid.NewString(), owner: bobID.String(), kind: "IMAGE", make: make, model: "R5", taken: plcDay.Add(time.Duration(minutes) * time.Minute), created: time.Now(), lat: lat, lon: lon}
	f.assets = append(f.assets, a)
	return a
}

func (f *plcFake) place(id string) (float64, float64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.assets {
		if a.id == id && a.lat != nil {
			return *a.lat, *a.lon, true
		}
	}
	return 0, 0, false
}

func (f *plcFake) handler() http.Handler {
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
			TakenAfter   *time.Time `json:"takenAfter"`
			TakenBefore  *time.Time `json:"takenBefore"`
			CreatedAfter *time.Time `json:"createdAfter"`
			Type         string     `json:"type"`
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
			if (body.TakenAfter != nil && a.taken.Before(*body.TakenAfter)) || (body.TakenBefore != nil && a.taken.After(*body.TakenBefore)) ||
				(body.CreatedAfter != nil && !a.created.After(*body.CreatedAfter)) || (body.Type != "" && a.kind != body.Type) {
				continue
			}
			exif := map[string]any{"dateTimeOriginal": a.taken.UTC().Format(time.RFC3339Nano), "make": a.make, "model": a.model}
			if a.lat != nil {
				exif["latitude"], exif["longitude"], exif["city"], exif["country"] = *a.lat, *a.lon, "Lisbon", "Portugal"
			}
			items = append(items, map[string]any{"id": a.id, "ownerId": a.owner, "createdAt": a.created.UTC().Format(time.RFC3339Nano), "fileCreatedAt": a.taken.UTC().Format(time.RFC3339Nano),
				"localDateTime": a.taken.UTC().Format(time.RFC3339Nano), "type": a.kind, "originalFileName": "x.jpg", "originalPath": "/p/x.jpg", "checksum": "x", "hasMetadata": true, "thumbhash": "abc",
				"fileModifiedAt": "2025-01-01T00:00:00Z", "isTrashed": a.trashed, "isArchived": a.archived, "exifInfo": exif})
		}
		j(w, 200, map[string]any{"albums": map[string]any{"count": 0, "facets": []any{}, "items": []any{}, "total": 0},
			"assets": map[string]any{"count": len(items), "facets": []any{}, "items": items, "total": len(items)}})
	})
	mux.HandleFunc("PUT /api/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Ids       []string `json:"ids"`
			Latitude  *float64 `json:"latitude"`
			Longitude *float64 `json:"longitude"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if !can(r, "asset.update") {
			http.Error(w, "no", 403)
			return
		}
		for _, id := range body.Ids {
			for _, a := range f.assets {
				if a.id == id && body.Latitude != nil && body.Longitude != nil {
					if !f.forget {
						a.lat, a.lon = body.Latitude, body.Longitude
					}
					f.writes = append(f.writes, id)
				}
			}
		}
		w.WriteHeader(204)
	})
	return mux
}

func plcSetup(t *testing.T) (*hub.Hub, *plcFake, *immich.API) {
	t.Helper()
	f := &plcFake{keys: map[string][]string{}, secrets: map[string]string{}}
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

var plcBob = immich.User{ID: bobID, Name: "Bob"}

func fp(v float64) *float64 { return &v }

func settle() { time.Sleep(8 * time.Millisecond) }

func TestAPlaceIsOfferedForAPhotoBetweenTwoLocatedOnesAndWrittenOnlyOnYes(t *testing.T) {
	h, f, sess := plcSetup(t)
	ctx := context.Background()
	r, err := h.FollowPlaces(ctx, sess, plcBob)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.FollowPlaces(ctx, sess, plcBob); !errors.Is(err, hub.ErrPlacesTwice) {
		t.Fatalf("one rule: %v", err)
	}
	before := f.photo(0, "Canon", fp(38.7223), fp(-9.1393))
	me := f.photo(3, "Canon", nil, nil)
	after := f.photo(10, "Canon", fp(38.7230), fp(-9.1400))
	alone := f.photo(300, "Canon", nil, nil) // hours from any located photo
	_ = after
	settle()
	n, err := h.LookRule(ctx, r)
	if err != nil || n != 1 {
		t.Fatalf("one photo is offered: %d %v", n, err)
	}
	if _, _, ok := f.place(me.id); ok {
		t.Fatal("a place was written before the answer")
	}
	if err := h.AnswerSuggestion(ctx, plcBob, r.ID, hub.AnswerSend, nil); err != nil {
		t.Fatal(err)
	}
	lat, lon, ok := f.place(me.id)
	if !ok || lat != 38.7223 || lon != -9.1393 {
		t.Fatalf("the nearer neighbour's place: %v %v %v", lat, lon, ok)
	}
	if _, _, ok := f.place(alone.id); ok {
		t.Fatal("a photo with no neighbour near got a place")
	}
	if n, _ := h.LookRule(ctx, r); n != 0 {
		t.Fatalf("it is not offered again: %d", n)
	}
	_ = before
	// a declined photo is not offered again
	d := f.photo(5, "Canon", nil, nil)
	settle()
	if n, _ := h.LookRule(ctx, r); n != 1 {
		t.Fatalf("a new photo: %d", n)
	}
	if err := h.AnswerSuggestion(ctx, plcBob, r.ID, hub.AnswerDecline, nil); err != nil {
		t.Fatal(err)
	}
	if n, _ := h.LookRule(ctx, r); n != 0 {
		t.Fatalf("declined, not again: %d", n)
	}
	if _, _, ok := f.place(d.id); ok {
		t.Fatal("a declined photo got a place")
	}
}

func TestPhotosThatAreNotTheirsOrAreHiddenOrNotPhotosOrHaveNoCameraAreLeftAlone(t *testing.T) {
	h, f, sess := plcSetup(t)
	ctx := context.Background()
	r, _ := h.FollowPlaces(ctx, sess, plcBob)
	f.photo(0, "Canon", fp(38.72), fp(-9.14))
	f.photo(6, "Canon", fp(38.7201), fp(-9.1401))
	partner := f.photo(2, "Canon", nil, nil)
	partner.owner = uuid.NewString()
	trashed := f.photo(2, "Canon", nil, nil)
	trashed.trashed = true
	archived := f.photo(3, "Canon", nil, nil)
	archived.archived = true
	video := f.photo(3, "Canon", nil, nil)
	video.kind = "VIDEO"
	f.photo(4, "", nil, nil) // no camera
	ok := f.photo(4, "Canon", nil, nil)
	settle()
	if n, err := h.LookRule(ctx, r); err != nil || n != 1 {
		t.Fatalf("only the photo of theirs with a camera is offered: %d %v", n, err)
	}
	if err := h.AnswerSuggestion(ctx, plcBob, r.ID, hub.AnswerSend, nil); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 1 || f.writes[0] != ok.id {
		t.Fatalf("one write, to that photo: %v", f.writes)
	}
}

func TestPlacesAreWrittenByThemselvesOnlyAfterThreeAlwaysOnlyForTwoSidedGuessesAndOnlyForLaterPhotos(t *testing.T) {
	h, f, sess := plcSetup(t)
	ctx := context.Background()
	r, _ := h.FollowPlaces(ctx, sess, plcBob)
	if err := h.SetRuleAuto(ctx, plcBob, r.ID, true); !errors.Is(err, hub.ErrAutoTooSoon) {
		t.Fatalf("auto at once: %v", err)
	}
	f.photo(0, "Canon", fp(38.7), fp(-9.1))
	for i := 0; i < 3; i++ {
		f.photo(1+i, "Canon", nil, nil)
		settle()
		h.LookRule(ctx, r)
		if err := h.AnswerSuggestion(ctx, plcBob, r.ID, hub.AnswerAlways, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.SetRuleAuto(ctx, plcBob, r.ID, true); err != nil {
		t.Fatal(err)
	}
	r, _ = h.Store.RuleByID(ctx, r.ID)
	time.Sleep(10 * time.Millisecond)
	// two-sided and one-sided photos arriving after the switch
	f.photo(60, "Canon", fp(38.7005), fp(-9.1005)) // a located photo an hour on
	two := f.photo(55, "Canon", nil, nil)          // 5 minutes before the located photo at minute 60, and 55 after the one at minute 0: one side only
	one := f.photo(8, "Canon", nil, nil)           // 8 minutes after the first located photo, the next located one is 52 minutes on: one side only
	time.Sleep(60 * time.Millisecond)
	if _, err := h.LookRule(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := f.place(one.id); ok {
		t.Fatal("a one-sided guess was written without asking")
	}
	if _, _, ok := f.place(two.id); ok {
		t.Fatal("a photo with a located photo on one side only was written without asking") // minute 55 is 55 minutes from the first located photo and 5 from the second
	}
	// a real two-sided one
	f.photo(100, "Canon", fp(38.7010), fp(-9.1010))
	both := f.photo(104, "Canon", nil, nil)
	f.photo(108, "Canon", fp(38.7011), fp(-9.1011))
	time.Sleep(60 * time.Millisecond)
	if _, err := h.LookRule(ctx, r); err != nil {
		t.Fatal(err)
	}
	lat, _, ok := f.place(both.id)
	if !ok || lat != 38.7011 && lat != 38.7010 {
		t.Fatalf("a two-sided guess is written by itself after the switch: %v %v", lat, ok)
	}
	if _, _, ok := f.place(one.id); ok {
		t.Fatal("the one-sided guess is still waiting for an answer")
	}
}

func TestOlderPhotosAreOfferedOnRequestAndTheKeyGoesWithTheRule(t *testing.T) {
	h, f, sess := plcSetup(t)
	ctx := context.Background()
	f.photo(0, "Canon", fp(38.7), fp(-9.1))
	old := f.photo(4, "Canon", nil, nil)
	settle()
	r, _ := h.FollowPlaces(ctx, sess, plcBob) // the photos were there before the rule
	if n, _ := h.LookRule(ctx, r); n != 0 {
		t.Fatalf("a rule looks at what comes after it: %d", n)
	}
	if n, err := h.LookBack(ctx, plcBob, r.ID); err != nil || n != 1 {
		t.Fatalf("look back: %d %v", n, err)
	}
	if _, _, ok := f.place(old.id); ok {
		t.Fatal("the older photo got a place without a yes")
	}
	if perms := f.keys; len(perms) != 1 {
		t.Fatalf("one key: %v", perms)
	}
	for _, p := range f.keys {
		if strings.Join(p, ",") != "user.read,asset.read,asset.update" {
			t.Fatalf("the key's permissions: %v", p)
		}
	}
	if err := h.DeleteRuleWith(ctx, sess, plcBob, r.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.keys) != 0 {
		t.Fatal("the key goes with the rule")
	}
}

func TestARuleSaysWhenImmichDidNotKeepAPlaceItWrote(t *testing.T) {
	h, f, sess := plcSetup(t)
	ctx := context.Background()
	r, _ := h.FollowPlaces(ctx, sess, plcBob)
	f.photo(0, "Canon", fp(38.7), fp(-9.1))
	me := f.photo(3, "Canon", nil, nil)
	f.forget = true
	settle()
	h.LookRule(ctx, r)
	if err := h.AnswerSuggestion(ctx, plcBob, r.ID, hub.AnswerSend, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := f.place(me.id); ok {
		t.Fatal("the fake keeps nothing")
	}
	h.LookRule(ctx, r)
	got, _ := h.Store.RuleByID(ctx, r.ID)
	if !strings.Contains(got.Problem, "did not keep the place written into 1 photos") {
		t.Fatalf("the rule says it: %q", got.Problem)
	}
	f.forget = false
	f.photo(6, "Canon", fp(38.7), fp(-9.1))
	f.mu.Lock()
	f.assets[len(f.assets)-1].lat = nil // not located: the new photo is a candidate, so the look runs fully
	f.mu.Unlock()
	if n, _ := h.LookRule(ctx, r); n < 0 {
		t.Fatal("a look")
	}
}
