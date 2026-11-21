package docs

//go:generate go run gen.go

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
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

// Tip is the title, one-line tip and page of a feature, for the "?" links and the tooltips. ok is false for a feature that
// has no page, so a template can never link to nothing.
func Tip(feature string) (title, tip, path string, ok bool) {
	b, err := bundle.ReadFile("bundle/tips.json")
	if err != nil {
		return
	}
	var m map[string]map[string]string
	if json.Unmarshal(b, &m) != nil {
		return
	}
	t, ok := m[feature]
	return t["title"], t["tip"], t["path"], ok
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
