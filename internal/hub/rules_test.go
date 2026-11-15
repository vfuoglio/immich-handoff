package hub_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
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
