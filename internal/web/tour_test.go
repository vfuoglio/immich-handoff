package web_test

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/docs"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

func tourJSON(t *testing.T, body string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<script id="tour" type="application/json">(.*?)</script>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("the page carries no tour")
	}
	return m[1]
}

func TestTheTourStartsByItselfOnceAndIsRememberedPerPerson(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	j := tourJSON(t, get(t, app.URL, "sess=family"))
	has(t, j, `"kind":"user"`, `"auto":true`, `"target":"needs"`, `"target":"offer"`, `"target":"palette"`, `"url":"help/household"`, `"selector":"#v-services details.offer"`)
	if strings.Contains(j, "admin-share") && strings.Contains(j, `"kind":"admin"`) {
		t.Error("the tour of the page carries the administrator's stops")
	}
	if code, _ := postForm(t, app.URL+"/tour/seen", "sess=family", "kind=user"); code != 204 {
		t.Fatalf("seen: %d", code)
	}
	has(t, tourJSON(t, get(t, app.URL, "sess=family")), `"auto":false`)
	// it is remembered for that person only
	has(t, tourJSON(t, get(t, app.URL, "sess=admin")), `"auto"`)
	if seen, _ := st.TourSeen(context.Background(), uuid.MustParse(familyID), "user"); !seen {
		t.Error("not stored")
	}
}

func TestTheTourWaitsWhileTagsAreOffAndTheRequestIsChecked(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	has(t, tourJSON(t, get(t, app.URL, "sess=other")), `"auto":false`) // Tags are off for "other": the page is switched off, so is the tour
	if code, _ := postForm(t, app.URL+"/tour/seen", "", "kind=user"); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/tour/seen", "sess=family", "kind=nonsense"); code != 400 {
		t.Errorf("an unknown tour: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/tour/seen", "sess=family", "kind=admin"); code != 400 {
		t.Errorf("a user marks the administrator's tour seen: %d", code)
	}
	if code, _ := do(t, "POST", app.URL+"/tour/seen", "sess=family", nil); code != 400 {
		t.Errorf("without the guard header: %d", code)
	}
}

func TestTheAdministratorsPageHasATourOfItsOwn(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	_, body := do(t, "GET", app.URL+"/admin", "sess=admin", nil)
	j := tourJSON(t, body)
	has(t, j, `"kind":"admin"`, `"auto":true`, `"target":"admin-share"`, `"target":"admin-backups"`, `"url":"help/operations"`)
	for _, id := range []string{"adm-share", "adm-connections", "adm-services"} { // the backups and the pet helper are on the page only when they are set up; the tour leaves their stops out otherwise
		has(t, body, `id="`+id+`"`)
	}
	if code, _ := postForm(t, app.URL+"/tour/seen", "sess=admin", "kind=admin"); code != 204 {
		t.Fatalf("seen: %d", code)
	}
	_, after := do(t, "GET", app.URL+"/admin", "sess=admin", nil)
	has(t, tourJSON(t, after), `"auto":false`)
	// the user tour is a separate flag
	has(t, tourJSON(t, get(t, app.URL, "sess=admin")), `"auto"`)
}

func TestEveryNamedTargetIsSomewhereOnThePage(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	j := tourJSON(t, get(t, app.URL, "sess=family"))
	for _, key := range docs.TourTargets {
		if !strings.Contains(j, `"`+key+`":{`) {
			t.Errorf("the target %q has no place in the page's table", key)
		}
	}
	// and the table names nothing that the guides cannot
	src, err := os.ReadFile("tour.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`(?m)^\t"([a-z-]+)":\s+\{`).FindAllStringSubmatch(string(src), -1) {
		found := false
		for _, k := range docs.TourTargets {
			found = found || k == m[1]
		}
		if !found {
			t.Errorf("tour.go places %q, which the documentation cannot name", m[1])
		}
	}
	// the selectors the user's stops point at really are in the page (the services and the settings views are in it, hidden)
	page := get(t, app.URL, "sess=family")
	has(t, page, `id="v-overview"`, `id="v-services"`, `data-view="services"`, `class="icon-btn help-btn"`, `id="palette"`)
}

func TestTheGetStartedCardAdaptsToWhoYouAre(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	// someone with no service sees the first thing to do, and no step about inviting
	postForm(t, app.URL+"/preferences/tags", "sess=admin", "")
	g := get(t, app.URL, "sess=admin")
	has(t, g, "Join a service, or offer one of your own", `data-show="offer"`, "Take the one-minute tour of the page")
	lacks(t, g, "Invite someone to send to your service")
	// an owner is also told to invite someone (the step is there, done or not, for a person who owns a service)
	has(t, get(t, app.URL, "sess=family"), "Invite someone to send to your service")
}
