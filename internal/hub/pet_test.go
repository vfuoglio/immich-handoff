package hub_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/pets"
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

// ---- the optional sidecar, against a stub ----

type stubPets struct {
	t      *testing.T
	prints map[string][]pets.Animal // photo id -> the animals the stub finds in it
	down   bool
	asked  int
}

func (s *stubPets) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.down {
			http.Error(w, "down", 500)
			return
		}
		if r.URL.Path == "/ping" {
			w.Write([]byte("ok"))
			return
		}
		b, _ := io.ReadAll(r.Body)
		s.asked++
		id := strings.TrimPrefix(string(b), "PHOTO:")
		json.NewEncoder(w).Encode(map[string]any{"animals": s.prints[id]})
	})
}

func animal(score float64, v ...float32) []pets.Animal {
	return []pets.Animal{{Kind: "dog", Score: 0.9, Box: [4]float64{0.1, 0.2, 0.5, 0.5}, Print: v}}
}

// petSidecarEnv makes the examples and the photos with pictures the stub can tell apart, and puts the stub in place.
func petSidecarEnv(t *testing.T) (*env, context.Context, *stubPets, []uuid.UUID) {
	e, ctx := ruleEnv(t)
	st := &stubPets{t: t, prints: map[string][]pets.Animal{}}
	srv := httptest.NewServer(st.Handler())
	t.Cleanup(srv.Close)
	c, err := pets.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	e.h.Pets = c
	e.h.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	ex := examples(e, 3)
	for _, id := range ex {
		picture(e, id.String())
		st.prints[id.String()] = animal(0.9, 1, 0, 0, 0)
	}
	return e, ctx, st, ex
}

// picture gives a photo a thumbnail the stub recognises.
func picture(e *env, id string) {
	if e.f.assetName == nil {
		e.f.assetName = map[string]string{}
	}
	if e.f.thumbs == nil {
		e.f.thumbs = map[string][]byte{}
	}
	e.f.assetName[id] = "pet-" + id + ".jpg"
	e.f.thumbs["pet-"+id] = []byte("PHOTO:" + id)
}

// candidate is a photo that arrived after the rule, with the animal the stub finds in it.
func candidate(e *env, st *stubPets, a []pets.Animal) map[string]any {
	h := hit(bobID, 0, func(m map[string]any) { m["createdAt"] = time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano) })
	picture(e, h["id"].(string))
	st.prints[h["id"].(string)] = a
	return h
}

