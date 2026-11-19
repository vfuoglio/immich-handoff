package hub_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

const (
	noraID = "eeeeeeee-0000-4000-8000-000000000001"
	leoID  = "eeeeeeee-0000-4000-8000-000000000002"
)

func ruleEnv(t *testing.T) (*env, context.Context) {
	e, ctx := setup(t, true), context.Background()
	e.f.named = []map[string]any{
		{"id": noraID, "name": "Nora", "isHidden": false, "thumbnailPath": "x"},
		{"id": leoID, "name": "Leo", "isHidden": false, "thumbnailPath": "x"},
		{"id": uuid.NewString(), "name": "", "isHidden": false, "thumbnailPath": "x"},
	}
	e.f.countries = []string{"Portugal", "Italy"}
	e.f.cities = map[string][]string{"Portugal": {"Lisbon"}}
	e.f.albums = []map[string]any{{"id": "dddddddd-0000-4000-8000-000000000001", "albumName": "Private", "assetCount": 3}}
	if err := e.h.Join(ctx, e.sess, bob, "family", "", ""); err != nil {
		t.Fatal(err)
	}
	return e, ctx
}

func TestARuleIsMadeFromChoicesImmichOffersAndSaysInWordsWhatItDoes(t *testing.T) {
	e, ctx := ruleEnv(t)
	c, err := e.h.RuleChoicesFor(ctx, e.sess, "Portugal")
	if err != nil || len(c.People) != 2 || c.People[0].Name != "Leo" || len(c.Countries) != 2 || len(c.Cities) != 1 {
		t.Fatalf("the form offers named people, places and albums: %+v %v", c, err)
	}
	r, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", People: []uuid.UUID{uuid.MustParse(noraID)}, NotPeople: []uuid.UUID{uuid.MustParse(leoID)},
		After: "2026-05-03", Before: "2026-05-09", Country: "Portugal", City: "Lisbon", NotAlbums: []uuid.UUID{uuid.MustParse("dddddddd-0000-4000-8000-000000000001")}})
	if err != nil {
		t.Fatal(err)
	}
	if got := hub.RuleSentence(r.Cond); got != "photos of Nora without Leo taken from 2026-05-03 to 2026-05-09 in Lisbon, Portugal not in Private" {
		t.Errorf("in words: %q", got)
	}
	if r.Mode != "suggest" || r.Since.IsZero() || r.Paused {
		t.Errorf("a new rule asks first and looks only from now on: %+v", r)
	}
	list, _ := e.h.RulesFor(ctx, bob.ID, "family")
	if len(list) != 1 || list[0].ID != r.ID {
		t.Fatalf("listed: %+v", list)
	}
	if err := e.h.PauseRule(ctx, bob.ID, r.ID, true); err != nil {
		t.Fatal(err)
	}
	if list, _ = e.h.RulesFor(ctx, bob.ID, "family"); !list[0].Paused {
		t.Error("paused")
	}
	if err := e.h.DeleteRule(ctx, bob, r.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ = e.h.RulesFor(ctx, bob.ID, "family"); len(list) != 0 {
		t.Error("ended")
	}
}

func TestARuleNeedsSomethingToLookForAndOnlyRealChoices(t *testing.T) {
	e, ctx := ruleEnv(t)
	nora := uuid.MustParse(noraID)
	for name, p := range map[string]hub.RuleParams{
		"empty":        {Service: "family"},
		"a stranger":   {Service: "family", People: []uuid.UUID{uuid.New()}},
		"same person":  {Service: "family", People: []uuid.UUID{nora}, NotPeople: []uuid.UUID{nora}},
		"bad date":     {Service: "family", After: "2026-13-40"},
		"dates around": {Service: "family", After: "2026-06-01", Before: "2026-05-01"},
		"made-up city": {Service: "family", Country: "Portugal", City: "Atlantis"},
		"free country": {Service: "family", Country: "portugal"},
		"long phrase":  {Service: "family", Phrase: strings.Repeat("x", 101)},
		"not my album": {Service: "family", Album: uuid.New()},
	} {
		if _, err := e.h.AddRule(ctx, e.sess, bob, p); err == nil || !hub.IsUserError(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "other", People: []uuid.UUID{nora}}); !errors.Is(err, hub.ErrNotJoined) {
		t.Errorf("only a service they joined: %v", err)
	}
	if _, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Phrase: "a beach"}); err != nil {
		t.Errorf("a phrase alone is a rule: %v", err)
	}
	// leaving the service ends its rules
	if err := e.h.Leave(ctx, e.sess, bob, "family"); err != nil {
		t.Fatal(err)
	}
	if list, _ := e.h.RulesFor(ctx, bob.ID, "family"); len(list) != 0 {
		t.Error("its rules ended with the membership")
	}
}

