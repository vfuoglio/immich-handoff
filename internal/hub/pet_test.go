package hub_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// examples makes n photos of bob's own, to use as the examples of a pet rule.
func examples(e *env, n int) []uuid.UUID {
	if e.f.mine == nil {
		e.f.mine = map[string]string{}
	}
	var out []uuid.UUID
	for i := 0; i < n; i++ {
		id := uuid.New()
		e.f.mine[id.String()] = bobID.String()
		out = append(out, id)
	}
	return out
}

// lookalikes is n photos Immich ranks as the nearest to an example, nearest first.
func lookalikes(n int) []map[string]any {
	var out []map[string]any
	for i := 0; i < n; i++ {
		// arrives just after the rule was made; the hub's clock is put two hours on, so it has settled
		out = append(out, hit(bobID, 0, func(m map[string]any) { m["createdAt"] = time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano) }))
	}
	return out
}

func TestAPetRuleNeedsThreeToEightOfThePersonsOwnPhotosAndNothingElse(t *testing.T) {
	e, ctx := ruleEnv(t)
	ex := examples(e, 9)
	for _, n := range []int{1, 2, 9} {
		if _, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Examples: ex[:n]}); !errors.Is(err, hub.ErrBadExamples) || !hub.IsUserError(err) {
			t.Errorf("%d examples: %v", n, err)
		}
	}
	if _, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Examples: []uuid.UUID{ex[0], ex[0], ex[1]}}); !errors.Is(err, hub.ErrBadExamples) {
		t.Errorf("the same photo twice: %v", err)
	}
	strange := uuid.New() // not known to bob's Immich
	if _, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Examples: []uuid.UUID{ex[0], ex[1], strange}}); !errors.Is(err, hub.ErrBadExamples) {
		t.Errorf("a photo that is not there: %v", err)
	}
	e.f.mine[ex[2].String()] = annID.String() // somebody else's photo
	if _, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Examples: ex[:3]}); !errors.Is(err, hub.ErrBadExamples) {
		t.Errorf("another person's photo: %v", err)
	}
	e.f.mine[ex[2].String()] = bobID.String()
	if _, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Examples: ex[:3], Phrase: "a dog"}); !errors.Is(err, hub.ErrPetAlone) {
		t.Errorf("a pet rule stands alone: %v", err)
	}
	if _, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Examples: ex[:3], People: []uuid.UUID{uuid.MustParse(noraID)}}); !errors.Is(err, hub.ErrPetAlone) {
		t.Errorf("not with people either: %v", err)
	}
	r, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Examples: ex[:3]})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Cond.Pet() || r.Cond.Rank != 10 || len(r.Cond.Examples) != 3 || !strings.Contains(hub.RuleSentence(r.Cond), "look like your 3 example photos") {
		t.Errorf("%+v %q", r.Cond, hub.RuleSentence(r.Cond))
	}
}

