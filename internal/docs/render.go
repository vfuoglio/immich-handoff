package docs

import (
	"html"
	"path"
	"regexp"
	"strings"
)

// Links says where the rewritten links of a page in Help point.
type Links struct {
	Base string // what the page's own links start with: "" for a page at the top, "../" one level down
	Site string // the public documentation site, for pages that are not in the app (contributors' pages)
	Repo string // the repository on GitHub, for files that are not documentation (the changelog, the demo)
}

var (
	firstH1  = regexp.MustCompile(`^\s*<h1(?: id="([^"]*)")?[^>]*>.*?</h1>\s*`)
	hrefAttr = regexp.MustCompile(`<a href="([^"]*)"`)
	srcAttr  = regexp.MustCompile(`<img src="([^"]*)"`)
	external = regexp.MustCompile(`^[a-z][a-z0-9+.-]*:`)
)

// RenderHelp renders a page of the bundle for Help. A link to another page of the bundle becomes a link to that page in Help
// (/help/<feature>, the anchor kept); a link to a page that is not in the app goes to the public site or the repository, opens
// in a new tab and carries the class "leaves"; a picture becomes the slimmed copy served at /help/img. pagePath is the page's
// path in the bundle and decides what a relative link means.
func RenderHelp(pagePath, body string, l Links) (string, error) {
	out, err := Render(body)
	if err != nil {
		return "", err
	}
	// Help shows the title itself; a link to the page's own heading (#the-title) still has somewhere to go
	out = firstH1.ReplaceAllStringFunc(out, func(m string) string {
		if id := firstH1.FindStringSubmatch(m)[1]; id != "" {
			return `<span id="` + id + `"></span>`
		}
		return ""
	})
	dir := path.Dir(pagePath)
	out = hrefAttr.ReplaceAllStringFunc(out, func(m string) string {
		url := html.UnescapeString(hrefAttr.FindStringSubmatch(m)[1])
		if strings.HasPrefix(url, "#") {
			return m
		}
		if external.MatchString(url) {
			return `<a class="leaves" target="_blank" rel="noopener noreferrer" href="` + html.EscapeString(url) + `"`
		}
		p, frag, _ := strings.Cut(url, "#")
		if frag != "" {
			frag = "#" + frag
		}
		target := path.Join(dir, p)
		if f, ok := FeatureOfPath(target); ok {
			return `<a href="` + l.Base + `help/` + f + html.EscapeString(frag) + `"`
		}
		var dest string
		if strings.HasPrefix(target, "../") || target == ".." { // out of docs/: a file of the repository
			dest = l.Repo + "/blob/main/" + strings.TrimPrefix(path.Clean("docs/"+target), "/")
		} else if strings.HasSuffix(target, ".md") {
			slug := strings.TrimSuffix(target, ".md")
			if slug == "README" || slug == "index" {
				slug = ""
			} else {
				slug += "/"
			}
			dest = l.Site + "/" + slug
		} else {
			dest = l.Site + "/" + target
		}
		return `<a class="leaves" target="_blank" rel="noopener noreferrer" href="` + html.EscapeString(dest+frag) + `"`
	})
	out = srcAttr.ReplaceAllStringFunc(out, func(m string) string {
		src := html.UnescapeString(srcAttr.FindStringSubmatch(m)[1])
		if external.MatchString(src) {
			return m
		}
		return `<img loading="lazy" src="` + l.Base + `help/img/` + html.EscapeString(ImageName(path.Join(dir, src))) + `"`
	})
	return out, nil
}

// ImageName is the name a picture of docs/images has in the bundle: its base name, as a JPEG unless it is an SVG.
func ImageName(p string) string {
	base := path.Base(p)
	ext := path.Ext(base)
	if ext == ".svg" {
		return base
	}
	return strings.TrimSuffix(base, ext) + ".jpg"
}
