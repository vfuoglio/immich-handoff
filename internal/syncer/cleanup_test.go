package syncer_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// tidyAsset is a photo in a receiver's inbox: when it was uploaded (what Immich calls createdAt), when it was
// taken, and whether it is in an album. Only photos that still carry the inbox tag are listed.
type tidyAsset struct {
	id       string
	uploaded time.Time
	taken    time.Time
	inAlbum  bool
	tagged   bool
}

// tidyImmich is a receiver's Immich that really applies the filters of the search the inbox tidy-up sends, so a test
// fails if the app stops sending the right ones: isNotInAlbum, and createdBefore (the upload date, not the date the
// photo was taken).
type tidyImmich struct {
	mu         sync.Mutex
	assets     []*tidyAsset
	searches   []map[string]json.RawMessage // every search body, as sent
	trashed    []string                     // what was moved to the trash
	denyDelete bool                         // the key may not delete (HTTP 403)
}

func (f *tidyImmich) handler() http.Handler {
	j := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		j(w, []any{map[string]any{"id": inboxTag, "name": "inbox", "value": "inbox", "createdAt": ts, "updatedAt": ts}})
	})
	mux.HandleFunc("POST /api/search/metadata", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.searches = append(f.searches, body)
		notInAlbum := string(body["isNotInAlbum"]) == "true"
		var before time.Time
		if raw, ok := body["createdBefore"]; ok {
			var s string
			json.Unmarshal(raw, &s)
			before, _ = time.Parse(time.RFC3339Nano, s)
		}
		_, hasTaken := body["takenBefore"]
		var items []any
		for _, a := range f.assets {
			if !a.tagged || (notInAlbum && a.inAlbum) {
				continue
			}
			when := a.uploaded // createdBefore is about the upload; a search on the taken date would use takenBefore
			if hasTaken {
				when = a.taken
				before, _ = time.Parse(time.RFC3339Nano, strings.Trim(string(body["takenBefore"]), `"`))
			}
			if !before.IsZero() && !when.Before(before) {
				continue
			}
			items = append(items, map[string]any{"id": a.id, "originalFileName": "IMG.jpg", "fileCreatedAt": ts, "fileModifiedAt": ts,
				"isFavorite": false, "isTrashed": false, "checksum": "x", "exifInfo": map[string]any{"fileSizeInByte": 1}})
		}
		j(w, map[string]any{"assets": map[string]any{"items": items, "count": len(items), "total": len(items), "facets": []any{}}})
	})
	mux.HandleFunc("DELETE /api/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Ids   []string
			Force bool
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.denyDelete {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(403)
			w.Write([]byte(`{"message":"Missing required permission: asset.delete"}`))
			return
		}
		if body.Force {
			w.WriteHeader(400) // a copy is only ever moved to the trash
			return
		}
		for _, id := range body.Ids {
			f.trashed = append(f.trashed, id)
			for _, a := range f.assets {
				if a.id == id {
					a.tagged = false // a trashed photo is not listed any more
				}
			}
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("DELETE /api/tags/"+inboxTag+"/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Ids []string }
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		var out []any
		for _, id := range body.Ids {
			for _, a := range f.assets {
				if a.id == id {
					a.tagged = false
				}
			}
			out = append(out, map[string]any{"id": id, "success": true})
		}
		j(w, out)
	})
	return mux
}

func (f *tidyImmich) stillTagged() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.assets {
		if a.tagged {
			out = append(out, a.id)
		}
	}
	slices.Sort(out)
	return out
}

func tidy(t *testing.T, f *tidyImmich, expiry ...int) {
	t.Helper()
	tidyWith(t, f, nil, nil, expiry...)
}

// tidyWith is tidy with the app-wide settings and each service changed too.
func tidyWith(t *testing.T, f *tidyImmich, config func(*syncer.Config), tune func(*syncer.Service), expiry ...int) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	rcv := &syncer.Account{Name: "family", API: newAPI(t, srv.URL, "dest")}
	var services []*syncer.Service
	for _, days := range expiry {
		svc := service([]*syncer.Account{{Name: "alice", API: newAPI(t, srv.URL, "src")}}, []*syncer.Account{rcv})
		svc.ExpiryDays = days
		if tune != nil {
			tune(svc)
		}
		services = append(services, svc)
	}
	s := &syncer.Syncer{Services: services, Log: quiet()}
	if config != nil {
		config(&s.Cfg)
	}
	s.Tidy(context.Background())
}

