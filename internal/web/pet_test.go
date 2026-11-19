package web_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/store"
)

func TestAPetRuleIsMadeFromPhotosThePersonPicksOfTheirOwn(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withPairings(t, &st))
	ctx := context.Background()
	const c1, c2, c3, c4 = "55555555-0000-4000-8000-0000000000c1", "55555555-0000-4000-8000-0000000000c2", "55555555-0000-4000-8000-0000000000c3", "55555555-0000-4000-8000-0000000000c4"
	_, page := do(t, "GET", app.URL+"/", "sess=other", nil)
	if !strings.Contains(page, "Make a rule for a pet") {
		t.Error("the service offers a rule for a pet")
	}
	code, form := do(t, "GET", app.URL+"/fragments/pet-form?service=club", "sess=other", nil)
	if code != 200 {
		t.Fatalf("the form: %d", code)
	}
	for _, want := range []string{"Pick 3 to 8 photos of your pet", "noisy start", "never sends by itself", "rules/pet-photo/" + c1, "rules/pet-photo/" + c4, `name="photo"`} {
		if !strings.Contains(form, want) {
			t.Errorf("the form lacks %q", want)
		}
	}
	if strings.Contains(form, "pet-photo/11111111") {
		t.Error("only the person's own photos are offered")
	}
	if code, _ := do(t, "GET", app.URL+"/fragments/pet-form?service=club", "", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/rules/pet-photo/"+c1, "sess=other", nil); code != 200 {
		t.Errorf("an own photo's picture: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/rules/pet-photo/11111111-1111-1111-1111-111111111111", "sess=other", nil); code != 404 {
		t.Errorf("not somebody else's: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/rules/pet-photo/"+c1, "", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, b := postForm(t, app.URL+"/rules/pet", "sess=other", "service=club&photo="+c1+"&photo="+c2); code != 409 || !strings.Contains(b, "3 to 8 photos of your own") {
		t.Errorf("two photos are too few: %d %s", code, b)
	}
	if code, b := postForm(t, app.URL+"/rules/pet", "sess=other", "service=club&photo="+c1+"&photo="+c2+"&photo=11111111-1111-1111-1111-111111111111"); code != 409 {
		t.Errorf("another person's photo: %d %s", code, b)
	}
	if code, b := postForm(t, app.URL+"/rules/pet", "sess=other", "service=club&photo="+c1+"&photo="+c2+"&photo="+c3); code != 204 {
		t.Fatalf("three photos: %d %s", code, b)
	}
	rules, _ := st.RulesOf(ctx, mustUUID(otherID))
	if len(rules) != 1 || len(rules[0].Cond.Examples) != 3 {
		t.Fatalf("%+v", rules)
	}
	_, page = do(t, "GET", app.URL+"/", "sess=other", nil)
	if !strings.Contains(page, "photos that look like your 3 example photos") || !strings.Contains(page, "Asks you first") {
		t.Error("the rule is shown as a sentence")
	}
	if strings.Contains(page, "Send by itself</button>") || strings.Contains(page, "rules/auto") && strings.Contains(page, `"on":"1"`) {
		t.Error("a pet rule has no switch to send by itself")
	}
}
