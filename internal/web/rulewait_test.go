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

func TestThePairingFormsOfferTheWaitAndEachPersonsChoiceIsKeptAndShown(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withPairings(t, &st))
	ctx := context.Background()
	_, form := do(t, "GET", app.URL+"/fragments/pair-form", "sess=family", nil)
	for _, want := range []string{`name="settle"`, "Wait before offering a photo", "The usual wait (5 min)"} {
		if !strings.Contains(form, want) {
			t.Errorf("the offer form lacks %q", want)
		}
	}
	if code, b := postForm(t, app.URL+"/pairings", "sess=family", "email=alice@x.test&person="+noraID+"&settle=61"); code != 409 || !strings.Contains(b, "from 1 to 60 minutes") {
		t.Errorf("a bad wait: %d %s", code, b)
	}
	if all, _ := st.Pairings(ctx); len(all) != 0 {
		t.Fatalf("it makes nothing: %+v", all)
	}
	if code, b := postForm(t, app.URL+"/pairings", "sess=family", "email=alice@x.test&person="+noraID+"&settle=2"); code != 204 {
		t.Fatalf("the offer: %d %s", code, b)
	}
	p := onlyPairing(t, st)
	_, mine := do(t, "GET", app.URL+"/", "sess=family", nil)
	if !strings.Contains(mine, "Waits 2 min before offering a photo.") {
		t.Error("the starter's card says how long their rule waits")
	}
	_, page := do(t, "GET", app.URL+"/pair/"+p.ID, "sess=alice", nil)
	for _, want := range []string{`name="settle"`, "The usual wait (5 min)"} {
		if !strings.Contains(page, want) {
			t.Errorf("the consent page lacks %q", want)
		}
	}
	if code, b := postForm(t, app.URL+"/pairings/accept", "sess=alice", "pairing="+p.ID+"&person="+leoID+"&settle=abc"); code != 409 || !strings.Contains(b, "from 1 to 60 minutes") {
		t.Errorf("a wait that is not a number: %d %s", code, b)
	}
	if code, b := postForm(t, app.URL+"/pairings/accept", "sess=alice", "pairing="+p.ID+"&person="+leoID+"&settle=10"); code != 204 {
		t.Fatalf("the yes: %d %s", code, b)
	}
	rules, _ := st.RulesOfPairing(ctx, p.ID)
	got := map[string]int{}
	for _, r := range rules {
		got[r.OwnerID.String()] = r.SettleMinutes
	}
	if got[familyID] != 2 || got[aliceID] != 10 {
		t.Errorf("each person's own wait: %v", got)
	}
	_, theirs := do(t, "GET", app.URL+"/", "sess=alice", nil)
	if !strings.Contains(theirs, "Waits 10 min before offering a photo.") || strings.Contains(theirs, "Waits 2 min") {
		t.Error("the other person's card says their own wait, not the starter's")
	}
}

// The rules and the sharing of photos are in every language the app has: nothing of them is left in English, and a refusal is said in
// the person's language too.
func TestTheRulesAndThePhotoSharingScreensAreInTheReadersLanguage(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withPairings(t, &st))
	for lang, c := range map[string]struct{ rule, sharing, refusal string }{
		"de": {"Regel anlegen", "Fotos mit jemandem teilen", "die Wartezeit, bevor ein Foto angeboten wird, kann 1 bis 60 Minuten betragen"},
		"fr": {"Créer la règle", "Partager des photos avec quelqu’un", "le délai avant de proposer une photo peut aller de 1 à 60 minutes"},
		"ru": {"Создать это правило", "Делиться фото с другим человеком", "ожидание перед предложением фото может быть от 1 до 60 минут"},
	} {
		cookie := "sess=other; handoff-lang=" + lang
		_, form := do(t, "GET", app.URL+"/fragments/rule-form?service=club", cookie, nil)
		if !strings.Contains(form, c.rule) {
			t.Errorf("%s: the rule form is not translated: %.200s", lang, form)
		}
		for _, english := range []string{"Make the rule", "Taken between", "Wait before offering a photo", "Make the same rule", "But not photos with"} {
			if strings.Contains(form, english) {
				t.Errorf("%s: the rule form still says %q", lang, english)
			}
		}
		if code, b := postForm(t, app.URL+"/rules", cookie, "service=club&person="+noraID+"&settle=61"); code != 409 || !strings.Contains(b, c.refusal) {
			t.Errorf("%s: a refusal is said in the reader's language: %d %s", lang, code, b)
		}
		if code, b := postForm(t, app.URL+"/rules", cookie, "service=club&person="+noraID+"&settle=2"); code != 204 {
			t.Fatalf("%s: a rule: %d %s", lang, code, b)
		}
		_, mine := do(t, "GET", app.URL+"/", cookie, nil)
		for _, english := range []string{"A rule looks in your Immich", "Asks you first", "Also look back", "Waits 2 min before", "End rule"} {
			if strings.Contains(mine, english) {
				t.Errorf("%s: the rules box still says %q", lang, english)
			}
		}
		_, page := do(t, "GET", app.URL+"/", "sess=family; handoff-lang="+lang, nil)
		if !strings.Contains(page, c.sharing) {
			t.Errorf("%s: the card for sharing photos is not translated", lang)
		}
		for _, english := range []string{"Share photos with someone", "Offer to share photos", "Loading your people"} {
			if strings.Contains(page, english) {
				t.Errorf("%s: the card still says %q", lang, english)
			}
		}
	}
}