func hit(owner uuid.UUID, ageMinutes int, mod func(m map[string]any), people ...string) map[string]any {
	var ps []any
	for _, p := range people {
		ps = append(ps, map[string]any{"id": p, "name": map[string]string{noraID: "Nora", leoID: "Leo"}[p], "isHidden": false, "thumbnailPath": "x"})
	}
	at := time.Now().Add(-time.Duration(ageMinutes) * time.Minute).UTC().Format(time.RFC3339Nano)
	m := map[string]any{"id": uuid.NewString(), "ownerId": owner.String(), "createdAt": at, "hasMetadata": true, "thumbhash": "abc", "isArchived": false, "isTrashed": false,
		"localDateTime": "2026-05-04T10:00:00Z", "fileCreatedAt": at, "originalFileName": "x.jpg", "type": "IMAGE", "people": ps}
	if mod != nil {
		mod(m)
	}
	return m
}

func TestARuleSuggestsOnlyWhatMatchesAndHasSettledAndNeverTheSamePhotoTwice(t *testing.T) {
	e, ctx := ruleEnv(t)
	nora, leo := noraID, leoID
	r, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", People: []uuid.UUID{uuid.MustParse(nora)}, NotPeople: []uuid.UUID{uuid.MustParse(leo)}})
	if err != nil {
		t.Fatal(err)
	}
	good := hit(bobID, 60, nil, nora)
	e.f.ruleHits = []map[string]any{
		good,
		hit(bobID, 60, nil, nora, leo), // Leo is in it too
		hit(bobID, 60, func(m map[string]any) { m["isArchived"] = true }, nora),
		hit(bobID, 60, func(m map[string]any) { m["isTrashed"] = true }, nora),
		hit(annID, 60, nil, nora), // someone else's photo in a shared album
		hit(bobID, 1, nil, nora),  // too new: faces may not be found yet
		hit(bobID, 60, func(m map[string]any) { m["thumbhash"] = nil }, nora),
		hit(bobID, 60, func(m map[string]any) { m["hasMetadata"] = false }, nora),
	}
	stored, _ := e.st.RuleByID(ctx, r.ID)
	n, err := e.h.LookRule(ctx, stored)
	if err != nil || n != 1 {
		t.Fatalf("one photo matches: %d %v", n, err)
	}
	got, _ := e.st.RulePhotos(ctx, r.ID, "suggested")
	if len(got) != 1 || got[0].AssetID != good["id"] || got[0].Why != "Nora in it" {
		t.Fatalf("it says why: %+v", got)
	}
	ids, _ := e.f.lastSearch["personIds"].([]any)
	if len(ids) != 1 || ids[0] != nora || e.f.lastSearch["createdAfter"] == nil || e.f.lastSearch["withPeople"] != true {
		t.Errorf("Immich is asked for the people and for new photos only: %v", e.f.lastSearch)
	}
	// looking again adds nothing; a photo the person declined is never offered again
	if n, _ = e.h.LookRule(ctx, stored); n != 0 {
		t.Errorf("the same photo is not suggested twice: %d", n)
	}
	e.st.SetRulePhotos(ctx, r.ID, []string{good["id"].(string)}, "suggested", "declined")
	if n, _ = e.h.LookRule(ctx, stored); n != 0 {
		t.Errorf("a declined photo stays declined: %d", n)
	}
	// a later photo (its faces found after the first look) simply joins
	late := hit(bobID, 60, nil, nora)
	e.f.ruleHits = append(e.f.ruleHits, late)
	if n, _ = e.h.LookRule(ctx, stored); n != 1 {
		t.Errorf("a photo that settles later is found: %d", n)
	}
	if after, _ := e.st.RuleByID(ctx, r.ID); after.LastLook.IsZero() || after.Problem != "" {
		t.Errorf("the look is recorded: %+v", after)
	}
}