const (
	a1 = "a0000000-0000-4000-8000-000000000001"
	a2 = "a0000000-0000-4000-8000-000000000002"
	a3 = "a0000000-0000-4000-8000-000000000003"
	a4 = "a0000000-0000-4000-8000-000000000004"
	a5 = "a0000000-0000-4000-8000-000000000005"
)

func daysAgo(d float64) time.Time { return time.Now().Add(-time.Duration(d * 24 * float64(time.Hour))) }

func TestUnfiledPhotosLoseTheInboxTagOnlyAfterTheExpiry(t *testing.T) {
	f := &tidyImmich{assets: []*tidyAsset{
		{id: a1, uploaded: daysAgo(31), taken: daysAgo(31), tagged: true},                // unfiled, past the 30 days: loses the tag
		{id: a2, uploaded: daysAgo(29), taken: daysAgo(29), tagged: true},                // unfiled, inside the 30 days: keeps it
		{id: a3, uploaded: daysAgo(1), taken: daysAgo(1), inAlbum: true, tagged: true},   // filed in an album: loses it at once
		{id: a4, uploaded: daysAgo(31), taken: daysAgo(31), inAlbum: true, tagged: true}, // filed and old: loses it once
	}}
	tidy(t, f, 30)
	if got := f.stillTagged(); !slices.Equal(got, []string{a2}) {
		t.Errorf("still tagged: %v, want only the unfiled photo that is 29 days old", got)
	}
}

func TestTheExpiryCountsFromTheUploadDateNotFromWhenThePhotoWasTaken(t *testing.T) {
	f := &tidyImmich{assets: []*tidyAsset{
		// taken years ago, uploaded yesterday: an old photo that has only just arrived keeps its tag
		{id: a1, uploaded: daysAgo(1), taken: daysAgo(900), tagged: true},
		// taken yesterday, uploaded 40 days ago: it has waited long enough
		{id: a2, uploaded: daysAgo(40), taken: daysAgo(1), tagged: true},
	}}
	tidy(t, f, 30)
	if got := f.stillTagged(); !slices.Equal(got, []string{a1}) {
		t.Errorf("still tagged: %v, want only the photo that arrived yesterday", got)
	}
}

func TestTheExpirySearchAsksForTheUploadDateAndLeavesOutPhotosInAlbums(t *testing.T) {
	f := &tidyImmich{assets: []*tidyAsset{{id: a1, uploaded: daysAgo(31), taken: daysAgo(31), tagged: true}}}
	tidy(t, f, 30)
	var expiry map[string]json.RawMessage
	for _, s := range f.searches {
		if _, ok := s["createdBefore"]; ok {
			expiry = s
		}
	}
	if expiry == nil {
		t.Fatalf("no search used createdBefore (the upload date): %v", f.searches)
	}
	if string(expiry["isNotInAlbum"]) != "true" {
		t.Errorf("the expiry search must leave out photos in albums (isNotInAlbum): %s", expiry["isNotInAlbum"])
	}
	for _, other := range []string{"takenBefore", "updatedBefore"} {
		if _, ok := expiry[other]; ok {
			t.Errorf("the expiry must not count from %s", other)
		}
	}
	var sent string
	json.Unmarshal(expiry["createdBefore"], &sent)
	at, err := time.Parse(time.RFC3339Nano, sent)
	if err != nil {
		t.Fatalf("createdBefore %q: %v", sent, err)
	}
	if want := time.Now().AddDate(0, 0, -30); at.Before(want.Add(-time.Minute)) || at.After(want.Add(time.Minute)) {
		t.Errorf("createdBefore is %v, want about %v (30 days ago)", at, want)
	}
}

func TestTheExpiryIsTheServicesOwnNumberOfDays(t *testing.T) {
	f := &tidyImmich{assets: []*tidyAsset{
		{id: a1, uploaded: daysAgo(2), tagged: true},   // past a 1 day expiry
		{id: a2, uploaded: daysAgo(0.5), tagged: true}, // inside it
	}}
	tidy(t, f, 1)
	if got := f.stillTagged(); !slices.Equal(got, []string{a2}) {
		t.Errorf("with an expiry of 1 day, still tagged: %v, want only the photo that is half a day old", got)
	}
}

