package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
)

func TestBodyRevIgnoresWhatIsNotAChange(t *testing.T) {
	a := BodyRev("# Title\n\nSome text.\n")
	if a != BodyRev("# Title  \r\n\r\nSome text.\r\n\r\n") {
		t.Error("a trailing space or a line ending changes the revision")
	}
	if a == BodyRev("# Title\n\nSome other text.\n") {
		t.Error("a change of words does not change the revision")
	}
	if len(a) != 8 {
		t.Errorf("%q", a)
	}
}

const english = "# Quick start\n\nSee [the page](the-page.md) and [the guide](../guides/share/household.md#set-it-up).\n\n## One\n\n![A picture](../images/a.png)\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"

func goodTranslation() Page {
	return Page{Path: "start/quickstart.md", Title: "Schnellstart", Tip: "In zehn Minuten zum ersten Foto.", SourceRev: BodyRev(english), Review: "needed",
		Body: "> [!NOTE]\n> Maschinell übersetzt.\n\n# Schnellstart\n\nSiehe [die Seite](the-page.md) und [die Anleitung](../../guides/share/household.md).\n\n## Eins\n\n![Ein Bild](../../images/a.png)\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"}
}

func englishPage() Page { return Page{Path: "start/quickstart.md", Body: english} }

func TestAGoodTranslationHasNoProblems(t *testing.T) {
	if bad := ValidateTranslation("de", goodTranslation(), englishPage()); len(bad) != 0 {
		t.Errorf("%v", bad)
	}
}

func TestATranslationThatDropsOrChangesWhatIsNotWordsIsRefused(t *testing.T) {
	cases := map[string]func(*Page){
		"a heading dropped":      func(p *Page) { p.Body = strings.Replace(p.Body, "## Eins\n\n", "", 1) },
		"a picture dropped":      func(p *Page) { p.Body = strings.Replace(p.Body, "![Ein Bild](../../images/a.png)\n\n", "", 1) },
		"a table dropped":        func(p *Page) { p.Body = p.Body[:strings.Index(p.Body, "| a |")] },
		"a link to another page": func(p *Page) { p.Body = strings.Replace(p.Body, "household.md", "circles.md", 1) },
		"a link dropped": func(p *Page) {
			p.Body = strings.Replace(p.Body, " und [die Anleitung](../../guides/share/household.md)", "", 1)
		},
		"no note while unchecked": func(p *Page) { p.Body = strings.Replace(p.Body, "> [!NOTE]", "> Hinweis", 1) },
		"feature set":             func(p *Page) { p.Feature = "quickstart" },
		"bad source_rev":          func(p *Page) { p.SourceRev = "abc" },
		"bad review":              func(p *Page) { p.Review = "maybe" },
		"no title":                func(p *Page) { p.Title = "" },
		"a tip too long":          func(p *Page) { p.Tip = strings.Repeat("a", 141) },
	}
	for name, mutate := range cases {
		p := goodTranslation()
		mutate(&p)
		if bad := ValidateTranslation("de", p, englishPage()); len(bad) == 0 {
			t.Errorf("%s: no problem reported", name)
		}
	}
}

func TestAnAnchorInAnotherPageIsNotKeptButOneInTheSamePageIs(t *testing.T) {
	en := ShapeOf("start/x.md", "", "[a](../guides/y.md#heading) [b](#own)")
	tr := ShapeOf("start/x.md", "de", "[a](../../guides/y.md) [b](#eigen)")
	if d := en.Differences(tr); len(d) != 0 {
		t.Errorf("%v", d)
	}
	if d := en.Differences(ShapeOf("start/x.md", "de", "[a](../../guides/z.md) [b](#eigen)")); len(d) == 0 {
		t.Error("a link to another page is accepted")
	}
}

func TestTheAppsOwnWordsMustBeUsed(t *testing.T) {
	tr := func(s string) (string, bool) {
		m := map[string]string{"Approve": "Genehmigen", "Settings": "Einstellungen"}
		v, ok := m[s]
		return v, ok
	}
	if got := MissingLabels("Press **Approve** in **Settings**, then **Something else**.", "Drücke **Genehmigen** in den **Einstellungen**.", tr); len(got) != 0 {
		t.Errorf("%v", got)
	}
	if got := MissingLabels("Press **Approve** in **Settings**.", "Drücke **Freigeben** in den **Einstellungen**.", tr); len(got) != 1 || !strings.Contains(got[0], "Genehmigen") {
		t.Errorf("%v", got)
	}
}

func TestAPageWithoutATranslationIsReadInEnglish(t *testing.T) {
	if Used("de", "guides/share/household.md") != "en" || Used("en", "start/quickstart.md") != "en" {
		t.Error("an untranslated page is not read in English")
	}
	if title, tip, ok := Meta("de", "household"); !ok || title == "" || tip == "" {
		t.Error("the title and the tip fall back to English")
	}
}

// For each language that has a translation: the translation uses the app's own words, and the checks the generator makes hold.
func TestTranslatedGuidesUseTheAppsOwnWords(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "docs", filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, lang := range Languages {
		l := i18n.Lang(lang)
		for _, p := range Translated(lang) {
			en, _ := Parse(p, []byte(read(p)))
			tr, _ := Parse(p, []byte(read(lang+"/"+p)))
			for _, m := range MissingLabels(en.Body, tr.Body, func(s string) (string, bool) { x := i18n.Text(l, s); return x, x != "" }) {
				t.Errorf("%s/%s: %s", lang, p, m)
			}
		}
	}
}