func TestAPhraseNeedsSmartSearchAndSaysSoWhenTheServerHasNone(t *testing.T) {
	e, ctx := ruleEnv(t)
	r, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Phrase: "a beach"})
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := e.st.RuleByID(ctx, r.ID)
	e.f.smartOff = true
	if _, err := e.h.LookRule(ctx, stored); err == nil {
		t.Fatal("no smart search")
	}
	if after, _ := e.st.RuleByID(ctx, r.ID); !strings.Contains(after.Problem, "cannot do a smart search") {
		t.Errorf("said plainly: %q", after.Problem)
	}
	e.f.smartOff = false
	e.f.ruleHits = []map[string]any{hit(bobID, 60, nil)}
	if n, err := e.h.LookRule(ctx, stored); err != nil || n != 1 {
		t.Fatalf("a smart search finds a photo: %d %v", n, err)
	}
	if after, _ := e.st.RuleByID(ctx, r.ID); after.Problem != "" {
		t.Errorf("and the problem goes: %q", after.Problem)
	}
}

func TestAnsweringASuggestionSendsDeclinesOrAlwaysSendsAndOnlyTheOwnerCanAnswer(t *testing.T) {
	e, ctx := ruleEnv(t)
	r, _ := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", People: []uuid.UUID{uuid.MustParse(noraID)}})
	a, b, c := hit(bobID, 60, nil, noraID), hit(bobID, 60, nil, noraID), hit(bobID, 60, nil, noraID)
	e.f.ruleHits = []map[string]any{a, b, c}
	stored, _ := e.st.RuleByID(ctx, r.ID)
	if n, err := e.h.LookRule(ctx, stored); err != nil || n != 3 {
		t.Fatalf("three suggestions: %d %v", n, err)
	}
	sg, err := e.h.Suggestions(ctx, bob.ID)
	if err != nil || len(sg) != 1 || len(sg[0].Photos) != 3 {
		t.Fatalf("they are listed: %+v %v", sg, err)
	}
	if err := e.h.AnswerSuggestion(ctx, ann, r.ID, hub.AnswerSend, nil); !errors.Is(err, hub.ErrNoRule) {
		t.Errorf("someone else cannot answer: %v", err)
	}
	// a review: only a is ticked; b and c are left out
	if err := e.h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerSend, []string{a["id"].(string)}); err != nil {
		t.Fatal(err)
	}
	if len(e.f.tagged) != 1 || e.f.tagged[0] != a["id"] {
		t.Errorf("the ticked photo is tagged with the service's tag: %v", e.f.tagged)
	}
	if len(e.f.tagNames) == 0 || e.f.tagNames[len(e.f.tagNames)-1] != "share" {
		t.Errorf("with the service's tag: %v", e.f.tagNames)
	}
	if !has(e.woken, "a rule sent photos: "+bob.ID.String()) {
		t.Errorf("a pass is started: %v", e.woken)
	}
	states, _ := e.st.RulePhotoStates(ctx, r.ID)
	if states[a["id"].(string)] != "sent" || states[b["id"].(string)] != "declined" || states[c["id"].(string)] != "declined" {
		t.Errorf("sent and declined: %v", states)
	}
	if err := e.h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerSend, nil); !errors.Is(err, hub.ErrNoSuggestion) {
		t.Errorf("nothing waits any more: %v", err)
	}
	// Always sends and counts; Not these only declines
	d, f := hit(bobID, 60, nil, noraID), hit(bobID, 60, nil, noraID)
	e.f.ruleHits = []map[string]any{d, f}
	e.h.LookRule(ctx, stored)
	if err := e.h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerAlways, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.st.RuleByID(ctx, r.ID); got.Always != 1 {
		t.Errorf("one Always: %d", got.Always)
	}
	g := hit(bobID, 60, nil, noraID)
	e.f.ruleHits = append(e.f.ruleHits, g)
	e.h.LookRule(ctx, stored)
	before := len(e.f.tagged)
	if err := e.h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerDecline, nil); err != nil {
		t.Fatal(err)
	}
	if len(e.f.tagged) != before {
		t.Error("declining tags nothing")
	}
	if got, _ := e.st.RuleByID(ctx, r.ID); got.Always != 1 {
		t.Errorf("declining does not count: %d", got.Always)
	}
}

