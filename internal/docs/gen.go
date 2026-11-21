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
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "images/") || rel == "index.md" { // index.md is the site's landing page (Starlight front matter), not a page of Help
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