func TestAPetRuleOffersTheNearestTenAtFirstLearnsFromWhatIsAcceptedNeverGoesByItselfAndNeverOffersADeclinedPhotoAgain(t *testing.T) {
	e, ctx := ruleEnv(t)
	ex := examples(e, 3)
	r, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Examples: ex})
	if err != nil {
		t.Fatal(err)
	}
	e.h.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	list := lookalikes(40)
	e.f.similar = map[string][]map[string]any{ex[0].String(): list, ex[1].String(): list[:2]}
	stored := func() store.Rule { x, _ := e.st.RuleByID(ctx, r.ID); return x }
	if n, err := e.h.LookRule(ctx, stored()); err != nil || n != 10 {
		t.Fatalf("the nearest ten to begin with: %d %v", n, err)
	}
	if len(e.f.similarAsked) != 3 {
		t.Errorf("each example is asked about: %v", e.f.similarAsked)
	}
	sg, _ := e.h.SuggestionOf(ctx, bob.ID, r.ID)
	if len(sg.Photos) != 10 || !strings.Contains(sg.Photos[0].Why, "(rank 1)") || !strings.Contains(sg.Photos[9].Why, "(rank 10)") {
		t.Fatalf("each says how near it was: %+v", sg.Photos)
	}
	// the person sends two of them, the farthest of which was number 9, and declines the rest
	id := func(i int) string { return list[i]["id"].(string) }
	if err := e.h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerSend, []string{id(2), id(8)}); err != nil {
		t.Fatal(err)
	}
	got := stored()
	if got.Cond.Rank != 15 { // 1.5 x 9, rounded up, plus one
		t.Errorf("the rank grows to 1.5 times the worst accepted, plus one: %d", got.Cond.Rank)
	}
	if len(got.Cond.Examples) != 5 {
		t.Errorf("each accepted photo becomes an example: %v", got.Cond.Examples)
	}
	if got.Always != 0 || got.Mode != store.RuleSuggest {
		t.Errorf("nothing about it goes by itself: %+v", got)
	}
	// the declined ones are never offered again, the examples never are, and the wider rank brings the next ones
	e.f.similar[ex[0].String()] = list
	n, err := e.h.LookRule(ctx, stored())
	if err != nil {
		t.Fatal(err)
	}
	sg, _ = e.h.SuggestionOf(ctx, bob.ID, r.ID)
	seen := map[string]bool{}
	for _, p := range sg.Photos {
		if seen[p.AssetID] {
			t.Errorf("twice: %s", p.AssetID)
		}
		seen[p.AssetID] = true
		for i := 0; i < 10; i++ {
			if i != 2 && i != 8 && p.AssetID == id(i) {
				t.Errorf("a declined photo is offered again: rank %d", i+1)
			}
		}
	}
	if n == 0 || len(sg.Photos) == 0 {
		t.Fatalf("the wider rank brings more: %d %+v", n, sg)
	}
	// a pet rule never goes by itself and never has Always
	if hub.CanAuto(stored()) {
		t.Error("CanAuto")
	}
	if err := e.h.SetRuleAuto(ctx, bob, r.ID, true); !errors.Is(err, hub.ErrAutoPet) || !hub.IsUserError(err) {
		t.Errorf("never by itself: %v", err)
	}
	// the examples grow only to a limit
	for i := 0; i < 20; i++ {
		e.f.similar[ex[0].String()] = lookalikes(30)
		e.h.LookRule(ctx, stored())
		sg, _ = e.h.SuggestionOf(ctx, bob.ID, r.ID)
		if len(sg.Photos) == 0 {
			break
		}
		e.h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerSend, nil)
	}
	if len(stored().Cond.Examples) > 12 {
		t.Errorf("at most twelve examples: %d", len(stored().Cond.Examples))
	}
	_ = fmt.Sprint
}

func TestAPetRuleSaysPlainlyThatTheServerCannotDoASmartSearch(t *testing.T) {
	e, ctx := ruleEnv(t)
	r, _ := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Examples: examples(e, 3)})
	e.f.smartOff = true
	stored := func() store.Rule { x, _ := e.st.RuleByID(ctx, r.ID); return x }
	if _, err := e.h.LookRule(ctx, stored()); err == nil {
		t.Fatal("it cannot look")
	}
	if p := stored().Problem; !strings.Contains(p, "cannot do a smart search") || !strings.Contains(p, "photos like your examples") {
		t.Errorf("the rule says why: %q", p)
	}
	e.f.smartOff = false
	e.f.similar = map[string][]map[string]any{}
	if _, err := e.h.LookRule(ctx, stored()); err != nil || stored().Problem != "" {
		t.Errorf("and recovers: %v %q", err, stored().Problem)
	}
}

func TestAPetRuleRanksTheWholeLibraryAndOnlyThenKeepsWhatArrivedAfterTheRule(t *testing.T) {
	e, ctx := ruleEnv(t)
	ex := examples(e, 3)
	r, _ := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Examples: ex})
	e.h.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	old := hit(bobID, 300, nil) // there before the rule: ranked first, but not offered
	list := append([]map[string]any{old}, lookalikes(3)...)
	e.f.similar = map[string][]map[string]any{ex[0].String(): list}
	stored, _ := e.st.RuleByID(ctx, r.ID)
	if n, err := e.h.LookRule(ctx, stored); err != nil || n != 3 {
		t.Fatalf("only what arrived after the rule: %d %v", n, err)
	}
	sg, _ := e.h.SuggestionOf(ctx, bob.ID, r.ID)
	if !strings.Contains(sg.Photos[0].Why, "(rank 2)") {
		t.Errorf("the rank counts the photo that was left out, because the ranking is of the whole library: %q", sg.Photos[0].Why)
	}
}