func TestAPetRuleWithTheSidecarComparesCropsLearnsItsCutoffAndTakesTheAnimalOfEachAcceptedPhoto(t *testing.T) {
	e, ctx, st, ex := petSidecarEnv(t)
	st.prints[ex[2].String()] = nil // no animal in one of the examples
	if _, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Examples: ex}); !errors.Is(err, hub.ErrNoAnimal) || !hub.IsUserError(err) {
		t.Fatalf("an example with no animal in it is refused: %v", err)
	}
	st.prints[ex[2].String()] = animal(0.9, 1, 0, 0, 0)
	r, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Examples: ex})
	if err != nil {
		t.Fatal(err)
	}
	prints, _ := e.st.PetPrints(ctx, r.ID)
	if len(prints) != 3 || prints[0].Kind != "dog" || prints[0].Box[2] != 0.5 || len(prints[0].Vec) != 4 {
		t.Fatalf("the animal of each example is kept, with where it is: %+v", prints)
	}
	links, _ := e.st.Links(ctx)
	var key string
	for _, l := range links {
		if l.Service == "family" && l.ImmichUserID == bobID {
			key = l.KeyID.String()
		}
	}
	if !slices.Contains(e.f.keyPerms[key], "asset.view") {
		t.Errorf("the key may look at previews, given through the person's own session: %v", e.f.keyPerms[key])
	}
	near := candidate(e, st, animal(0.9, 0.9, 0.436, 0, 0))   // cosine 0.90
	faint := candidate(e, st, animal(0.9, 0.46, 0.888, 0, 0)) // cosine 0.46
	far := candidate(e, st, animal(0.9, 0.3, 0.954, 0, 0))    // cosine 0.30
	none := candidate(e, st, nil)                             // no animal
	e.f.ruleHits = []map[string]any{near, faint, far, none}
	stored := func() store.Rule { x, _ := e.st.RuleByID(ctx, r.ID); return x }
	if n, err := e.h.LookRule(ctx, stored()); err != nil || n != 2 {
		t.Fatalf("the two that are like the pet: %d %v", n, err)
	}
	sg, _ := e.h.SuggestionOf(ctx, bob.ID, r.ID)
	if len(sg.Photos) != 2 || sg.Photos[0].AssetID != near["id"] || !strings.Contains(sg.Photos[0].Why, "(match 0.90)") || !strings.Contains(sg.Photos[1].Why, "(match 0.46)") {
		t.Fatalf("the nearest first, each saying how like: %+v", sg.Photos)
	}
	before := st.asked
	e.h.LookRule(ctx, stored())
	if st.asked != before {
		t.Errorf("a photo that was looked at is not sent to the sidecar again: %d more", st.asked-before)
	}
	// the person sends the faint one: the cutoff moves just under it, and its animal becomes one more example
	if err := e.h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerSend, []string{faint["id"].(string)}); err != nil {
		t.Fatal(err)
	}
	if c := stored().Cond.Cutoff; c < 0.43 || c > 0.45 {
		t.Errorf("the cutoff moves to just under the worst accepted: %v", c)
	}
	if p, _ := e.st.PetPrints(ctx, r.ID); len(p) != 4 {
		t.Errorf("the accepted photo's animal is one more example: %d", len(p))
	}
	if stored().Mode != store.RuleSuggest || hub.CanAuto(stored()) {
		t.Error("never by itself")
	}
	// everything turned down: the cutoff goes above the best that was turned down
	again := candidate(e, st, animal(0.9, 0.5, 0.866, 0, 0)) // cosine 0.50 with the first examples
	e.f.ruleHits = []map[string]any{again}
	e.h.LookRule(ctx, stored())
	if err := e.h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerDecline, nil); err != nil {
		t.Fatal(err)
	}
	if c := stored().Cond.Cutoff; c <= 0.5 {
		t.Errorf("the cutoff rises above what was turned down: %v", c)
	}
}

func TestAPetRuleSaysSoAndUsesImmichsSearchWhenTheSidecarIsDownAndKeysLoseTheirPermissionWhenItIsOff(t *testing.T) {
	e, ctx, st, ex := petSidecarEnv(t)
	r, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Examples: ex})
	if err != nil {
		t.Fatal(err)
	}
	stored := func() store.Rule { x, _ := e.st.RuleByID(ctx, r.ID); return x }
	e.f.similar = map[string][]map[string]any{ex[0].String(): lookalikes(5)}
	st.down = true
	if n, err := e.h.LookRule(ctx, stored()); err != nil || n != 5 {
		t.Fatalf("it falls back to Immich's own search: %d %v", n, err)
	}
	if p := stored().Problem; !strings.Contains(p, "pet helper is not answering") {
		t.Errorf("and says so: %q", p)
	}
	st.down = false
	e.f.ruleHits = nil
	if _, err := e.h.LookRule(ctx, stored()); err != nil || stored().Problem != "" {
		t.Errorf("it recovers: %v %q", err, stored().Problem)
	}
	// a rule made while the sidecar is down works the Immich way, with no fingerprints
	st.down = true
	r2, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Examples: ex})
	if err != nil {
		t.Fatalf("made while it is down: %v", err)
	}
	if p, _ := e.st.PetPrints(ctx, r2.ID); len(p) != 0 {
		t.Error("nothing to compare yet")
	}
	// the sidecar is switched off: the permission to look at previews is taken off the key when the person opens the page
	st.down = false
	links, _ := e.st.Links(ctx)
	var key string
	for _, l := range links {
		if l.Service == "family" && l.ImmichUserID == bobID {
			key = l.KeyID.String()
		}
	}
	if !slices.Contains(e.f.keyPerms[key], "asset.view") {
		t.Fatalf("it has it: %v", e.f.keyPerms[key])
	}
	e.h.Pets = nil
	e.h.EnsureKeyPermissions(ctx, e.sess, bob)
	if slices.Contains(e.f.keyPerms[key], "asset.view") {
		t.Errorf("and loses it again: %v", e.f.keyPerms[key])
	}
}
