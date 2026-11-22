package docs

import (
	"crypto/sha256"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Translations. A page that has been translated lives at docs/<language>/<same path>, with only what is translated in its front matter
// (title, tip, tips, tour, source_rev, review); everything else (feature, audience, goal, order) comes from the English page, so a
// translation cannot drift from it. docs/<language>/strings.yml holds the title and the tip of every page, so the lists of Help and the
// "?" tooltips are in the person's language even where the text of a page is still English. A page with no translation shows in English.

// Languages are the languages the app's own interface is translated into, and so the ones the documentation can be translated into.
var Languages = []string{"de", "fr", "es", "it", "pt", "ru"}

// IsLanguage says whether a folder name under docs/ is a language.
func IsLanguage(name string) bool { return contains(Languages, name) }

// Strings is a language's docs/<language>/strings.yml: the title and the tip of each feature.
type Strings map[string]struct {
	Title string `yaml:"title" json:"title"`
	Tip   string `yaml:"tip" json:"tip"`
}

// Reviews says how far a translation has been checked: "needed" until a native speaker has read it, then "done".
var Reviews = []string{"needed", "done"}

// BodyRev is the short hash of an English page's text that a translation records as source_rev, so that a change of the English can be
// told from a change of nothing (a space at the end of a line, or a different line ending, is not a change).
func BodyRev(body string) string {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		lines = append(lines, strings.TrimRight(l, " \t"))
	}
	text := strings.TrimSpace(strings.Join(lines, "\n"))
	return fmt.Sprintf("%x", sha256.Sum256([]byte(text)))[:8]
}

var revRe = regexp.MustCompile(`^[0-9a-f]{8}$`)

// ValidateTranslation says what is wrong with a translated page, one problem per line, given the English page it translates.
func ValidateTranslation(lang string, tr, en Page) []string {
	var bad []string
	say := func(f string, a ...any) {
		bad = append(bad, fmt.Sprintf("%s/%s: "+f, append([]any{lang, tr.Path}, a...)...))
	}
	if tr.Feature != "" || tr.Audience != "" || tr.Goal != "" || tr.Order != 0 {
		say("feature, audience, goal and order belong to the English page")
	}
	if tr.Title == "" {
		say("no title")
	}
	if tr.Tip == "" || len([]rune(tr.Tip)) > 140 || strings.Contains(tr.Tip, "\n") {
		say("the tip is missing, longer than 140 characters or on more than one line")
	}
	if !revRe.MatchString(tr.SourceRev) {
		say("source_rev is not the short hash of the English page (use docs.BodyRev: 8 hex digits)")
	}
	if !contains(Reviews, tr.Review) {
		say("review is %q, not one of %s", tr.Review, strings.Join(Reviews, ", "))
	}
	for k, v := range tr.Tips {
		if _, ok := en.Tips[k]; !ok {
			say("the tip %q has no English tip", k)
		}
		if v == "" || len([]rune(v)) > 140 || strings.Contains(v, "\n") {
			say("the tip %q is empty, longer than 140 characters or on more than one line", k)
		}
	}
	for i, st := range tr.Tour {
		found := false
		for _, e := range en.Tour {
			found = found || (e.Target == st.Target && e.Page == st.Page)
		}
		if !found {
			say("tour stop %d (%s) has no English stop", i+1, st.Target)
		}
		if st.Title == "" || st.Text == "" {
			say("tour stop %d needs a title and a text", i+1)
		}
	}
	a, b := ShapeOf(en.Path, "", en.Body), ShapeOf(tr.Path, lang, tr.Body)
	for _, d := range a.Differences(b) {
		say("%s", d)
	}
	if tr.Review == "needed" && !strings.Contains(tr.Body, "[!NOTE]") {
		say("a page not yet reviewed starts with a note that says it is machine-translated (> [!NOTE])")
	}
	return bad
}

// Shape is what a page is made of, apart from its words: the translation must have the same.
type Shape struct {
	Headings, Tables, Images int
	Links                    []string // where each link goes, as a path in docs/ without the language folder; anchors only for the page itself
}