func TestAMessageLinkSendsOrLeavesOutWhatARuleFoundAndWorksOnce(t *testing.T) {
	e, ctx := ruleEnv(t)
	r, _ := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", People: []uuid.UUID{uuid.MustParse(noraID)}})
	a := hit(bobID, 60, nil, noraID)
	e.f.ruleHits = []map[string]any{a}
	stored, _ := e.st.RuleByID(ctx, r.ID)
	e.h.LookRule(ctx, stored)
	send, _ := e.st.ActionMake(ctx, store.ActionLink{UserID: bobID, UserName: "Bob", Kind: hub.ActionRuleSend, Service: "family", Target: r.ID, Group: r.ID})
	no, _ := e.st.ActionMake(ctx, store.ActionLink{UserID: bobID, UserName: "Bob", Kind: hub.ActionRuleDecline, Service: "family", Target: r.ID, Group: r.ID})
	if info, ok := e.h.ActionPage(ctx, send); !ok || !strings.Contains(info.Name, "Nora") {
		t.Fatalf("the page says what it is about: %+v %v", info, ok)
	}
	if _, err := e.h.RunAction(ctx, send); err != nil {
		t.Fatal(err)
	}
	if len(e.f.tagged) != 1 || e.f.tagged[0] != a["id"] {
		t.Errorf("tagged: %v", e.f.tagged)
	}
	// the other link of the same message is no longer about anything
	if _, ok := e.h.ActionPage(ctx, no); ok {
		t.Error("nothing waits any more, so the other link is as good as unknown")
	}
	if _, err := e.h.RunAction(ctx, no); err == nil {
		t.Error("and does nothing")
	}
}

