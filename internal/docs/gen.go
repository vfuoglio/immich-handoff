//go:build ignore

// Command gen reads the documentation in ../../docs and writes the bundle the app embeds (bundle/): the Markdown of every page for
// the app's users and administrators (not the contributors' pages, not the images), an index with each page's front matter, the
// tooltips that the "?" links show and a small search index. It stops with the list of problems when a page's front matter is wrong.
// It runs with go generate, so CI's "generated code is up to date" check also keeps the bundle in step with the pages.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vfuoglio/immich-handoff/internal/docs"
)

type indexEntry struct {
	Path     string `json:"path"`
	Title    string `json:"title"`
	Feature  string `json:"feature"`
	Audience string `json:"audience"`
	Goal     string `json:"goal"`
	Order    int    `json:"order"`
	Tip      string `json:"tip"`
}

func main() {
	root := "../../docs"
	var pages []docs.Page
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "images/") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		page, err := docs.Parse(rel, src)
		if err != nil {
			return err
		}
		pages = append(pages, page)
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if bad := docs.Validate(pages); len(bad) > 0 {
		fmt.Fprintln(os.Stderr, "the documentation's front matter has problems:\n  "+strings.Join(bad, "\n  "))
		os.Exit(1)
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Path < pages[j].Path })

	out := "bundle"
	if err := os.RemoveAll(filepath.Join(out, "en")); err != nil {
		panic(err)
	}
	var index []indexEntry
	tips := map[string]map[string]string{}
	for _, p := range pages {
		if p.Audience != "user" && p.Audience != "admin" {
			continue
		}
		dest := filepath.Join(out, "en", filepath.FromSlash(p.Path))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			panic(err)
		}
		if err := os.WriteFile(dest, []byte(p.Body), 0o644); err != nil {
			panic(err)
		}
		index = append(index, indexEntry{p.Path, p.Title, p.Feature, p.Audience, p.Goal, p.Order, p.Tip})
		tips[p.Feature] = map[string]string{"title": p.Title, "tip": p.Tip, "path": p.Path}
	}
	sort.SliceStable(index, func(i, j int) bool {
		a, b := index[i], index[j]
		if gi, gj := rank(a.Goal), rank(b.Goal); gi != gj {
			return gi < gj
		}
		return a.Order < b.Order
	})
	write(filepath.Join(out, "index.json"), index)
	write(filepath.Join(out, "tips.json"), tips)
}

func rank(goal string) int {
	for i, g := range docs.Goals {
		if g == goal {
			return i
		}
	}
	return len(docs.Goals)
}

func write(path string, v any) {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		panic(err)
	}
}
