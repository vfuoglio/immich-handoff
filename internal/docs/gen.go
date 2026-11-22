//go:build ignore

// Command gen reads the documentation in ../../docs and writes the bundle the app embeds (bundle/): the Markdown of every page for
// the app's users and administrators (not the contributors' pages, not the images), an index with each page's front matter, the
// tooltips that the "?" links show and a small search index. It stops with the list of problems when a page's front matter is wrong.
// It runs with go generate, so CI's "generated code is up to date" check also keeps the bundle in step with the pages.
package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/vfuoglio/immich-handoff/internal/docs"
	xdraw "golang.org/x/image/draw"
	"gopkg.in/yaml.v3"
)

type tipEntry struct {
	Title string            `json:"title"`
	Tip   string            `json:"tip"`
	Path  string            `json:"path"`
	Tips  map[string]string `json:"tips,omitempty"`
}

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
	translated := map[string][]docs.Page{} // by language: the pages of docs/<language>/, with the path inside it
	strs := map[string]docs.Strings{}
	gone := movedPages(root)
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "images/") || rel == "index.md" { // index.md is the site's landing page (Starlight front matter), not a page of Help
			return nil
		}
		if gone[rel] { // a one-line pointer to a page that moved (docs/redirects.txt)
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if lang, inner, ok := strings.Cut(rel, "/"); ok && docs.IsLanguage(lang) {
			page, err := docs.Parse(inner, src)
			if err != nil {
				return fmt.Errorf("%s: %w", lang, err)
			}
			translated[lang] = append(translated[lang], page)
			return nil
		}
		page, err := docs.Parse(rel, src)
		if err != nil {
			return err
		}
		pages = append(pages, page)
		return nil
	})
	for _, lang := range docs.Languages { // docs/<language>/strings.yml
		if b, e := os.ReadFile(filepath.Join(root, lang, "strings.yml")); e == nil {
			var m docs.Strings
			dec := yaml.NewDecoder(strings.NewReader(string(b)))
			dec.KnownFields(true)
			if e := dec.Decode(&m); e != nil {
				fmt.Fprintf(os.Stderr, "%s/strings.yml: %v\n", lang, e)
				os.Exit(1)
			}
			strs[lang] = m
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if bad := docs.Validate(pages); len(bad) > 0 {
		fmt.Fprintln(os.Stderr, "the documentation's front matter has problems:\n  "+strings.Join(bad, "\n  "))
		os.Exit(1)
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Path < pages[j].Path })
	byPath := map[string]docs.Page{}
	for _, p := range pages {
		byPath[p.Path] = p
	}
	if len(os.Args) > 2 && os.Args[1] == "-rev" { // the source_rev for a translation of an English page: go run gen.go -rev start/quickstart.md
		p, ok := byPath[os.Args[2]]
		if !ok {
			fmt.Fprintln(os.Stderr, "no such English page:", os.Args[2])
			os.Exit(1)
		}
		fmt.Println(docs.BodyRev(p.Body))
		return
	}
	if bad := checkTranslations(byPath, pages, translated, strs); len(bad) > 0 {
		fmt.Fprintln(os.Stderr, "the translations have problems:\n  "+strings.Join(bad, "\n  "))
		os.Exit(1)
	}
	if len(os.Args) > 1 && os.Args[1] == "-stale" { // a list for CI: a translation whose English page has changed since it was made
		for _, lang := range docs.Languages {
			for _, t := range translated[lang] {
				if en, ok := byPath[t.Path]; ok && docs.BodyRev(en.Body) != t.SourceRev {
					fmt.Printf("::warning::docs/%s/%s was translated from an English page that has changed since (source_rev %s, now %s)\n", lang, t.Path, t.SourceRev, docs.BodyRev(en.Body))
				}
			}
		}
		return
	}

	out := "bundle"
	if err := os.RemoveAll(filepath.Join(out, "en")); err != nil {
		panic(err)
	}
	if err := os.RemoveAll(filepath.Join(out, "img")); err != nil {
		panic(err)
	}
	var index []indexEntry
	pictures := map[string]bool{}
	tips := map[string]tipEntry{}
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
		for _, m := range picture.FindAllStringSubmatch(p.Body, -1) {
			pictures[m[1]] = true
		}
		index = append(index, indexEntry{p.Path, p.Title, p.Feature, p.Audience, p.Goal, p.Order, p.Tip})
		tips[p.Feature] = tipEntry{p.Title, p.Tip, p.Path, p.Tips}
	}
	var tour []docs.TourStop
	for _, p := range pages {
		tour = append(tour, p.Tour...)
	}
	write(filepath.Join(out, "tour.json"), tour)
	slim(root, filepath.Join(out, "img"), pictures)
	sort.SliceStable(index, func(i, j int) bool {
		a, b := index[i], index[j]
		if gi, gj := rank(a.Goal), rank(b.Goal); gi != gj {
			return gi < gj
		}
		return a.Order < b.Order
	})
	write(filepath.Join(out, "index.json"), index)
	write(filepath.Join(out, "tips.json"), tips)
	for _, lang := range docs.Languages {
		writeLanguage(out, lang, byPath, translated[lang], strs[lang])
	}
}

type pageNote struct {
	Review string `json:"review"`
	Stale  bool   `json:"stale,omitempty"`
}

