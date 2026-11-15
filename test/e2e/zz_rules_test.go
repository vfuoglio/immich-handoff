//go:build e2e

// What rules by people, places and dates (#129) rely on in Immich, asked of a real server (docs/design/rules-design.md has the findings).
// It stays as the check that Immich still behaves this way. This Immich runs without machine learning, so faces are made by hand
// (POST /faces gives a photo a face of a named person), which is also how the rest of the suite gets people.
package e2e

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestRulesImmichRules(t *testing.T) {
	alice, _, _, _ := bootstrap(t)
	finding := func(f string, args ...any) { t.Logf("FINDING: "+f, args...) }
	bearer := map[string]string{"Authorization": "Bearer " + alice.token}
	key := func(perms ...string) map[string]string {
		var k struct {
			Secret string `json:"secret"`
		}
		mustOK(t, call(t, "POST", "/api-keys", bearer, map[string]any{"name": fmt.Sprintf("rules-%d", time.Now().UnixNano()), "permissions": perms}, &k), "key")
		return map[string]string{"x-api-key": k.Secret}
	}
	full := keyAuth(alice)
	read := key("user.read", "asset.read")
	people := key("user.read", "asset.read", "person.read")

	one, _ := upload(t, alice, "rule-1.jpg", jpegBytes(30001))
	two, _ := upload(t, alice, "rule-2.jpg", jpegBytes(30002))
	var nora, bob struct {
		ID string `json:"id"`
	}
	mustOK(t, call(t, "POST", "/people", full, map[string]string{"name": "Nora-rules"}, &nora), "person")
	mustOK(t, call(t, "POST", "/people", full, map[string]string{"name": "Bob-rules"}, &bob), "person")
	face := func(asset, person string) {
		t.Helper()
		mustOK(t, call(t, "POST", "/faces", full, map[string]any{"assetId": asset, "personId": person, "imageWidth": 64, "imageHeight": 64, "x": 10, "y": 10, "width": 30, "height": 30}, nil), "face")
	}
	face(one, nora.ID)
	face(one, bob.ID)
	face(two, nora.ID)

	search := func(auth map[string]string, body map[string]any) (ids []string, people [][]string) {
		t.Helper()
		var res struct {
			Assets struct {
				Items []struct {
					ID     string `json:"id"`
					People []struct{ Name string }
				}
			}
		}
		mustOK(t, call(t, "POST", "/search/metadata", auth, body, &res), "search")
		for _, i := range res.Assets.Items {
			ids = append(ids, i.ID)
			var names []string
			for _, p := range i.People {
				names = append(names, p.Name)
			}
			people = append(people, names)
		}
		return
	}

	// 1. people: a key with asset.read can search by person and read who is in each photo; listing people needs person.read
	ids, who := search(read, map[string]any{"personIds": []string{nora.ID}, "withPeople": true})
	if len(ids) != 2 || len(who[0]) == 0 {
		t.Fatalf("a search by person with asset.read: %v %v", ids, who)
	}
	finding("searching by person and reading the people in each result needs only asset.read; the key does not need person.read for that")
	if c := call(t, "GET", "/people", read, nil, nil); c != http.StatusForbidden {
		t.Errorf("listing people with asset.read: HTTP %d, want 403", c)
	}
	var list struct {
		People []struct{ ID, Name string }
	}
	mustOK(t, call(t, "GET", "/people", people, nil, &list), "people")
	found := 0
	for _, p := range list.People {
		if p.ID == nora.ID || p.ID == bob.ID {
			found++
		}
	}
	if found != 2 {
		t.Errorf("both named people are listed with person.read: %d", found)
	}
	finding("listing named people (to pick one in a rule) needs person.read; unnamed people are not listed")

	// 2. "all must match": two people in the same search gives photos that have both
	ids, _ = search(read, map[string]any{"personIds": []string{nora.ID, bob.ID}})
	if len(ids) != 1 || ids[0] != one {
		t.Errorf("both people: %v", ids)
	}
	finding("several person ids in one search mean all of them (the photo with both Nora and Bob only), so 'except' conditions are checked by the app on the people each result carries")

	// 3. dates
	ids, _ = search(read, map[string]any{"personIds": []string{nora.ID}, "takenAfter": time.Now().Add(-time.Hour).Format(time.RFC3339), "takenBefore": time.Now().Add(time.Hour).Format(time.RFC3339)})
	if len(ids) != 2 {
		t.Errorf("a person and a date range together: %v", ids)
	}

	// 4. archived photos are still returned by default, so the app must leave them out itself; locked ones are not returned
	mustOK(t, call(t, "PUT", "/assets", full, map[string]any{"ids": []string{two}, "visibility": "archive"}, nil), "archive")
	ids, _ = search(read, map[string]any{"personIds": []string{nora.ID}})
	if len(ids) != 2 {
		t.Errorf("an archived photo is still in a default search: %v", ids)
	}
	finding("an archived photo is returned by a default search (the app must leave out isArchived itself)")
	mustOK(t, call(t, "PUT", "/assets", full, map[string]any{"ids": []string{two}, "visibility": "timeline"}, nil), "unarchive")

	// 5. readiness: metadata and a thumbnail are told by the asset itself; there is no flag for faces having been looked for
	var a struct {
		HasMetadata bool   `json:"hasMetadata"`
		Thumbhash   string `json:"thumbhash"`
		IsArchived  bool   `json:"isArchived"`
		Visibility  string `json:"visibility"`
	}
	eventually(t, "the photo is processed", 60*time.Second, func() bool {
		call(t, "GET", "/assets/"+one, full, nil, &a)
		return a.HasMetadata && a.Thumbhash != ""
	})
	if a.Visibility != "timeline" {
		t.Errorf("a normal photo is in the timeline: %+v", a)
	}
	finding("an asset says hasMetadata and a thumbhash when metadata and thumbnail are done; nothing says that face detection has finished, so a rule never concludes 'no match': it looks again on every pass over recent photos, and auto waits for the photo to settle")

	// 6. places: Immich reverse-geocodes GPS; city and country match exactly (case matters) and the values to offer come from Immich
	mustOK(t, call(t, "PUT", "/assets", full, map[string]any{"ids": []string{one}, "latitude": 38.72, "longitude": -9.14}, nil), "gps")
	var cities, countries []string
	eventually(t, "the place is worked out", 60*time.Second, func() bool {
		call(t, "GET", "/search/suggestions?type=country", read, nil, &countries)
		return len(countries) > 0
	})
	call(t, "GET", "/search/suggestions?type=city", read, nil, &cities)
	if ids, _ = search(read, map[string]any{"country": countries[0]}); len(ids) != 1 {
		t.Errorf("a search by the suggested country: %v", ids)
	}
	if ids, _ = search(read, map[string]any{"country": "portugal"}); len(ids) != 0 && countries[0] == "Portugal" {
		t.Errorf("country matching is exact and case matters: %v", ids)
	}
	finding("places: suggestions for cities and countries come from /search/suggestions with asset.read (here %v, %v); a city can be a neighbourhood, and matching is exact, so a rule picks from the suggestions and never from free text", cities, countries)

	// 7. a person who is not in the list any more (deleted, or merged into another): a search by their id finds nothing, and the app
	// can tell from the list. Merging itself answers an empty list and changes nothing on a server without machine learning, so it is
	// not exercised here.
	mustOK(t, call(t, "DELETE", "/people/"+bob.ID, full, nil, nil), "delete person")
	if ids, _ = search(read, map[string]any{"personIds": []string{bob.ID}}); len(ids) != 0 {
		t.Errorf("a deleted person matches nothing: %v", ids)
	}
	finding("a person who was deleted matches nothing; a rule holding an id that is no longer in the people list says so and asks for a new person (merging could not be tried without machine learning)")

	// 8. smart search needs machine learning: without it the answer is a plain 400
	var msg struct{ Message string }
	if c := call(t, "POST", "/search/smart", read, map[string]any{"query": "beach"}, &msg); c != http.StatusBadRequest || msg.Message == "" {
		t.Errorf("smart search without machine learning: HTTP %d %q", c, msg.Message)
	}
	finding("smart search on a server without machine learning answers 400 %q: a rule with a phrase says so plainly and finds nothing; it cannot be tested end to end here", msg.Message)
}
