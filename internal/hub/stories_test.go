package hub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	imgjpeg "image/jpeg"
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

// texturedJPEG is a picture full of detail that differs from seed to seed, so the quality check keeps it and tells it from the others.
func storyJPEG(seed uint32) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 320, 240))
	state := seed*2654435761 + 1
	for y := 0; y < 240; y += 3 {
		for x := 0; x < 320; x += 3 {
			state = state*1664525 + 1013904223
			v := uint8(60 + (state>>16)%140)
			for dy := 0; dy < 3; dy++ {
				for dx := 0; dx < 3; dx++ {
					img.Set(x+dx, y+dy, color.RGBA{v, v, v, 255})
				}
			}
		}
	}
	var b bytes.Buffer
	imgjpeg.Encode(&b, img, &imgjpeg.Options{Quality: 90})
	return b.Bytes()
}

type storyAsset struct {
	id      string
	at      time.Time
	city    string
	person  string
	seed    uint32
	hidden  bool
	trashed bool
}

// storyFake is a small Immich with bob's photos (with place and people), albums, people and keys.
type storyFake struct {
	mu      sync.Mutex
	assets  []*storyAsset
	albums  map[string]map[string]bool
	named   map[string]string
	people  map[string]string
	keys    map[string][]string
	secrets map[string]string
	n       uint32
}

func (f *storyFake) photo(day, city, person string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	t, _ := time.Parse("2006-01-02", day)
	a := &storyAsset{id: uuid.NewString(), at: t.Add(11 * time.Hour), city: city, person: person, seed: f.n}
	f.assets = append(f.assets, a)
	return a.id
}

func (f *storyFake) album(name string, ids ...string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := uuid.NewString()
	f.named[id], f.albums[id] = name, map[string]bool{}
	for _, a := range ids {
		f.albums[id][a] = true
	}
	return id
}

func (f *storyFake) keyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.keys)
}

func (f *storyFake) handler() http.Handler {
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
	item := func(a *storyAsset) map[string]any {
		m := map[string]any{"id": a.id, "ownerId": bobID.String(), "createdAt": a.at.UTC().Format(time.RFC3339Nano), "fileCreatedAt": a.at.UTC().Format(time.RFC3339Nano),
			"localDateTime": a.at.UTC().Format(time.RFC3339Nano), "type": "IMAGE", "originalFileName": "x.jpg", "originalPath": "/p/x.jpg", "checksum": "x", "hasMetadata": true,
			"fileModifiedAt": "2025-01-01T00:00:00Z", "isTrashed": a.trashed, "isArchived": false, "isFavorite": false,
			"exifInfo": map[string]any{"city": a.city, "country": "Portugal", "latitude": 38.72, "longitude": -9.14}}
		if a.person != "" {
			m["people"] = []any{map[string]any{"id": uuid.NewString(), "name": a.person}}
		}
		return m
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
			AlbumIds    []string   `json:"albumIds"`
			TakenAfter  *time.Time `json:"takenAfter"`
			TakenBefore *time.Time `json:"takenBefore"`
			WithExif    bool       `json:"withExif"`
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
			if (body.TakenAfter != nil && a.at.Before(*body.TakenAfter)) || (body.TakenBefore != nil && a.at.After(*body.TakenBefore)) {
				continue
			}
			items = append(items, item(a))
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
		for _, a := range f.assets {
			if a.id == r.PathValue("id") {
				w.Header().Set("Content-Type", "image/jpeg")
				w.Write(storyJPEG(a.seed))
				return
			}
		}
		http.NotFound(w, r)
	})
	return mux
}

func storySetup(t *testing.T) (*hub.Hub, *storyFake, *immich.API) {
	t.Helper()
	f := &storyFake{albums: map[string]map[string]bool{}, named: map[string]string{}, people: map[string]string{}, keys: map[string][]string{}, secrets: map[string]string{}}
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
	h := &hub.Hub{Store: st, Connect: connect, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), RulesSettle: time.Millisecond, AppURL: "https://photos.example.test"}
	return h, f, sess
}