// writeLanguage writes a language's translated pages and its meta.json: the titles and tips of every page, the pages' notes (how far
// each was reviewed, whether the English has changed since), the button tips and the tour stops that were translated.
func writeLanguage(out, lang string, english map[string]docs.Page, pages []docs.Page, strs docs.Strings) {
	dir := filepath.Join(out, lang)
	if err := os.RemoveAll(dir); err != nil {
		panic(err)
	}
	if len(pages) == 0 && len(strs) == 0 {
		return
	}
	meta := struct {
		Strings docs.Strings                 `json:"strings"`
		Pages   map[string]pageNote          `json:"pages"`
		Tips    map[string]map[string]string `json:"tips"`
		Tour    []docs.TourStop              `json:"tour"`
	}{Strings: strs, Pages: map[string]pageNote{}, Tips: map[string]map[string]string{}}
	for _, p := range pages {
		en := english[p.Path]
		if en.Audience != "user" && en.Audience != "admin" {
			continue
		}
		dest := filepath.Join(dir, filepath.FromSlash(p.Path))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			panic(err)
		}
		if err := os.WriteFile(dest, []byte(p.Body), 0o644); err != nil {
			panic(err)
		}
		meta.Pages[p.Path] = pageNote{p.Review, docs.BodyRev(en.Body) != p.SourceRev}
		if len(p.Tips) > 0 {
			meta.Tips[en.Feature] = p.Tips
		}
		meta.Tour = append(meta.Tour, p.Tour...)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}
	write(filepath.Join(dir, "meta.json"), meta)
}

// checkTranslations says what is wrong with the translated pages and strings of every language.
func checkTranslations(english map[string]docs.Page, pages []docs.Page, translated map[string][]docs.Page, strs map[string]docs.Strings) []string {
	var bad []string
	features := map[string]bool{}
	for _, p := range pages {
		if p.Audience == "user" || p.Audience == "admin" {
			features[p.Feature] = true
		}
	}
	for _, lang := range docs.Languages {
		for _, t := range translated[lang] {
			en, ok := english[t.Path]
			if !ok {
				bad = append(bad, fmt.Sprintf("%s/%s: there is no English page %s", lang, t.Path, t.Path))
				continue
			}
			bad = append(bad, docs.ValidateTranslation(lang, t, en)...)
			if s, ok := strs[lang][en.Feature]; ok && s.Title != t.Title {
				bad = append(bad, fmt.Sprintf("%s/%s: the title is %q but strings.yml says %q", lang, t.Path, t.Title, s.Title))
			}
		}
		for f, s := range strs[lang] {
			if !features[f] {
				bad = append(bad, fmt.Sprintf("%s/strings.yml: %q is not a feature", lang, f))
			}
			if s.Title == "" || s.Tip == "" || len([]rune(s.Tip)) > 140 || strings.Contains(s.Tip, "\n") {
				bad = append(bad, fmt.Sprintf("%s/strings.yml: %s needs a title and a tip of one line, 140 characters at most", lang, f))
			}
		}
		if len(strs[lang]) > 0 {
			for f := range features {
				if _, ok := strs[lang][f]; !ok {
					bad = append(bad, fmt.Sprintf("%s/strings.yml: no title and tip for %q", lang, f))
				}
			}
		}
	}
	sort.Strings(bad)
	return bad
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

var picture = regexp.MustCompile(`images/([A-Za-z0-9._-]+\.(?:png|jpg|jpeg|gif|svg))`)

// maxWidth is the widest a picture is kept: Help shows pictures in a column, and the originals (17 MB) stay with the site.
const maxWidth = 900

// slim writes a small copy of each picture the pages show: a JPEG at most maxWidth wide (a GIF becomes its first frame, the
// animation is on the site), and SVGs as they are. Pure Go, so the build needs no tools.
func slim(from, to string, names map[string]bool) {
	if err := os.MkdirAll(to, 0o755); err != nil {
		panic(err)
	}
	for n := range names {
		src := filepath.Join(from, "images", n)
		f, err := os.Open(src)
		if err != nil {
			fmt.Fprintln(os.Stderr, "a page shows a picture that does not exist:", n)
			os.Exit(1)
		}
		dest := filepath.Join(to, docs.ImageName(n))
		if strings.HasSuffix(n, ".svg") {
			b, _ := os.ReadFile(src)
			f.Close()
			os.WriteFile(dest, b, 0o644)
			continue
		}
		var img image.Image
		switch {
		case strings.HasSuffix(n, ".png"):
			img, err = png.Decode(f)
		case strings.HasSuffix(n, ".gif"):
			var g *gif.GIF
			if g, err = gif.DecodeAll(f); err == nil {
				img = g.Image[0]
			}
		default:
			img, err = jpeg.Decode(f)
		}
		f.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, n+":", err)
			os.Exit(1)
		}
		b := img.Bounds()
		if b.Dx() > maxWidth {
			h := b.Dy() * maxWidth / b.Dx()
			small := image.NewRGBA(image.Rect(0, 0, maxWidth, h))
			xdraw.CatmullRom.Scale(small, small.Bounds(), img, b, xdraw.Src, nil)
			img = small
		}
		flat := image.NewRGBA(img.Bounds()) // a JPEG has no transparency: flatten on white
		xdraw.Draw(flat, flat.Bounds(), image.White, image.Point{}, xdraw.Src)
		xdraw.Draw(flat, flat.Bounds(), img, img.Bounds().Min, xdraw.Over)
		o, err := os.Create(dest)
		if err != nil {
			panic(err)
		}
		if err := jpeg.Encode(o, flat, &jpeg.Options{Quality: 78}); err != nil {
			panic(err)
		}
		o.Close()
	}
}

// movedPages reads docs/redirects.txt: the old paths of pages that moved, each left as a one-line pointer for readers of old links.
func movedPages(root string) map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(filepath.Join(root, "redirects.txt"))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 && !strings.HasPrefix(line, "#") {
			out[f[0]] = true
		}
	}
	return out
}