func TestWhenTwoServicesShareAnInboxTheShorterExpiryApplies(t *testing.T) {
	f := &tidyImmich{assets: []*tidyAsset{
		{id: a1, uploaded: daysAgo(10), tagged: true}, // older than 7 days, younger than 30
		{id: a2, uploaded: daysAgo(3), tagged: true},
	}}
	tidy(t, f, 30, 7)
	if got := f.stillTagged(); !slices.Equal(got, []string{a2}) {
		t.Errorf("with expiries of 30 and 7 days for one inbox, still tagged: %v, want only the 3 day old photo", got)
	}
}

func TestNothingIsUntaggedWhenNothingHasExpired(t *testing.T) {
	f := &tidyImmich{assets: []*tidyAsset{{id: a5, uploaded: daysAgo(2), tagged: true}}}
	tidy(t, f, 30)
	if got := f.stillTagged(); !slices.Equal(got, []string{a5}) {
		t.Errorf("still tagged: %v", got)
	}
}

func (f *tidyImmich) trashedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string{}, f.trashed...)
	slices.Sort(out)
	return out
}

func withDelete(c *syncer.Config) { c.ReceiverDelete = true }
func lifetime(days int) func(*syncer.Service) {
	return func(s *syncer.Service) { s.LifetimeDays = days }
}

func TestCopiesThatOutliveTheirLifetimeGoToTheTrashUnlessFiled(t *testing.T) {
	f := &tidyImmich{assets: []*tidyAsset{
		{id: a1, uploaded: daysAgo(5), taken: daysAgo(5), tagged: true},                // unfiled, past the 3 days: to the trash
		{id: a2, uploaded: daysAgo(1), taken: daysAgo(1), tagged: true},                // unfiled, inside the lifetime: stays
		{id: a3, uploaded: daysAgo(5), taken: daysAgo(5), inAlbum: true, tagged: true}, // filed: kept, loses the inbox tag
	}}
	tidyWith(t, f, withDelete, lifetime(3), 30)
	if got := f.trashedIDs(); len(got) != 1 || got[0] != a1 {
		t.Errorf("only the old unfiled copy goes to the trash: %v", got)
	}
	if got := f.stillTagged(); len(got) != 1 || got[0] != a2 {
		t.Errorf("the young copy keeps its tag and the filed one loses it: %v", got)
	}
}

func TestWithALifetimeTheInboxExpiryDoesNotTakeTheTagOffFirst(t *testing.T) {
	f := &tidyImmich{assets: []*tidyAsset{
		{id: a1, uploaded: daysAgo(35), taken: daysAgo(35), tagged: true}, // past the 30-day inbox expiry, inside the 40-day lifetime
	}}
	tidyWith(t, f, withDelete, lifetime(40), 30)
	if got := f.stillTagged(); len(got) != 1 {
		t.Errorf("the copy must keep its tag until its lifetime ends: %v", got)
	}
	if len(f.trashedIDs()) != 0 {
		t.Errorf("nothing is trashed yet: %v", f.trashedIDs())
	}
}

func TestWithoutTheAdministratorsSayTheLifetimeDoesNothing(t *testing.T) {
	f := &tidyImmich{assets: []*tidyAsset{
		{id: a1, uploaded: daysAgo(31), taken: daysAgo(31), tagged: true},
	}}
	tidyWith(t, f, nil, lifetime(3), 30) // RECEIVER_DELETE is off: the old behaviour, the tag comes off, nothing is trashed
	if len(f.trashedIDs()) != 0 || len(f.stillTagged()) != 0 {
		t.Errorf("with deleting off the copy only loses its tag: trashed %v, tagged %v", f.trashedIDs(), f.stillTagged())
	}
}

func TestAKeyThatMayNotDeleteLeavesTheCopiesWhereTheyAre(t *testing.T) {
	f := &tidyImmich{denyDelete: true, assets: []*tidyAsset{
		{id: a1, uploaded: daysAgo(5), taken: daysAgo(5), tagged: true},
	}}
	tidyWith(t, f, withDelete, lifetime(3), 30)
	if got := f.stillTagged(); len(got) != 1 {
		t.Errorf("a refused delete must not cost the copy its tag: %v", got)
	}
}