var (
	headingLine = regexp.MustCompile(`(?m)^#{1,6} `)
	tableRule   = regexp.MustCompile(`(?m)^\|[ :|-]*-[ :|-]*\|\s*$`)
	imageRef    = regexp.MustCompile(`!\[[^\]]*\]\(|<img `)
	linkRef     = regexp.MustCompile(`(!?)\[[^\]]*\]\(([^)\s]+)\)`)
	fenced      = regexp.MustCompile("(?s)```.*?```")
)

// ShapeOf reads a page's shape. lang is "" for the English page; pagePath is the page's path in docs/ without the language folder.
func ShapeOf(pagePath, lang, body string) Shape {
	body = fenced.ReplaceAllString(body, "")
	s := Shape{Headings: len(headingLine.FindAllString(body, -1)), Tables: len(tableRule.FindAllString(body, -1)), Images: len(imageRef.FindAllString(body, -1))}
	here := pagePath
	if lang != "" {
		here = path.Join(lang, pagePath)
	}
	for _, m := range linkRef.FindAllStringSubmatch(body, -1) {
		if m[1] == "!" {
			continue
		}
		url := m[2]
		if external.MatchString(url) {
			s.Links = append(s.Links, url)
			continue
		}
		p, frag, _ := strings.Cut(url, "#")
		if p == "" {
			s.Links = append(s.Links, "#"+frag) // an anchor in the page itself
			continue
		}
		target := path.Join(path.Dir(here), p)
		if lang != "" {
			target = strings.TrimPrefix(target, lang+"/")
		}
		if frag != "" && lang == "" {
			frag = "#" + frag
		} else if frag != "" {
			frag = "#" // headings are translated, so their ids differ: a translation links to a page, not to a heading of another page
		}
		s.Links = append(s.Links, target+strings.TrimSuffix(frag, "#"))
	}
	return s
}

// Differences says how another shape differs from this one (this is the English, the other the translation).
func (s Shape) Differences(o Shape) []string {
	var out []string
	if s.Headings != o.Headings {
		out = append(out, fmt.Sprintf("it has %d headings, the English page has %d", o.Headings, s.Headings))
	}
	if s.Tables != o.Tables {
		out = append(out, fmt.Sprintf("it has %d tables, the English page has %d", o.Tables, s.Tables))
	}
	if s.Images != o.Images {
		out = append(out, fmt.Sprintf("it has %d pictures, the English page has %d", o.Images, s.Images))
	}
	if len(s.Links) != len(o.Links) {
		out = append(out, fmt.Sprintf("it has %d links, the English page has %d", len(o.Links), len(s.Links)))
		return out
	}
	for i := range s.Links {
		a, b := s.Links[i], o.Links[i]
		if i := strings.Index(a, "#"); i > 0 && !strings.HasPrefix(a, "#") {
			a = a[:i] // the anchor of another page is not kept in a translation
		}
		if a != b && !(strings.HasPrefix(a, "#") && strings.HasPrefix(b, "#")) {
			out = append(out, fmt.Sprintf("link %d goes to %q, the English page's goes to %q", i+1, b, s.Links[i]))
		}
	}
	return out
}

var boldLabel = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)

// MissingLabels says which of the app's own words a translated page does not use. For every **bold phrase** of the English page that is
// a message of the app (a button, a heading, a setting), the translation must contain the app's translation of it into that language,
// so a guide never names a button the app does not have. translate returns the translation of an English message, or false when the app
// has no such message.
func MissingLabels(english, translated string, translate func(string) (string, bool)) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range boldLabel.FindAllStringSubmatch(fenced.ReplaceAllString(english, ""), -1) {
		label := strings.TrimSpace(m[1])
		if seen[label] {
			continue
		}
		seen[label] = true
		if t, ok := translate(label); ok && t != label && !strings.Contains(translated, t) {
			out = append(out, fmt.Sprintf("%q should appear as %q", label, t))
		}
	}
	return out
}
