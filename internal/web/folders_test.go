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
