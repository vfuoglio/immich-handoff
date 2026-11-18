package hub_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Immich never says that it has finished looking for faces, so how long a photo waits before a rule offers it is the person's
// choice for each rule: the app's usual wait (five minutes) unless they chose another, and six times that before it goes by itself.
func TestARuleCanChooseItsOwnWaitBeforeAPhotoIsOfferedAndTheChoiceIsKept(t *testing.T) {
	e, ctx := ruleEnv(t)
	nora := []uuid.UUID{uuid.MustParse(noraID)}
	usual, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", People: nora})
	if err != nil {
		t.Fatal(err)
	}
	short, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", People: nora, SettleMinutes: 2})
	if err != nil {
		t.Fatal(err)
	}
	if usual.SettleMinutes != 0 || short.SettleMinutes != 2 {
		t.Fatalf("the choice is on the rule: %d %d", usual.SettleMinutes, short.SettleMinutes)
	}
	if got := e.h.RuleWait(); got != 5*time.Minute {
		t.Errorf("the usual wait is five minutes: %v", got)
	}
	three, one := hit(bobID, 3, nil, noraID), hit(bobID, 1, nil, noraID)
	e.f.ruleHits = []map[string]any{three, one}
	for _, r := range []store.Rule{usual, short} {
		stored, _ := e.st.RuleByID(ctx, r.ID)
		if _, err := e.h.LookRule(ctx, stored); err != nil {
			t.Fatal(err)
		}
	}
	offered := func(r store.Rule) []string {
		var ids []string
		got, _ := e.st.RulePhotos(ctx, r.ID, store.RuleSuggested)
		for _, p := range got {
			ids = append(ids, p.AssetID)
		}
		return ids
	}
	if got := offered(usual); len(got) != 0 {
		t.Errorf("the usual wait does not offer a photo three minutes old: %v", got)
	}
	if got := offered(short); len(got) != 1 || got[0] != three["id"] {
		t.Errorf("a rule that waits two minutes offers the three-minute-old photo and not the one-minute-old: %v", got)
	}
	// the choice is kept as the rule is stored and read back, and listed
	if list, _ := e.h.RulesFor(ctx, bob.ID, "family"); len(list) != 2 || list[0].SettleMinutes != 0 || list[1].SettleMinutes != 2 {
		t.Errorf("kept: %+v", list)
	}
	// only a wait that makes sense
	for _, n := range []int{-1, 61, 1000} {
		if _, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", People: nora, SettleMinutes: n}); !errors.Is(err, hub.ErrBadSettle) || !hub.IsUserError(err) {
			t.Errorf("%d minutes: %v", n, err)
		}
	}
	if _, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", People: nora, SettleMinutes: 60}); err != nil {
		t.Errorf("an hour is the longest: %v", err)
	}
}

func TestARuleThatSendsByItselfWaitsSixTimesItsOwnWait(t *testing.T) {
	e, ctx := ruleEnv(t)
	nora := []uuid.UUID{uuid.MustParse(noraID)}
	usual, _ := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", People: nora})
	short, _ := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", People: nora, SettleMinutes: 2})
	for _, r := range []store.Rule{usual, short} { // switched on two hours ago, so the photos below are all "after the switch"
		if err := e.st.SetRuleMode(ctx, r.ID, store.RuleAuto, time.Now().Add(-2*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	fifteen := hit(bobID, 15, nil, noraID) // more than six times two minutes, less than six times five
	e.f.ruleHits = []map[string]any{fifteen}
	before := len(e.f.tagged)
	stored, _ := e.st.RuleByID(ctx, usual.ID)
	if _, err := e.h.LookRule(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if len(e.f.tagged) != before {
		t.Errorf("the usual rule waits half an hour before it sends by itself: %v", e.f.tagged[before:])
	}
	stored, _ = e.st.RuleByID(ctx, short.ID)
	if _, err := e.h.LookRule(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if len(e.f.tagged) != before+1 || e.f.tagged[before] != fifteen["id"] {
		t.Errorf("the rule that waits two minutes sends it after twelve: %v", e.f.tagged[before:])
	}
}
