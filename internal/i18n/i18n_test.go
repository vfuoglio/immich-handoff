package i18n_test

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
)

func TestEnglishIsTheSourceAndAMissingTranslationFallsBackToIt(t *testing.T) {
	en := i18n.For(i18n.English)
	if got := en.T("Sign in to {where} first", "where", "Immich"); got != "Sign in to Immich first" {
		t.Errorf("placeholders: %q", got)
	}
	// a language with no entry shows the English, never nothing
	de := i18n.For(i18n.German)
	if got := de.T("This message has no translation {x}", "x", "yet"); got != "This message has no translation yet" {
		t.Errorf("fallback: %q", got)
	}
	if got := i18n.For("xx").Lang; got != i18n.English {
		t.Errorf("an unknown language is English: %q", got)
	}
}

func TestPluralCategoriesFollowEachLanguagesRules(t *testing.T) {
	cases := []struct {
		lang i18n.Lang
		n    int
		want string
	}{
		{i18n.English, 1, "one"}, {i18n.English, 0, "other"}, {i18n.English, 2, "other"},
		{i18n.German, 1, "one"}, {i18n.German, 5, "other"},
		{i18n.French, 0, "one"}, {i18n.French, 1, "one"}, {i18n.French, 2, "other"},
		{i18n.Portuguese, 0, "one"}, {i18n.Portuguese, 1, "one"}, {i18n.Portuguese, 3, "other"},
		{i18n.Spanish, 1, "one"}, {i18n.Spanish, 0, "other"},
		{i18n.Italian, 1, "one"}, {i18n.Italian, 2, "other"},
		{i18n.Russian, 1, "one"}, {i18n.Russian, 21, "one"}, {i18n.Russian, 101, "one"}, {i18n.Russian, 11, "many"},
		{i18n.Russian, 2, "few"}, {i18n.Russian, 4, "few"}, {i18n.Russian, 22, "few"}, {i18n.Russian, 12, "many"}, {i18n.Russian, 14, "many"},
		{i18n.Russian, 5, "many"}, {i18n.Russian, 0, "many"}, {i18n.Russian, 100, "many"}, {i18n.Russian, 111, "many"}, {i18n.Russian, 112, "many"},
	}
	for _, c := range cases {
		if got := i18n.Plural(c.lang, c.n); got != c.want {
			t.Errorf("%s %d: %s, want %s", c.lang, c.n, got, c.want)
		}
	}
}

func TestACountedMessageUsesTheEnglishFormsWhenThereIsNoTranslation(t *testing.T) {
	en := i18n.For(i18n.English)
	if got := en.N("{n} photo", "{n} photos", 1); got != "1 photo" {
		t.Errorf("one: %q", got)
	}
	if got := en.N("{n} photo", "{n} photos", 3); got != "3 photos" {
		t.Errorf("other: %q", got)
	}
	if got := en.N("{n} photo from {who}", "{n} photos from {who}", 0, "who", "Bob"); got != "0 photos from Bob" {
		t.Errorf("zero: %q", got)
	}
}

func TestHTMLMessagesEscapeTheirValuesButKeepTheirOwnMarkup(t *testing.T) {
	got := i18n.For(i18n.English).H(`Add the tag <code class="tag">{tag}</code> to a photo`, "tag", `<script>x</script>`)
	want := `Add the tag <code class="tag">&lt;script&gt;x&lt;/script&gt;</code> to a photo`
	if got != want {
		t.Errorf("got %q", got)
	}
}

func TestTheLanguageIsChosenFromTheCookieThenTheBrowserThenEnglish(t *testing.T) {
	for _, c := range []struct {
		cookie, accept string
		want           i18n.Lang
	}{
		{"", "", i18n.English},
		{"de", "fr", i18n.German},                      // the person's own choice wins
		{"klingon", "fr", i18n.French},                 // an unknown choice is ignored
		{"", "fr-CH, fr;q=0.9, en;q=0.8", i18n.French}, // a regional form is its language
		{"", "sv, de;q=0.7, en;q=0.3", i18n.German},    // the first language we have, by preference
		{"", "en;q=0.3, es;q=0.9", i18n.Spanish},       // by weight, not by order
		{"", "ja, zh;q=0.9", i18n.English},             // none we have
		{"", "de;q=0", i18n.English},                   // refused
		{"", "*", i18n.English},
		{"", "garbage;;;q=x,,", i18n.English},
		{" es ", "", i18n.Spanish},
		{"", "it, ru;q=0.9", i18n.Italian},
		{"", "ru, pt;q=0.9", i18n.Russian},
		{"", "pt-BR, en;q=0.5", i18n.Portuguese},
	} {
		if got := i18n.Negotiate(c.cookie, c.accept); got != c.want {
			t.Errorf("cookie %q accept %q: %s, want %s", c.cookie, c.accept, got, c.want)
		}
	}
}

