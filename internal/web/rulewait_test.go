package web_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/store"
)

func TestARuleFormOffersTheWaitBeforeAPhotoIsOfferedAndTheChoiceIsKeptAndShown(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withPairings(t, &st))
	ctx := context.Background()
	_, form := do(t, "GET", app.URL+"/fragments/rule-form?service=club", "sess=other", nil)
	for _, want := range []string{`name="settle"`, "Wait before offering a photo", "The usual wait (5 min)", ">1 min<", ">30 min<", "A rule that sends by itself waits six times as long."} {
		if !strings.Contains(form, want) {
			t.Errorf("the form lacks %q", want)
		}
	}
	if code, b := postForm(t, app.URL+"/rules", "sess=other", "service=club&person="+noraID+"&settle=2"); code != 204 {
		t.Fatalf("a rule that waits two minutes: %d %s", code, b)
	}
	if code, b := postForm(t, app.URL+"/rules", "sess=other", "service=club&person="+leoID); code != 204 {
		t.Fatalf("a rule with the usual wait: %d %s", code, b)
	}
	rules, _ := st.RulesOf(ctx, mustUUID(otherID))
	if len(rules) != 2 || rules[0].SettleMinutes != 2 || rules[1].SettleMinutes != 0 {
		t.Fatalf("the choice is kept, and the usual wait stays the default: %+v", rules)
	}
	for _, bad := range []string{"61", "-3", "abc"} {
		if code, b := postForm(t, app.URL+"/rules", "sess=other", "service=club&person="+noraID+"&settle="+bad); code != 409 || !strings.Contains(b, "from 1 to 60 minutes") {
			t.Errorf("%q: %d %s", bad, code, b)
		}
	}
	if code, _ := postForm(t, app.URL+"/rules", "", "service=club&person="+noraID+"&settle=2"); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	_, page := do(t, "GET", app.URL+"/", "sess=other", nil)
	if n := strings.Count(page, "Waits 2 min before offering a photo."); n != 1 {
		t.Errorf("only the rule with its own wait says so, and says it once: %d", n)
	}
	if strings.Contains(page, "Waits 0 min") {
		t.Error("the usual wait is not announced")
	}
	// a rule that sends by itself says the right time, not half an hour
	if err := st.SetRuleMode(ctx, rules[0].ID, store.RuleAuto, rules[0].CreatedAt); err != nil {
		t.Fatal(err)
	}
	_, page = do(t, "GET", app.URL+"/", "sess=other", nil)
	if !strings.Contains(page, "once they have been there 12 min") {
		t.Error("an automatic rule that waits two minutes sends after twelve")
	}
}
