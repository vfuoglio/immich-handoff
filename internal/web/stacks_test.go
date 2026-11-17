package web_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/store"
)

func TestTheStackingCardChecksTheRuleAsItIsFilledInAndMakesIt(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	ctx := context.Background()
	if _, page := do(t, "GET", app.URL+"/", "sess=other", nil); !strings.Contains(page, "Stack photos that belong together") || !strings.Contains(page, "Stack photos by a rule") {
		t.Fatal("the card is offered")
	}
	if code, _ := do(t, "GET", app.URL+"/fragments/stack-check?pair=1", "", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, body := do(t, "GET", app.URL+"/fragments/stack-check?pair=1&edit=1&cover=jpeg", "sess=other", nil); code != 200 || !strings.Contains(body, "Looked at 2 of your photos and found: 1 RAW with its JPEG.") {
		t.Errorf("what the form says: %d %s", code, body)
	}
	if _, body := do(t, "GET", app.URL+"/fragments/stack-check?edit=1", "sess=other", nil); !strings.Contains(body, "found nothing to stack") {
		t.Errorf("nothing for the signal asked: %s", body)
	}
	if _, body := do(t, "GET", app.URL+"/fragments/stack-check", "sess=other", nil); !strings.Contains(body, "choose at least one thing to stack") {
		t.Errorf("no signal chosen: %s", body)
	}
	if code, b := postForm(t, app.URL+"/rules/stack", "sess=other", "cover=jpeg"); code != 409 {
		t.Errorf("nothing to stack: %d %s", code, b)
	}
	if code, b := postForm(t, app.URL+"/rules/stack", "sess=other", "pair=1&edit=1&burst=1&cover=raw"); code != 204 {
		t.Fatalf("make the rule: %d %s", code, b)
	}
	if code, _ := postForm(t, app.URL+"/rules/stack", "sess=other", "pair=1&edit=1&burst=1&cover=raw"); code != 409 {
		t.Errorf("the same twice: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/rules/stack", "", "pair=1"); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	_, page := do(t, "GET", app.URL+"/", "sess=other", nil)
	for _, want := range []string{"Stack a RAW with its JPEG, an edit with its original, a burst", "Asks you first"} {
		if !strings.Contains(page, want) {
			t.Errorf("the rule on the page lacks %q", want)
		}
	}
	rules, _ := st.RulesOf(ctx, mustUUID(otherID))
	var id string
	for _, r := range rules {
		if r.Kind == store.RuleKindStack {
			id = r.ID
		}
	}
	if id == "" {
		t.Fatal("the rule is stored")
	}
	if code, b := postForm(t, app.URL+"/rules/stack-undo", "sess=other", "rule="+id); code != 409 || !strings.Contains(b, "nothing to take apart") {
		t.Errorf("nothing to undo yet: %d %s", code, b)
	}

	// what it offers: one cover for each group on the card, and every photo of each group on the review page
	cover := "55555555-0000-4000-8000-0000000000b2"
	why := "pair:" + cover
	st.PutRulePhoto(ctx, id, cover, store.RuleSuggested, why)
	st.PutRulePhoto(ctx, id, "55555555-0000-4000-8000-0000000000b1", store.RuleSuggested, why)
	_, page = do(t, "GET", app.URL+"/", "sess=other", nil)
	for _, want := range []string{"1 group of photos to stack", "1 RAW with its JPEG", "Stack them", "Not these", "always", "rules/photo/" + id + "/" + cover} {
		if !strings.Contains(page, want) {
			t.Errorf("the suggestion card lacks %q", want)
		}
	}
	if strings.Contains(page, "rules/photo/"+id+"/55555555-0000-4000-8000-0000000000b1") && strings.Count(page, "rules/photo/"+id+"/") > 2 {
		t.Error("the card shows one picture for each group")
	}
	_, review := do(t, "GET", app.URL+"/rules/review/"+id, "sess=other", nil)
	for _, want := range []string{"Photos to stack", "A RAW with its JPEG", `name="photo" value="` + cover + `" checked`, "Stack the ticked groups", "0000000000b1"} {
		if !strings.Contains(review, want) {
			t.Errorf("the review page lacks %q", want)
		}
	}
	// stopping it deletes the key
	if code, _ := postForm(t, app.URL+"/rules/delete", "sess=other", "rule="+id); code != 204 {
		t.Fatalf("stop: %d", code)
	}
	if _, ok, _ := st.StackKey(ctx, mustUUID(otherID)); ok {
		t.Error("the key is gone with the last rule")
	}
}
