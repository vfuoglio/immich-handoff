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
