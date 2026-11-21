package web_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/store"
)

func TestTheFoldersCardChecksTheFolderAsItIsTypedAndMakesTheRule(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	ctx := context.Background()
	if _, page := do(t, "GET", app.URL+"/", "sess=other", nil); !strings.Contains(page, "Albums from my folders") || !strings.Contains(page, "Make albums from a folder") {
		t.Fatal("the card is offered")
	}
	if code, _ := do(t, "GET", app.URL+"/fragments/folder-check?folder=/external/photos", "", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, body := do(t, "GET", app.URL+"/fragments/folder-check?folder=/external/photos&level=0&naming=last", "sess=other", nil); code != 200 ||
		!strings.Contains(body, "Found 3 of your photos") || !strings.Contains(body, "making 2 albums") || !strings.Contains(body, "paris (2 photos)") || !strings.Contains(body, "rome (1 photo)") {
		t.Errorf("what the form says as the folder is typed: %d %s", code, body)
	}
	if _, body := do(t, "GET", app.URL+"/fragments/folder-check?folder=/external/photos&level=1&naming=parent", "sess=other", nil); !strings.Contains(body, "photos / 2025-02 (3 photos)") {
		t.Errorf("another level and naming: %s", body)
	}
	if _, body := do(t, "GET", app.URL+"/fragments/folder-check?folder=photos", "sess=other", nil); !strings.Contains(body, "write the folder as a path") {
		t.Errorf("a folder that is not a path: %s", body)
	}
	if _, body := do(t, "GET", app.URL+"/fragments/folder-check?folder=/nowhere", "sess=other", nil); !strings.Contains(body, "none of your photos") {
		t.Errorf("a folder with none of the person's photos: %s", body)
	}
	if code, b := postForm(t, app.URL+"/rules/folder", "sess=other", "folder=/nowhere&level=0&naming=last"); code != 409 {
		t.Errorf("a folder with nothing: %d %s", code, b)
	}
	if code, b := postForm(t, app.URL+"/rules/folder", "sess=other", "folder=/external/photos&level=1&naming=parent"); code != 204 {
		t.Fatalf("make the rule: %d %s", code, b)
	}
	if code, _ := postForm(t, app.URL+"/rules/folder", "sess=other", "folder=/external/photos&level=1&naming=parent"); code != 409 {
		t.Errorf("the same folder twice: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/rules/folder", "", "folder=/external/photos"); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	_, page := do(t, "GET", app.URL+"/", "sess=other", nil)
	for _, want := range []string{"/external/photos", "An album for the first folder below it, named after the folder and the one above it", "Asks you first", "Stop"} {
		if !strings.Contains(page, want) {
			t.Errorf("the rule on the page lacks %q", want)
		}
	}
	rules, _ := st.RulesOf(ctx, mustUUID(otherID))
	var id string
	for _, r := range rules {
		if r.Kind == store.RuleKindFolder {
			id = r.ID
		}
	}
	if id == "" {
		t.Fatal("the rule is stored")
	}

	// what it offers: the card names the albums, and the review page shows each photo with its album
	st.PutRulePhoto(ctx, id, "55555555-0000-4000-8000-0000000000a1", store.RuleSuggested, "photos / 2025-02")
	st.PutRulePhoto(ctx, id, "55555555-0000-4000-8000-0000000000a2", store.RuleSuggested, "photos / 2025-02")
	st.PutRulePhoto(ctx, id, "55555555-0000-4000-8000-0000000000a3", store.RuleSuggested, "photos / 2025-03")
	_, page = do(t, "GET", app.URL+"/", "sess=other", nil)
	for _, want := range []string{"3 photos for 2 albums from your folders", "Albums: photos / 2025-02, photos / 2025-03", "Make the albums", "Not these", "always"} {
		if !strings.Contains(page, want) {
			t.Errorf("the suggestion card lacks %q", want)
		}
	}
	if _, review := do(t, "GET", app.URL+"/rules/review/"+id, "sess=other", nil); !strings.Contains(review, "Albums from /external/photos") || !strings.Contains(review, "Make the albums with the ticked photos") || !strings.Contains(review, "photos / 2025-03") {
		t.Error("the review page speaks of the albums and shows each photo's album")
	}

	// stopping it
	if code, _ := postForm(t, app.URL+"/rules/delete", "sess=other", "rule="+id); code != 204 {
		t.Fatalf("stop: %d", code)
	}
	if _, page := do(t, "GET", app.URL+"/", "sess=other", nil); strings.Contains(page, id) {
		t.Error("the rule is gone")
	}
	if _, ok, _ := st.FolderKey(ctx, mustUUID(otherID)); ok {
		t.Error("the key is gone with the last rule")
	}
}

func TestTheFoldersCardIsNotOfferedWithoutSelfService(t *testing.T) {
	app, _ := setup(t)
	if _, page := do(t, "GET", app.URL+"/", "sess=other", nil); strings.Contains(page, "Albums from my folders") {
		t.Error("no card without a store")
	}
}

func TestASmartAlbumShowsOnTheSettingsCardAsASuggestionAndOnTheReviewPage(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	ctx := context.Background()
	if _, page := do(t, "GET", app.URL+"/", "sess=other", nil); !strings.Contains(page, "Smart albums") || !strings.Contains(page, "Make a smart album") {
		t.Fatal("the card is offered")
	}
	if code, _ := postForm(t, app.URL+"/rules/album", "", "newname=x"); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/fragments/album-form", "", nil); code != 401 {
		t.Errorf("form signed out: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/rules/album", "sess=other", "make=Leica&newname=Leicas"); code != 409 && code != 502 {
		t.Errorf("a camera nobody has, or no way to ask: %d", code)
	}
	r, err := st.AddRule(ctx, store.Rule{OwnerID: mustUUID(otherID), Kind: store.RuleKindAlbum, Dest: "55555555-0000-4000-8000-0000000000b1", DestAlbum: "Canon pictures",
		Cond: store.RuleCond{Make: "Canon", RemoveStale: true}})
	if err != nil {
		t.Fatal(err)
	}
	_, page := do(t, "GET", app.URL+"/", "sess=other", nil)
	for _, want := range []string{"Canon pictures", "taken with Canon", "Asks you first", "Takes out what no longer matches", "Look for older photos"} {
		if !strings.Contains(page, want) {
			t.Errorf("the rule on the page lacks %q", want)
		}
	}
	st.PutRulePhoto(ctx, r.ID, "55555555-0000-4000-8000-0000000000a1", store.RuleSuggested, "taken with Canon")
	st.PutRulePhoto(ctx, r.ID, "55555555-0000-4000-8000-0000000000a2", store.RuleSuggested, "taken with Canon")
	_, page = do(t, "GET", app.URL+"/", "sess=other", nil)
	for _, want := range []string{"2 new photos for the album Canon pictures", "Put them in the album", "Not these", "Always"} {
		if !strings.Contains(page, want) {
			t.Errorf("the suggestion card lacks %q", want)
		}
	}
	if _, review := do(t, "GET", app.URL+"/rules/review/"+r.ID, "sess=other", nil); !strings.Contains(review, "For the album Canon pictures") || !strings.Contains(review, "Put the ticked photos in the album") {
		t.Error("the review page speaks of the album")
	}
	if code, _ := postForm(t, app.URL+"/rules/delete", "sess=other", "rule="+r.ID); code != 204 {
		t.Fatalf("stop: %d", code)
	}
}

func TestCirclesCardMakesACircleOffersAlbumsAndTheAdministratorChoosesHowPeopleArePicked(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	ctx := context.Background()
	if _, page := do(t, "GET", app.URL+"/", "sess=other", nil); !strings.Contains(page, "Make a circle") || !strings.Contains(page, "Every new album you make is offered to them") {
		t.Fatal("the card is offered")
	}
	if code, _ := postForm(t, app.URL+"/circles", "", "name=x&emails=alice@x.test"); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/fragments/circle-form", "", nil); code != 401 {
		t.Errorf("form signed out: %d", code)
	}
	if code, body := do(t, "GET", app.URL+"/fragments/circle-form", "sess=other", nil); code != 200 || !strings.Contains(body, "Email addresses of their Immich accounts") {
		t.Errorf("method 1 asks for emails: %d %s", code, body)
	}
	if code, _ := postForm(t, app.URL+"/circles", "sess=other", "name=Family&emails=nobody@x.test"); code != 409 {
		t.Errorf("an email nobody has: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/circles", "sess=other", "name=Family&emails=other@x.test"); code != 409 {
		t.Errorf("oneself: %d", code)
	}
	if code, b := postForm(t, app.URL+"/circles", "sess=other", "name=Family&role=editor&prefix=Trip&emails=alice@x.test"); code != 204 {
		t.Fatalf("make the circle: %d %s", code, b)
	}
	_, page := do(t, "GET", app.URL+"/", "sess=other", nil)
	for _, want := range []string{"Family", "Alice", "alice@x.test", "Editors", "Albums whose name starts with Trip", "Asks you first", "Offer my existing albums"} {
		if !strings.Contains(page, want) {
			t.Errorf("the circle on the page lacks %q", want)
		}
	}
	cs, _ := st.CirclesOf(ctx, mustUUID(otherID))
	if len(cs) != 1 {
		t.Fatalf("the circle is stored: %v", cs)
	}
	id := cs[0].ID
	st.PutCircleShare(ctx, store.CircleShare{CircleID: id, AlbumID: mustUUID("77777777-0000-4000-8000-000000000001"), AlbumName: "Trip 2026", UserID: mustUUID(aliceID), State: store.CircleOffered})
	_, page = do(t, "GET", app.URL+"/", "sess=other", nil)
	for _, want := range []string{"1 album for the circle Family", "Albums: Trip 2026", "Immich may email each person", "Share them", "Not these"} {
		if !strings.Contains(page, want) {
			t.Errorf("the suggestion card lacks %q", want)
		}
	}
	if code, _ := postForm(t, app.URL+"/circles/answer", "sess=family", "circle="+id+"&do=decline"); code != 409 {
		t.Errorf("another person cannot answer for the circle: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/circles/answer", "sess=other", "circle="+id+"&do=decline"); code != 204 {
		t.Errorf("decline: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/circles/auto", "sess=other", "circle="+id+"&on=1"); code != 409 {
		t.Errorf("by itself only after three Always: %d", code)
	}

	// the administrator allows picking from every account; nobody else can
	if code, _ := postForm(t, app.URL+"/admin/circles", "sess=other", "method=2"); code != 404 {
		t.Errorf("not for an ordinary person: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/admin/circles", "sess=admin", "method=2"); code != 204 {
		t.Fatalf("the administrator chooses: %d", code)
	}
	if _, body := do(t, "GET", app.URL+"/fragments/circle-form", "sess=other", nil); !strings.Contains(body, "Alice") || !strings.Contains(body, "Family") || strings.Contains(body, "Other</b>") || strings.Contains(body, "Email addresses of their") {
		t.Errorf("method 2 lists the other accounts and not oneself: %s", body)
	}
	if code, _ := postForm(t, app.URL+"/circles/member", "sess=other", "circle="+id+"&user="+aliceID); code != 204 && code != 409 {
		t.Errorf("add a person by account: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/circles/delete", "sess=other", "circle="+id); code != 204 {
		t.Fatalf("end the circle: %d", code)
	}
}

func TestThePlacesCardOffersTheRuleAndTheReviewPageShowsEachPhotoNextToWhereItsPlaceComesFrom(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	ctx := context.Background()
	if _, page := do(t, "GET", app.URL+"/", "sess=other", nil); !strings.Contains(page, "Places for photos that have none") || !strings.Contains(page, "cannot be taken back by the app") || !strings.Contains(page, "Offer places to my photos that have none") {
		t.Fatal("the card says what it does and that a place cannot be taken back")
	}
	if code, _ := postForm(t, app.URL+"/rules/places", "", ""); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, b := postForm(t, app.URL+"/rules/places", "sess=other", ""); code != 204 {
		t.Fatalf("make the rule: %d %s", code, b)
	}
	if code, _ := postForm(t, app.URL+"/rules/places", "sess=other", ""); code != 409 {
		t.Errorf("one rule only: %d", code)
	}
	rules, _ := st.RulesOf(ctx, mustUUID(otherID))
	var id string
	for _, r := range rules {
		if r.Kind == store.RuleKindPlace {
			id = r.ID
		}
	}
	if id == "" {
		t.Fatal("the rule is stored")
	}
	st.PutRulePhoto(ctx, id, "55555555-0000-4000-8000-0000000000d1", store.RuleSuggested, "2|38.722300|-9.139300|55555555-0000-4000-8000-0000000000d9|3|90|Lisbon, Portugal")
	st.PutRulePhoto(ctx, id, "55555555-0000-4000-8000-0000000000d2", store.RuleSuggested, "1|38.722300|-9.139300|55555555-0000-4000-8000-0000000000d9|8|0|Lisbon, Portugal")
	st.PutRulePhoto(ctx, id, "55555555-0000-4000-8000-0000000000d9", store.RuleSeen, "")
	_, page := do(t, "GET", app.URL+"/", "sess=other", nil)
	for _, want := range []string{"2 photos can get a place from the photos next to them", "Give them these places", "Not these", "Always", "cannot take a written place back"} {
		if !strings.Contains(page, want) {
			t.Errorf("the suggestion card lacks %q", want)
		}
	}
	code, review := do(t, "GET", app.URL+"/rules/review/"+id, "sess=other", nil)
	if code != 200 {
		t.Fatalf("the review page: %d", code)
	}
	for _, want := range []string{"Places for photos that have none", "The photo the place comes from", "rules/photo/" + id + "/55555555-0000-4000-8000-0000000000d9", "Show on a map",
		"https://www.openstreetmap.org/?mlat=38.72230&amp;mlon=-9.13930", "rel=\"noopener noreferrer\"", "Lisbon, Portugal", "on both sides", "on one side", "Give the ticked photos these places", "The places are written into your photos"} {
		if !strings.Contains(review, want) {
			t.Errorf("the review page lacks %q", want)
		}
	}
	if strings.Contains(review, "<img src=\"https://") || strings.Contains(review, "tile.openstreetmap") {
		t.Error("no map or picture is loaded from outside")
	}
	if code, _ := postForm(t, app.URL+"/rules/delete", "sess=other", "rule="+id); code != 204 {
		t.Fatalf("stop: %d", code)
	}
}
