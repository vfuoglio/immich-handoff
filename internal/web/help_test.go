package web_test

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/docs"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

func TestHelpIsForPeopleWhoAreSignedIn(t *testing.T) {
	app, _ := setup(t)
	for _, path := range []string{"/help", "/help/stories", "/help/search?q=story", "/help/img/hero.jpg", "/fragments/help/stories"} {
		if code, _ := do(t, "GET", app.URL+path, "", nil); code != 401 && code != 403 {
			t.Errorf("%s without signing in: %d", path, code)
		}
	}
}

func TestTheHelpPageListsTheGuidesByWhatYouWantToDo(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	code, body := do(t, "GET", app.URL+"/help", "sess=family", nil)
	if code != 200 {
		t.Fatal(code)
	}
	has(t, body, "<h1>Help</h1>", `id="help-q"`, "Start here", ">Share<", `href="help/stories"`, `href="help/rules"`)
	if strings.Contains(body, `href="help/install"`) {
		t.Error("a person who is not an administrator is shown the administrator's guides")
	}
	_, body = do(t, "GET", app.URL+"/help", "sess=admin", nil)
	has(t, body, `href="help/install"`, `href="help/admin-page"`)
}

func TestAGuideIsRenderedWithItsLinksAndPicturesInsideTheApp(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	code, body := do(t, "GET", app.URL+"/help/household", "sess=family", nil)
	if code != 200 {
		t.Fatal(code)
	}
	has(t, body, `<base href="../">`, `class="help-body" data-feature="household"`, `src="help/img/`, `href="help/`)
	if strings.Contains(body, ".md\"") || strings.Contains(body, `src="http`) {
		t.Error("a link still points at a file, or a picture comes from another site")
	}
	if n := strings.Count(body, "<h1"); n != 1 {
		t.Errorf("the title is shown %d times", n)
	}
	// the picture it names is served from the same address, as a JPEG
	img := regexp.MustCompile(`src="(help/img/[^"]+)"`).FindStringSubmatch(body)
	if img == nil {
		t.Fatal("no picture")
	}
	if code, b := do(t, "GET", app.URL+"/"+img[1], "sess=family", nil); code != 200 || len(b) < 1000 {
		t.Errorf("the picture: %d, %d bytes", code, len(b))
	}
	if code, _ := do(t, "GET", app.URL+"/help/img/nothing.jpg", "sess=family", nil); code != 404 {
		t.Errorf("a picture that does not exist: %d", code)
	}
}