func waitStory(t *testing.T, h *hub.Hub, id string) store.Story {
	t.Helper()
	var s store.Story
	for i := 0; i < 200; i++ {
		var err error
		if s, err = h.Store.StoryByID(context.Background(), bobID, id); err == nil && s.State != store.StoryBuilding {
			return s
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the story was not built: %+v", s)
	return s
}

var storyBob = immich.User{ID: bobID, Name: "Bob"}

// holidayAlbum is an album of photos in three places over a week, with a named person in some.
func holidayAlbum(f *storyFake) string {
	var ids []string
	for d := 1; d <= 6; d++ {
		for k := 0; k < 4; k++ {
			city, person := "Lisbon", ""
			if d > 3 {
				city = "Porto"
			}
			if k == 0 {
				person = "Nora"
			}
			ids = append(ids, f.photo(fmt.Sprintf("2026-08-%02d", d), city, person))
		}
	}
	return f.album("Portugal", ids...)
}

func TestAStoryIsBuiltFromAnAlbumWithChaptersAndPublicOnlyWhatTheSwitchesAllow(t *testing.T) {
	h, f, sess := storySetup(t)
	ctx := context.Background()
	album := holidayAlbum(f)
	s, err := h.CreateStory(ctx, sess, storyBob, hub.StoryParams{Kind: store.StoryAlbum, Source: uuid.MustParse(album), EndsInDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "Portugal" || len(s.Token) != 64 || s.EndsAt.IsZero() {
		t.Fatalf("a new story: %+v", s)
	}
	if _, err := h.StoryFor(ctx, s.Token); !errors.Is(err, hub.ErrDropGone) {
		t.Fatalf("a story still being made has no public page: %v", err)
	}
	s = waitStory(t, h, s.ID)
	if s.State != store.StoryReady || len(s.Picks) == 0 || len(s.Picks) > 24 || len(s.Chapters) < 2 {
		t.Fatalf("built: %s %q %d picks %d chapters", s.State, s.Problem, len(s.Picks), len(s.Chapters))
	}
	if s.Chapters[0].Title != "Lisbon" || s.Chapters[len(s.Chapters)-1].Title != "Porto" {
		t.Fatalf("a week in two cities is cut by place: %+v", s.Chapters)
	}
	pub, err := h.StoryFor(ctx, s.Token)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := json.Marshal(hub.StoryDocOf(pub))
	text := string(doc)
	if strings.Contains(text, "38.72") || strings.Contains(text, "-9.14") || strings.Contains(text, "atitude") {
		t.Errorf("coordinates are never in the page: %s", text)
	}
	if strings.Contains(text, "Nora") {
		t.Errorf("names are off by default: %s", text)
	}
	for _, p := range pub.Picks {
		if strings.Contains(text, p.Asset) {
			t.Fatalf("an Immich id is in the page: %s", p.Asset)
		}
	}
	if !strings.Contains(text, "Lisbon, Portugal") || !strings.Contains(text, "2026-08-01") {
		t.Errorf("dates and places are on by default: %s", text)
	}
	// the switches
	if err := h.EditStory(ctx, storyBob, s.ID, hub.StoryEdit{Title: "Summer in Portugal", ShowDates: false, ShowPlaces: false, ShowPeople: true, EndsInDays: -1}); err != nil {
		t.Fatal(err)
	}
	pub, _ = h.StoryFor(ctx, s.Token)
	doc, _ = json.Marshal(hub.StoryDocOf(pub))
	text = string(doc)
	if strings.Contains(text, "2026-08") || strings.Contains(text, "Lisbon, Portugal") || !strings.Contains(text, "Nora") || !strings.Contains(text, "Summer in Portugal") {
		t.Errorf("with dates and places off and names on: %s", text)
	}
}

func TestAStoryPageGivesPicturesByOpaqueIdOnlyAndTheCoverOnlyWhenAllowed(t *testing.T) {
	h, f, sess := storySetup(t)
	ctx := context.Background()
	album := holidayAlbum(f)
	s, err := h.CreateStory(ctx, sess, storyBob, hub.StoryParams{Kind: store.StoryAlbum, Source: uuid.MustParse(album)})
	if err != nil {
		t.Fatal(err)
	}
	s = waitStory(t, h, s.ID)
	doc := hub.StoryDocOf(s)
	if data, kind, err := h.StoryImage(ctx, s.Token, doc.Scenes[0].ID); err != nil || kind != "image/jpeg" || len(data) < 100 {
		t.Fatalf("a picture: %v %q", err, kind)
	}
	if _, _, err := h.StoryImage(ctx, s.Token, s.Picks[0].Asset); !errors.Is(err, hub.ErrDropGone) {
		t.Fatalf("an Immich id is not a way in: %v", err)
	}
	if _, _, err := h.StoryImage(ctx, strings.Repeat("0", 64), doc.Scenes[0].ID); !errors.Is(err, hub.ErrDropGone) {
		t.Fatalf("an unknown story: %v", err)
	}
	if _, _, err := h.StoryCover(ctx, s.Token); !errors.Is(err, hub.ErrDropGone) {
		t.Fatalf("no cover unless the owner allowed a preview: %v", err)
	}
	if err := h.EditStory(ctx, storyBob, s.ID, hub.StoryEdit{Title: s.Title, ShowDates: true, ShowPlaces: true, Preview: true, EndsInDays: -1}); err != nil {
		t.Fatal(err)
	}
	if _, kind, err := h.StoryCover(ctx, s.Token); err != nil || kind != "image/jpeg" {
		t.Fatalf("the cover: %v", err)
	}
	h.AppURL = ""
	if err := h.EditStory(ctx, storyBob, s.ID, hub.StoryEdit{Title: s.Title, Preview: true, EndsInDays: -1}); !errors.Is(err, hub.ErrStoryPreview) {
		t.Fatalf("a preview needs the app's address: %v", err)
	}
}

func TestAStoryEndsWhenItsDateComesAndRemovingItDeletesItsKey(t *testing.T) {
	h, f, sess := storySetup(t)
	ctx := context.Background()
	album := holidayAlbum(f)
	s, _ := h.CreateStory(ctx, sess, storyBob, hub.StoryParams{Kind: store.StoryAlbum, Source: uuid.MustParse(album), EndsInDays: 7})
	s = waitStory(t, h, s.ID)
	if f.keyCount() != 1 {
		t.Fatalf("one key for the story: %d", f.keyCount())
	}
	h.Now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	if _, err := h.StoryFor(ctx, s.Token); !errors.Is(err, hub.ErrDropGone) {
		t.Fatalf("an expired story answers like an unknown one: %v", err)
	}
	h.StoryMaintenance(ctx)
	if got, _ := h.Store.StoryByID(ctx, bobID, s.ID); got.State != store.StoryEnded {
		t.Fatalf("the maintenance ends it: %s", got.State)
	}
	if f.keyCount() != 1 {
		t.Fatal("an expired story keeps its key until the owner removes it")
	}
	h.Now = nil
	if err := h.EditStory(ctx, storyBob, s.ID, hub.StoryEdit{Title: s.Title, ShowDates: true, ShowPlaces: true, EndsInDays: 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.StoryFor(ctx, s.Token); err != nil {
		t.Fatalf("a longer life brings it back: %v", err)
	}
	if err := h.EndStory(ctx, sess, storyBob, s.ID); err != nil {
		t.Fatal(err)
	}
	if f.keyCount() != 0 {
		t.Fatal("removing the story deletes its key")
	}
	if _, err := h.StoryFor(ctx, s.Token); !errors.Is(err, hub.ErrDropGone) {
		t.Fatalf("a removed story: %v", err)
	}
	if err := h.EndStory(ctx, sess, immich.User{ID: carolID}, s.ID); !errors.Is(err, hub.ErrNoStory) {
		t.Fatalf("somebody else's story: %v", err)
	}
}

func TestAPhotoCanBeTakenOutOfAStoryButNotTheLastAndASourceMustBeThePersons(t *testing.T) {
	h, f, sess := storySetup(t)
	ctx := context.Background()
	album := holidayAlbum(f)
	if _, err := h.CreateStory(ctx, sess, storyBob, hub.StoryParams{Kind: store.StoryAlbum, Source: uuid.New()}); !errors.Is(err, hub.ErrNoAlbum) {
		t.Fatalf("an album that is not theirs: %v", err)
	}
	if _, err := h.CreateStory(ctx, sess, storyBob, hub.StoryParams{Kind: store.StoryPerson, Source: uuid.New()}); !errors.Is(err, hub.ErrRulePerson) {
		t.Fatalf("a person who is not named: %v", err)
	}
	if _, err := h.CreateStory(ctx, sess, storyBob, hub.StoryParams{Kind: store.StoryRange, From: "2026-09-01", To: "2026-08-01"}); !errors.Is(err, hub.ErrBadStoryDates) {
		t.Fatalf("dates the wrong way round: %v", err)
	}
	if _, err := h.CreateStory(ctx, sess, storyBob, hub.StoryParams{Kind: "everything"}); !errors.Is(err, hub.ErrBadStorySource) {
		t.Fatalf("a source that does not exist: %v", err)
	}
	if _, err := h.CreateStory(ctx, sess, storyBob, hub.StoryParams{Kind: store.StoryAlbum, Source: uuid.MustParse(album), Title: strings.Repeat("x", 70)}); !errors.Is(err, hub.ErrBadStoryTitle) {
		t.Fatalf("a title that is too long: %v", err)
	}
	if _, err := h.CreateStory(ctx, sess, storyBob, hub.StoryParams{Kind: store.StoryAlbum, Source: uuid.MustParse(album), EndsInDays: 5}); !errors.Is(err, hub.ErrBadStoryEnd) {
		t.Fatalf("a life of five days: %v", err)
	}
	if f.keyCount() != 0 {
		t.Fatal("a refused story leaves no key behind")
	}
	s, _ := h.CreateStory(ctx, sess, storyBob, hub.StoryParams{Kind: store.StoryAlbum, Source: uuid.MustParse(album)})
	s = waitStory(t, h, s.ID)
	n := len(s.Picks)
	if err := h.RemoveStoryPhoto(ctx, storyBob, s.ID, s.Picks[0].Asset); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.Store.StoryByID(ctx, bobID, s.ID); len(got.Picks) != n-1 {
		t.Fatalf("one photo fewer: %d of %d", len(got.Picks), n)
	}
	if err := h.RemoveStoryPhoto(ctx, storyBob, s.ID, uuid.NewString()); !errors.Is(err, hub.ErrBadStoryPhoto) {
		t.Fatalf("a photo that is not in the story: %v", err)
	}
	for {
		got, _ := h.Store.StoryByID(ctx, bobID, s.ID)
		if len(got.Picks) == 1 {
			if err := h.RemoveStoryPhoto(ctx, storyBob, s.ID, got.Picks[0].Asset); !errors.Is(err, hub.ErrStoryLast) {
				t.Fatalf("not the last one: %v", err)
			}
			break
		}
		if err := h.RemoveStoryPhoto(ctx, storyBob, s.ID, got.Picks[0].Asset); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := h.Store.StoryByID(ctx, bobID, s.ID); len(got.Chapters) != 1 || got.Picks[0].Chapter != 0 {
		t.Fatalf("chapters with no photo left go: %+v", got.Chapters)
	}
	if err := h.RebuildStory(ctx, sess, storyBob, s.ID); err != nil {
		t.Fatal(err)
	}
	if again := waitStory(t, h, s.ID); len(again.Picks) < 2 {
		t.Fatalf("picking again brings them back: %d", len(again.Picks))
	}
}

func TestAStoryOfARangeAndTheEndOfYearSuggestion(t *testing.T) {
	h, f, sess := storySetup(t)
	ctx := context.Background()
	for i := 0; i < 36; i++ {
		f.photo(fmt.Sprintf("2026-%02d-%02d", 1+i%12, 1+i%27), "Lisbon", "")
	}
	h.Now = func() time.Time { return time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC) }
	if _, ok := h.StoryYearOffer(ctx, sess, storyBob); ok {
		t.Fatal("not in June")
	}
	h.Now = func() time.Time { return time.Date(2026, 12, 3, 12, 0, 0, 0, time.UTC) }
	year, ok := h.StoryYearOffer(ctx, sess, storyBob)
	if !ok || year != 2026 {
		t.Fatalf("in December with 36 photos from the year: %d %v", year, ok)
	}
	s, err := h.MakeStoryOfYear(ctx, sess, storyBob, 2026)
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "My 2026" {
		t.Fatalf("the title: %q", s.Title)
	}
	if got := waitStory(t, h, s.ID); got.State != store.StoryReady || len(got.Chapters) < 6 {
		t.Fatalf("a year is cut by month: %s %d", got.State, len(got.Chapters))
	}
	if _, ok := h.StoryYearOffer(ctx, sess, storyBob); ok {
		t.Fatal("not again once it was made")
	}
	h.Now = func() time.Time { return time.Date(2027, 1, 5, 12, 0, 0, 0, time.UTC) }
	if y, ok := h.StoryYearOffer(ctx, sess, storyBob); ok || y != 0 {
		// made for 2026: nothing for 2026 in January either
		t.Fatalf("in January the year before is the one, and it was made: %d %v", y, ok)
	}
	other := immich.User{ID: carolID, Name: "Carol"}
	h.Now = func() time.Time { return time.Date(2026, 12, 20, 12, 0, 0, 0, time.UTC) }
	if err := h.DismissStoryYear(ctx, carolID, 2026); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.StoryYearOffer(ctx, sess, other); ok {
		t.Fatal("a dismissed year is not offered again")
	}
}

func TestAStoryCanBePlayedOnATVInOrderWithItsCaptions(t *testing.T) {
	h, f, sess := storySetup(t)
	ctx := context.Background()
	album := holidayAlbum(f)
	s, err := h.CreateStory(ctx, sess, storyBob, hub.StoryParams{Kind: store.StoryAlbum, Source: uuid.MustParse(album)})
	if err != nil {
		t.Fatal(err)
	}
	s = waitStory(t, h, s.ID)
	_, code, _, _ := h.NewTV("4.4.4.4")
	tv, err := h.ClaimCast(ctx, sess, storyBob, code, hub.CastSource{Kind: store.CastStory, ID: uuid.MustParse(s.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if tv.SourceName != s.Title {
		t.Fatalf("the TV shows the story: %+v", tv)
	}
	items, ordered, err := h.CastFeed(ctx, tv.ID)
	if err != nil || !ordered || len(items) != len(s.Picks) {
		t.Fatalf("an ordered feed of the story's photos: %d %v %v", len(items), ordered, err)
	}
	if items[0].Caption == "" || !strings.Contains(items[0].Caption, "Lisbon") || !strings.Contains(items[0].Caption, "2026-08-01") {
		t.Fatalf("a caption with the chapter, the date and the place: %q", items[0].Caption)
	}
	if data, _, err := h.CastImage(ctx, tv.ID, items[0].ID); err != nil || len(data) < 100 {
		t.Fatalf("a picture: %v", err)
	}
	// somebody else's story, and a story that is not ready, are refused
	_, code2, _, _ := h.NewTV("4.4.4.5")
	if _, err := h.ClaimCast(ctx, sess, immich.User{ID: carolID, Name: "Carol"}, code2, hub.CastSource{Kind: store.CastStory, ID: uuid.MustParse(s.ID)}); !errors.Is(err, hub.ErrNoStory) {
		t.Fatalf("somebody else's story: %v", err)
	}
	// with the captions switched off, the TV is told nothing about dates or places
	if err := h.EditStory(ctx, storyBob, s.ID, hub.StoryEdit{Title: s.Title, EndsInDays: -1}); err != nil {
		t.Fatal(err)
	}
	h.Now = func() time.Time { return time.Now().Add(3 * time.Minute) } // past the cached list
	items, _, _ = h.CastFeed(ctx, tv.ID)
	if strings.Contains(items[0].Caption, "2026") || strings.Contains(items[0].Caption, "Portugal") {
		t.Fatalf("no dates or places in the captions: %q", items[0].Caption)
	}
}