func TestARuleSendsByItselfWhenSwitchedOnOnlyNewSettledPhotosAndNeverForAPhrase(t *testing.T) {
	e, ctx := ruleEnv(t)
	r, _ := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", People: []uuid.UUID{uuid.MustParse(noraID)}})
	ph, _ := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Phrase: "a beach"})
	rr, _ := e.st.RuleByID(ctx, r.ID)
	pp, _ := e.st.RuleByID(ctx, ph.ID)
	if !hub.CanAuto(rr) || hub.CanAuto(pp) {
		t.Error("a rule that sends can be switched on at once, a phrase never")
	}
	stored := func() store.Rule { x, _ := e.st.RuleByID(ctx, r.ID); return x }
	old := hit(bobID, 600, nil, noraID) // reached Immich before the switch: stays a suggestion
	for i := 0; i < 3; i++ {
		e.f.ruleHits = []map[string]any{hit(bobID, 60, nil, noraID)}
		e.h.LookRule(ctx, stored())
		if err := e.h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerAlways, nil); err != nil {
			t.Fatal(err)
		}
	}
	e.f.ruleHits = []map[string]any{old}
	e.h.LookRule(ctx, stored())
	if err := e.h.SetRuleAuto(ctx, bob, r.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := stored(); got.Mode != "auto" || got.AutoSince.IsZero() {
		t.Fatalf("automatic since now: %+v", got)
	}
	if err := e.h.SetRuleAuto(ctx, bob, ph.ID, true); !errors.Is(err, hub.ErrAutoPhrase) {
		t.Errorf("never for a phrase: %v", err)
	}
	before := len(e.f.tagged)
	fresh, young := hit(bobID, 0, func(m map[string]any) { m["createdAt"] = time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano) }, noraID), hit(bobID, 10, nil, noraID)
	// "fresh" reached Immich after the switch but only moments ago; "young" is ten minutes old (settled for a suggestion, not for auto)
	e.f.ruleHits = []map[string]any{fresh, young}
	e.h.LookRule(ctx, stored())
	if len(e.f.tagged) != before {
		t.Errorf("nothing is sent before a photo has been in Immich half an hour: %v", e.f.tagged[before:])
	}
	// a photo that reached Immich after the switch and has settled is sent without asking
	settled := hit(bobID, 40, func(m map[string]any) {
		m["createdAt"] = time.Now().Add(-40 * time.Minute).UTC().Format(time.RFC3339Nano)
	}, noraID)
	e.f.ruleHits = []map[string]any{settled}
	// the rule's "since" and auto moment: make the switch earlier than the photo
	e.st.SetRuleMode(ctx, r.ID, "auto", time.Now().Add(-2*time.Hour))
	if n, err := e.h.LookRule(ctx, stored()); err != nil || n != 0 {
		t.Fatalf("no suggestion, it was sent: %d %v", n, err)
	}
	if len(e.f.tagged) != before+1 || e.f.tagged[before] != settled["id"] {
		t.Errorf("sent on its own: %v", e.f.tagged)
	}
	// the photos from before the switch (the old one, and the ten-minute-old one, which was there when it was switched on) are only
	// suggestions
	if sg, _ := e.h.SuggestionOf(ctx, bob.ID, r.ID); len(sg.Photos) != 2 {
		t.Errorf("what was there before stays a suggestion: %+v", sg.Photos)
	}
	if err := e.h.SetRuleAuto(ctx, bob, r.ID, false); err != nil {
		t.Fatal(err)
	}
	if stored().Mode != "suggest" {
		t.Error("asks first again")
	}
}

func TestLookingBackSuggestsOlderPhotosInOneBatchAndOnlyWhenNothingWaits(t *testing.T) {
	e, ctx := ruleEnv(t)
	r, _ := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", People: []uuid.UUID{uuid.MustParse(noraID)}})
	if _, err := e.h.LookBack(ctx, bob, r.ID); !errors.Is(err, hub.ErrNothingOlder) {
		t.Errorf("nothing older: %v", err)
	}
	e.f.ruleHits = []map[string]any{hit(bobID, 60*24*400, nil, noraID), hit(bobID, 60*24*30, nil, noraID), hit(bobID, 60*24*2, func(m map[string]any) { m["isArchived"] = true }, noraID)}
	n, err := e.h.LookBack(ctx, bob, r.ID)
	if err != nil || n != 2 {
		t.Fatalf("two older photos, not the archived one: %d %v", n, err)
	}
	if _, present := e.f.lastSearch["createdAfter"]; present && e.f.lastSearch["createdAfter"] != nil {
		t.Errorf("a look back does not limit itself to new photos: %v", e.f.lastSearch)
	}
	if sg, _ := e.h.SuggestionOf(ctx, bob.ID, r.ID); len(sg.Photos) != 2 {
		t.Errorf("they wait as suggestions: %+v", sg.Photos)
	}
	if _, err := e.h.LookBack(ctx, bob, r.ID); !errors.Is(err, hub.ErrLookBackBusy) {
		t.Errorf("answer first: %v", err)
	}
	if _, err := e.h.LookBack(ctx, ann, r.ID); !errors.Is(err, hub.ErrNoRule) {
		t.Errorf("only the owner: %v", err)
	}
}