func TestAnAdministratorsGuideIsNotFoundByAnyoneElse(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	if code, _ := do(t, "GET", app.URL+"/help/install", "sess=family", nil); code != 404 {
		t.Errorf("a user opens the install guide: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/fragments/help/install", "sess=family", nil); code != 404 {
		t.Errorf("a user opens the install guide in the drawer: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/help/install", "sess=admin", nil); code != 200 {
		t.Errorf("an administrator does not: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/help/nothing-here", "sess=family", nil); code != 404 {
		t.Errorf("a guide that does not exist: %d", code)
	}
}

func TestTheSearchFindsGuidesAndKeepsAdministratorsGuidesBack(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	type hit struct{ Feature, Title, URL string }
	search := func(cookie, q string) []hit {
		code, body := do(t, "GET", app.URL+"/help/search?q="+q, cookie, nil)
		if code != 200 {
			t.Fatalf("%s: %d", q, code)
		}
		var hits []hit
		if err := json.Unmarshal([]byte(body), &hits); err != nil {
			t.Fatal(err, body)
		}
		return hits
	}
	if h := search("sess=family", "storys"); len(h) == 0 || h[0].Feature != "stories" || h[0].URL != "help/stories" {
		t.Errorf("%+v", h)
	}
	for _, h := range search("sess=family", "install") {
		if h.Feature == "install" {
			t.Error("a user finds the install guide")
		}
	}
	found := false
	for _, h := range search("sess=admin", "install") {
		found = found || h.Feature == "install"
	}
	if !found {
		t.Error("an administrator does not find the install guide")
	}
}

func TestTheDrawerHoldsTheGuideWithoutThePageAroundIt(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	code, body := do(t, "GET", app.URL+"/fragments/help/stories", "sess=family", nil)
	if code != 200 {
		t.Fatal(code)
	}
	has(t, body, `class="help-drawer-head"`, `href="help/stories"`, ">Open the full page<", `class="help-body" data-feature="stories"`)
	if strings.Contains(body, "<html") || strings.Contains(body, "<header class=\"bar\"") {
		t.Error("the drawer carries the whole page")
	}
}

func TestEveryPageHasTheHelpButtonTheDrawerAndTheFooterLink(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	for _, path := range []string{"/", "/help", "/help/household"} {
		_, body := do(t, "GET", app.URL+path, "sess=family", nil)
		has(t, body, `class="icon-btn help-btn"`, `id="help-drawer"`, `class="help-foot"`)
	}
	// the "?" of a card is there when the feature has a guide, and it names the guide
	_, body := do(t, "GET", app.URL+"/", "sess=family", nil)
	if !strings.Contains(body, `class="help-link"`) || !strings.Contains(body, `data-help="`) {
		t.Error("no card has a ? beside its heading")
	}
	for _, m := range regexp.MustCompile(`data-help="([a-z-]+)"`).FindAllStringSubmatch(body, -1) {
		if code, _ := do(t, "GET", app.URL+"/help/"+m[1], "sess=admin", nil); code != 200 {
			t.Errorf("the ? for %s opens nothing (%d)", m[1], code)
		}
	}
}

// a "?" beside a heading that asks for a guide that does not exist would show nothing, and nobody would notice
func TestEveryHelpLinkAndTipInTheTemplatesNamesARealGuide(t *testing.T) {
	src, err := os.ReadFile("views.templ")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range regexp.MustCompile(`@HelpLink\("([a-z-]+)"\)`).FindAllStringSubmatch(string(src), -1) {
		n++
		if _, _, _, ok := docs.Tip(m[1]); !ok {
			t.Errorf("@HelpLink(%q): no guide has that feature", m[1])
		}
	}
	if n < 20 {
		t.Errorf("only %d cards have a ?", n)
	}
	for _, m := range regexp.MustCompile(`tipOf\(ctx, "([a-z-]+)", "([a-z0-9-]+)"\)`).FindAllStringSubmatch(string(src), -1) {
		if _, ok := docs.ButtonTip(m[1], m[2]); !ok {
			t.Errorf("tipOf(%q, %q): the guide has no such tip", m[1], m[2])
		}
	}
}

func TestHelpSpeaksTheLanguageOfThePersonWhereItHasBeenTranslated(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	const de = "sess=admin; handoff-lang=de"
	// the lists have German titles and tips for every page, translated or not
	_, body := do(t, "GET", app.URL+"/help", de, nil)
	has(t, body, "Schnellstart: deine erste Übergabe in zehn Minuten", "Geschichten: ein teilbarer animierter Rückblick", "Fotos mit Haushalt oder Freunden teilen")
	// a translated page: its text, its picture note and the app's own words
	_, body = do(t, "GET", app.URL+"/help/quickstart", de, nil)
	has(t, body, "<h1>Schnellstart: deine erste Übergabe in zehn Minuten</h1>", "Einen eigenen Dienst anbieten", "maschinell übersetzt", `href="help/the-page"`)
	if strings.Contains(body, "Quick start") {
		t.Error("English left on a translated page")
	}
	// a page that is not translated is read in English, with a note
	_, body = do(t, "GET", app.URL+"/help/stories", de, nil)
	has(t, body, "<h1>Geschichten: ein teilbarer animierter Rückblick</h1>", "Diese Anleitung gibt es nur auf Englisch.", "Turn an album into a short animated story")
	// the drawer and the tooltip of a card, and the tour, are in German too
	_, body = do(t, "GET", app.URL+"/fragments/help/stories", de, nil)
	has(t, body, "Diese Anleitung gibt es nur auf Englisch.")
	_, body = do(t, "GET", app.URL+"/", de, nil)
	has(t, body, `title="Aus einem Album, einer Person oder einem Zeitraum einen animierten Rückblick machen, den du senden kannst."`)
	has(t, tourJSON(t, body), `"title":"Aktion nötig"`, `"title":"Dienste"`)
	// English stays English
	_, body = do(t, "GET", app.URL+"/help/quickstart", "sess=admin", nil)
	has(t, body, "<h1>Quick start: your first hand-over in ten minutes</h1>")
	if strings.Contains(body, "maschinell") {
		t.Error("a German note on the English page")
	}
}