func TestTheLocaleTravelsInTheContext(t *testing.T) {
	if i18n.From(context.Background()).Lang != i18n.English {
		t.Error("no locale in the context means English")
	}
	ctx := i18n.With(context.Background(), i18n.For(i18n.French))
	if i18n.From(ctx).Lang != i18n.French {
		t.Error("the locale put in the context comes back")
	}
}

func TestMomentsAndWeekdaysAreInTheLanguage(t *testing.T) {
	at := time.Date(2026, 11, 2, 15, 4, 0, 0, time.UTC) // a Monday
	for lang, want := range map[i18n.Lang][2]string{
		i18n.English: {"2 Nov 15:04", "Mon"}, i18n.German: {"2. Nov. 15:04", "Mo"}, i18n.French: {"2 nov. 15:04", "lun."},
		i18n.Spanish: {"2 nov 15:04", "lun"},
	} {
		loc := i18n.For(lang)
		if got := loc.Stamp(at); got != want[0] {
			t.Errorf("%s stamp: %q, want %q", lang, got, want[0])
		}
		if got := loc.Weekday(at); got != want[1] {
			t.Errorf("%s weekday: %q, want %q", lang, got, want[1])
		}
	}
}

func TestEveryLanguageHasAName(t *testing.T) {
	for _, l := range i18n.All {
		if i18n.Names[l] == "" {
			t.Errorf("%s has no name for the menu", l)
		}
	}
	if len(i18n.All) != 7 {
		t.Errorf("English and six others: %d", len(i18n.All))
	}
}

// Every language must have exactly the same messages as German, with the same placeholders and markup, and the plural
// forms its language needs.
func TestEveryLanguageHasTheSameMessagesWithTheSamePlaceholdersAndMarkup(t *testing.T) {
	ph := regexp.MustCompile(`\{\w+\}`)
	tag := regexp.MustCompile(`<[^>]+>`)
	same := func(a, b []string) bool { return strings.Join(a, "|") == strings.Join(b, "|") }
	ids := i18n.Messages(i18n.German)
	if len(ids) < 300 {
		t.Fatalf("only %d messages in the German catalog", len(ids))
	}
	for _, l := range i18n.All[1:] {
		if got := i18n.Messages(l); len(got) != len(ids) {
			t.Errorf("%s has %d messages, German %d", l, len(got), len(ids))
		}
		for _, id := range ids {
			if f := i18n.Forms(l, id); f != nil {
				for _, want := range []string{"one", "other"} {
					if f[want] == "" {
						t.Errorf("%s: %q lacks the %s form", l, id, want)
					}
				}
				continue
			}
			text := i18n.Text(l, id)
			if text == "" {
				t.Errorf("%s lacks %q", l, id)
				continue
			}
			if !same(ph.FindAllString(text, -1), ph.FindAllString(id, -1)) {
				t.Errorf("%s: placeholders differ in %q: %q", l, id, text)
			}
			if !same(tag.FindAllString(text, -1), tag.FindAllString(id, -1)) {
				t.Errorf("%s: markup differs in %q: %q", l, id, text)
			}
		}
	}
}

func TestStoredTextIsShownInTheReadersLanguage(t *testing.T) {
	de, en := i18n.For(i18n.German), i18n.For(i18n.English)
	at := time.Date(2026, 11, 2, 15, 4, 0, 0, time.UTC)
	cases := []struct {
		name, stored, wantEN, wantDE string
	}{
		{"plain", i18n.Pack("Hello, {name}.", "name", "Ana"), "Hello, Ana.", "Hallo, Ana."},
		{"one", i18n.PackN("{n} photo", "{n} photos", 1), "1 photo", "1 Foto"},
		{"many", i18n.PackN("{n} photo", "{n} photos", 3), "3 photos", "3 Fotos"},
		{"joined", i18n.PackJoin(", ", i18n.Pack("paused"), i18n.Pack("no limit")), "paused, no limit", "pausiert, kein Limit"},
		{"date", i18n.Pack("An administrator ({admin}) removed the service {service} on {date}. Nothing is handed over any more and your photos were not touched.", "admin", "Bo", "service", "S", "date", i18n.Day(at)),
			"An administrator (Bo) removed the service S on 2 Nov 2026. Nothing is handed over any more and your photos were not touched.",
			"Ein Administrator (Bo) hat den Dienst S am 2. Nov. 2026 entfernt. Es wird nichts mehr übergeben, und deine Fotos wurden nicht angetastet."},
		{"old text", "Alice handed over 2 photos", "Alice handed over 2 photos", "Alice handed over 2 photos"}, // stored before translations existed
		{"braces that are not ours", "{not json", "{not json", "{not json"},
	}
	for _, c := range cases {
		if got := en.Unpack(c.stored); got != c.wantEN {
			t.Errorf("%s in English: %q, want %q", c.name, got, c.wantEN)
		}
		if got := de.Unpack(c.stored); got != c.wantDE {
			t.Errorf("%s in German: %q, want %q", c.name, got, c.wantDE)
		}
	}
}
