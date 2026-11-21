package web_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/pets"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/web"
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

func TestAPetRuleThatUsesThePetHelperShowsTheAnimalFoundInEachExampleAndTheFormSaysTheHelperIsOn(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withPairings(t, &st))
	ctx := context.Background()
	_, form := do(t, "GET", app.URL+"/fragments/pet-form?service=club", "sess=other", nil)
	if strings.Contains(form, "pet helper on") {
		t.Error("the form does not mention a helper that is not there")
	}
	r, err := st.AddRule(ctx, store.Rule{OwnerID: mustUUID(otherID), Service: "club", Cond: store.RuleCond{Examples: []string{"55555555-0000-4000-8000-0000000000c1", "55555555-0000-4000-8000-0000000000c2", "55555555-0000-4000-8000-0000000000c3"}, Rank: 10}})
	if err != nil {
		t.Fatal(err)
	}
	st.PutPetPrint(ctx, r.ID, store.PetPrint{Asset: "55555555-0000-4000-8000-0000000000c1", Kind: "dog", Box: [4]float64{0.1, 0.2, 0.5, 0.4}, Vec: []float32{1, 0}})
	_, page := do(t, "GET", app.URL+"/", "sess=other", nil)
	for _, want := range []string{"The animal the pet helper found in each of your examples", "rules/pet-photo/55555555-0000-4000-8000-0000000000c1", "left:10%;top:20%;width:50%;height:40%"} {
		if !strings.Contains(page, want) {
			t.Errorf("the card lacks %q", want)
		}
	}
}

func TestThePetFormSaysTheHelperIsOnWhenItIs(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withPairings(t, &st), func(s *web.Server) { s.Hub.Pets, _ = pets.New("http://pets.invalid:8090") })
	_, form := do(t, "GET", app.URL+"/fragments/pet-form?service=club", "sess=other", nil)
	if !strings.Contains(form, "This server has the pet helper on") || !strings.Contains(form, "keeps nothing") {
		t.Error("the form says what the helper is and does")
	}
}

// Without the helper the form still works, and says so, with where to read how to switch the helper on; the administrator's page
// says whether the helper is off, on or not answering.
func TestThePetFormAndTheAdminPageSayWhenTheHelperIsOff(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withPairings(t, &st))
	_, form := do(t, "GET", app.URL+"/fragments/pet-form?service=club", "sess=other", nil)
	if !strings.Contains(form, "This works with Immich") || !strings.Contains(form, "guides/automate/pets.md#the-optional-pet-helper") || strings.Contains(form, "This server has the pet helper on") {
		t.Errorf("the form without the helper: %.600s", form)
	}
	_, admin := do(t, "GET", app.URL+"/admin", "sess=admin", nil)
	if !strings.Contains(admin, `id="pethelper"`) || !strings.Contains(admin, "Rules for a pet work without it") {
		t.Errorf("the administrator's page says the helper is off")
	}
}

// The Pets card is in Settings, the sharing forms offer a pet, and the photo picker for it is its own fragment.
func TestThePetsCardAndTheSharingFormsOfferAPet(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withPairings(t, &st))
	body := get(t, app.URL, "sess=other")
	for _, want := range []string{`class="card petscard"`, "Two places to start one:", "pet helper"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	_, form := do(t, "GET", app.URL+"/fragments/pair-form", "sess=other", nil)
	if !strings.Contains(form, "Also share photos of a pet") || !strings.Contains(form, `hx-get="fragments/pet-pick"`) {
		t.Errorf("the offer form can add a pet: %.400s", form)
	}
	if code, pick := do(t, "GET", app.URL+"/fragments/pet-pick", "sess=other", nil); code != 200 || !strings.Contains(pick, "Pick 3 to 8 photos of the animal") {
		t.Errorf("the picker: %d %.200s", code, pick)
	}
}