func TestTwoRulesThatFindTheSamePhotoHandItOverOnce(t *testing.T) {
	e, ctx := ruleEnv(t)
	nora := uuid.MustParse(noraID)
	one, _ := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", People: []uuid.UUID{nora}})
	two, _ := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Country: "Portugal"})
	p := hit(bobID, 60, nil, noraID)
	e.f.ruleHits = []map[string]any{p}
	a, _ := e.st.RuleByID(ctx, one.ID)
	b, _ := e.st.RuleByID(ctx, two.ID)
	e.h.LookRule(ctx, a)
	e.h.LookRule(ctx, b) // both suggest it
	if err := e.h.AnswerSuggestion(ctx, bob, one.ID, hub.AnswerSend, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.h.AnswerSuggestion(ctx, bob, two.ID, hub.AnswerSend, nil); err != nil {
		t.Fatal(err)
	}
	if len(e.f.tagged) != 1 {
		t.Errorf("handed over once, not twice: %v", e.f.tagged)
	}
	// and a rule that looks afterwards does not offer it again
	e.h.LookRule(ctx, b)
	if sg, _ := e.h.SuggestionOf(ctx, bob.ID, two.ID); len(sg.Photos) != 0 {
		t.Errorf("nothing waits: %+v", sg.Photos)
	}
	// the same rule for a service the person has not joined is refused, and says which
	if _, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Phrase: "x", Also: []string{"nowhere"}}); err == nil || !strings.Contains(err.Error(), "nowhere") {
		t.Errorf("a service they did not join: %v", err)
	}
}

func TestASendRuleSwitchedOnAtOnceSendsNothingThatWasThereBeforeAndOffAsksAgain(t *testing.T) {
	e, ctx := ruleEnv(t)
	r, _ := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", People: []uuid.UUID{uuid.MustParse(noraID)}})
	stored := func() store.Rule { x, _ := e.st.RuleByID(ctx, r.ID); return x }
	if stored().Always != 0 {
		t.Fatal("no Always answered yet")
	}
	before := hit(bobID, 600, nil, noraID) // there before the switch
	e.f.ruleHits = []map[string]any{before}
	e.h.LookRule(ctx, stored())
	if err := e.h.SetRuleAuto(ctx, bob, r.ID, true); err != nil {
		t.Fatalf("on at once, with no Always: %v", err)
	}
	if got := stored(); got.Mode != "auto" || got.AutoSince.IsZero() {
		t.Fatalf("on since now: %+v", got)
	}
	sent := len(e.f.tagged)
	e.st.SetRuleMode(ctx, r.ID, "auto", time.Now().Add(-2*time.Hour))
	e.h.LookRule(ctx, stored())
	if len(e.f.tagged) != sent {
		t.Errorf("a photo that was there before the switch is not sent: %v", e.f.tagged[sent:])
	}
	after := hit(bobID, 40, func(m map[string]any) {
		m["createdAt"] = time.Now().Add(-40 * time.Minute).UTC().Format(time.RFC3339Nano)
	}, noraID)
	e.f.ruleHits = []map[string]any{after}
	e.h.LookRule(ctx, stored())
	if len(e.f.tagged) != sent+1 || e.f.tagged[sent] != after["id"] {
		t.Errorf("a settled photo that arrived after it is sent: %v", e.f.tagged)
	}
	if err := e.h.SetRuleAuto(ctx, bob, r.ID, false); err != nil {
		t.Fatal(err)
	}
	e.f.ruleHits = []map[string]any{hit(bobID, 45, func(m map[string]any) {
		m["createdAt"] = time.Now().Add(-45 * time.Minute).UTC().Format(time.RFC3339Nano)
	}, noraID)}
	n := len(e.f.tagged)
	e.h.LookRule(ctx, stored())
	if len(e.f.tagged) != n {
		t.Error("switched off, it only suggests")
	}
}
