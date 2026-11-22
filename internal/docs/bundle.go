package docs

//go:generate go run gen.go

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

// bundle is what gen.go made from docs/: the Markdown of the pages for users and administrators, in English (bundle/en), and
// index.json, tips.json and search.json.
//
//go:embed bundle
var bundle embed.FS

// Entry is a page as the index lists it.
type Entry struct {
	Path     string `json:"path"`
	Title    string `json:"title"`
	Feature  string `json:"feature"`
	Audience string `json:"audience"`
	Goal     string `json:"goal"`
	Order    int    `json:"order"`
	Tip      string `json:"tip"`
}

// Index lists the pages in the order Help shows them: by goal, then by order.
func Index() ([]Entry, error) {
	var out []Entry
	b, err := bundle.ReadFile("bundle/index.json")
	if err != nil {
		return nil, err
	}
	return out, json.Unmarshal(b, &out)
}

type tipEntry struct {
	Title string            `json:"title"`
	Tip   string            `json:"tip"`
	Path  string            `json:"path"`
	Tips  map[string]string `json:"tips,omitempty"`
}

var tips = sync.OnceValue(func() map[string]tipEntry {
	var m map[string]tipEntry
	if b, err := bundle.ReadFile("bundle/tips.json"); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
})

// Tip is the title, one-line tip and page of a feature, for the "?" links and the tooltips. ok is false for a feature that
// has no page, so a template can never link to nothing.
func Tip(feature string) (title, tip, path string, ok bool) {
	t, ok := tips()[feature]
	return t.Title, t.Tip, t.Path, ok
}

// ButtonTip is the tooltip of one button or switch of a feature, by the key the page's front matter gives it.
func ButtonTip(feature, key string) (string, bool) {
	t, ok := tips()[feature].Tips[key]
	return t, ok
}

// Features lists the feature keys that have a page, sorted.
func Features() []string {
	var out []string
	for f := range tips() {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// FeatureOfPath is the feature of a page, by its path in the bundle ("guides/show/stories.md").
func FeatureOfPath(path string) (string, bool) {
	for f, t := range tips() {
		if t.Path == path {
			return f, true
		}
	}
	return "", false
}

// Source returns one page's Markdown in a language, falling back to English as the app's own texts do.
func Source(lang, path string) (string, error) {
	path = strings.TrimPrefix(path, "/")
	if strings.Contains(path, "..") {
		return "", fmt.Errorf("no such page")
	}
	for _, l := range []string{lang, "en"} {
		if b, err := fs.ReadFile(bundle, "bundle/"+l+"/"+path); err == nil {
			return string(b), nil
		}
	}
	return "", fmt.Errorf("no such page")
}

// Image returns a slimmed picture of the pages, by the name ImageName gives it, and its content type.
func Image(name string) ([]byte, string, bool) {
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return nil, "", false
	}
	b, err := fs.ReadFile(bundle, "bundle/img/"+name)
	if err != nil {
		return nil, "", false
	}
	if strings.HasSuffix(name, ".svg") {
		return b, "image/svg+xml", true
	}
	return b, "image/jpeg", true
}

// Paths lists every page in the bundle, sorted.
func Paths() []string {
	var out []string
	fs.WalkDir(bundle, "bundle/en", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, strings.TrimPrefix(p, "bundle/en/"))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// Tour returns the stops of the tour of a page ("user" or "admin"), in order.
func Tour(page string) []TourStop {
	var all []TourStop
	if b, err := bundle.ReadFile("bundle/tour.json"); err == nil {
		json.Unmarshal(b, &all)
	}
	var out []TourStop
	for _, s := range all {
		if s.Page == page {
			out = append(out, s)
		}
	}
	return out
}

type pageNote struct {
	Review string `json:"review"`
	Stale  bool   `json:"stale"`
}

type langMeta struct {
	Strings Strings                      `json:"strings"`
	Pages   map[string]pageNote          `json:"pages"`
	Tips    map[string]map[string]string `json:"tips"`
	Tour    []TourStop                   `json:"tour"`
}

var metas sync.Map // language -> langMeta

// meta is what the bundle holds for a language besides its pages; empty for English and for a language with nothing translated.
func meta(lang string) langMeta {
	if v, ok := metas.Load(lang); ok {
		return v.(langMeta)
	}
	var m langMeta
	if IsLanguage(lang) {
		if b, err := bundle.ReadFile("bundle/" + lang + "/meta.json"); err == nil {
			json.Unmarshal(b, &m)
		}
	}
	metas.Store(lang, m)
	return m
}

// Meta is the title and the tip of a feature's page in a language, falling back to English.
func Meta(lang, feature string) (title, tip string, ok bool) {
	if s, found := meta(lang).Strings[feature]; found {
		return s.Title, s.Tip, true
	}
	title, tip, _, ok = Tip(feature)
	return
}

// ButtonTipFor is a button's tip in a language, when the guide has been translated that far (false: use the English one).
func ButtonTipFor(lang, feature, key string) (string, bool) {
	s, ok := meta(lang).Tips[feature][key]
	return s, ok
}

// TourFor is the stops of a page's tour in a language: the English stops, with the title and the text of each that was translated.
func TourFor(lang, page string) []TourStop {
	stops := Tour(page)
	for i, s := range stops {
		for _, t := range meta(lang).Tour {
			if t.Target == s.Target && t.Page == s.Page {
				stops[i].Title, stops[i].Text = t.Title, t.Text
			}
		}
	}
	return stops
}

// Used says which language a page is read in: the language asked for if the page has been translated, otherwise English.
func Used(lang, path string) string {
	if _, ok := meta(lang).Pages[path]; ok {
		return lang
	}
	return "en"
}

// Stale says whether the page's translation was made from an English page that has changed since.
func Stale(lang, path string) bool { return meta(lang).Pages[path].Stale }

// Review says how far a translated page has been checked ("needed" or "done"), or "" when the page is not translated.
func Review(lang, path string) string { return meta(lang).Pages[path].Review }

// Translated lists the pages translated into a language, sorted.
func Translated(lang string) []string {
	var out []string
	for p := range meta(lang).Pages {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
