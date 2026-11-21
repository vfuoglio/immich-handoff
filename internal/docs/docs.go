// Package docs is the one source of the documentation: the Markdown files in docs/, read the same way by the build that makes the
// bundle embedded in the app (the Help pages, the "?" links and the tooltips) and by the public site (site/). A page is plain
// Markdown, readable on GitHub, with a small front matter; GitHub alerts (> [!NOTE]) and one fence for tabs are the only
// additions, so no page needs MDX. See docs/contributing/docs.md.
package docs

import (
	"bytes"
	"fmt"
	"html/template"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"gopkg.in/yaml.v3"
)

// Goals are the sections of the Using it chooser, in the order the site shows them. "start" holds the first steps and the
// answers to the first questions, "run" holds installing and looking after the app.
var Goals = []string{"start", "share", "collect", "automate", "show", "run"}

// Audiences say who a page is for. Contributor pages are on the site but never in the app.
var Audiences = []string{"user", "admin", "contributor"}

// Page is one document: its front matter and its Markdown body.
type Page struct {
	Path      string `yaml:"-"`     // the file, relative to docs/ and with forward slashes ("guides/show/stories.md")
	Title     string `yaml:"title"` // shown in the site's sidebar and in Help
	Feature   string `yaml:"feature"`
	Audience  string `yaml:"audience"`
	Goal      string `yaml:"goal"`
	Order     int    `yaml:"order"`
	Tip       string `yaml:"tip"`        // one line, shown as the tooltip of the "?" that opens this page
	SourceRev string `yaml:"source_rev"` // translations only: the revision of the English page it was translated from
	// Tips are the tooltips of the page's buttons and switches by key ("send-by-itself": "Sends without asking, ..."), asked for by the
	// templates with the feature and the key, so the words live with the guide that explains the feature.
	Tips map[string]string `yaml:"tips"`
	// Tour is the stops of the first-run tour (only docs/start/the-page.md has one): where it points, what it says, and the guide it can open.
	Tour []TourStop `yaml:"tour"`
	Body string     `yaml:"-"`
}

// TourStop is one stop of a tour: a coach-mark pointing at one thing on the page.
type TourStop struct {
	Target  string `yaml:"target" json:"target"` // a key of TourTargets: what it points at
	Page    string `yaml:"page" json:"page"`     // "user" (the Overview, Services, Settings) or "admin" (the administrator's page)
	Title   string `yaml:"title" json:"title"`
	Text    string `yaml:"text" json:"text"`
	Feature string `yaml:"feature" json:"feature,omitempty"` // a guide that explains more, if there is one
}

// TourTargets are the things a tour (and a step of the Get started card) can point at. The page decides where each one is; the
// documentation only names it, so that a stop can never point at something that has no name.
var TourTargets = []string{
	"needs", "scene", "services-tab", "offer", "invite", "send", "settings-tab", "notifications", "palette", "help", "tour",
	"admin-share", "admin-connections", "admin-services", "admin-backups", "admin-pet",
}

// Parse reads a page: an optional "---" front matter, then Markdown.
func Parse(path string, src []byte) (Page, error) {
	p := Page{Path: path}
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	if bytes.HasPrefix(src, []byte("---\n")) {
		end := bytes.Index(src[4:], []byte("\n---\n"))
		if end < 0 {
			return p, fmt.Errorf("%s: the front matter is never closed", path)
		}
		dec := yaml.NewDecoder(bytes.NewReader(src[4 : 4+end]))
		dec.KnownFields(true)
		if err := dec.Decode(&p); err != nil {
			return p, fmt.Errorf("%s: front matter: %w", path, err)
		}
		src = src[4+end+5:]
	}
	p.Body = strings.TrimLeft(string(src), "\n")
	return p, nil
}

// Exempt says whether a page may go without front matter: the contributors' pages, the index of the documentation, the folder of
// images and the translations' own index.
func Exempt(path string) bool {
	return strings.HasPrefix(path, "contributing/") || path == "README.md" || strings.HasPrefix(path, "images/")
}

var kebab = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// Validate says what is wrong with a set of pages, one problem per line, or nothing.
func Validate(pages []Page) []string {
	var bad []string
	seen := map[string]string{}
	for _, p := range pages {
		if p.Title == "" && !Exempt(p.Path) {
			bad = append(bad, p.Path+": no title")
		}
		if Exempt(p.Path) && p.Feature == "" {
			continue
		}
		if !kebab.MatchString(p.Feature) {
			bad = append(bad, fmt.Sprintf("%s: feature %q is not a lower-case key with hyphens", p.Path, p.Feature))
		} else if other, dup := seen[p.Feature]; dup {
			bad = append(bad, fmt.Sprintf("%s: feature %q is also the feature of %s", p.Path, p.Feature, other))
		} else {
			seen[p.Feature] = p.Path
		}
		if !contains(Audiences, p.Audience) {
			bad = append(bad, fmt.Sprintf("%s: audience %q is not one of %s", p.Path, p.Audience, strings.Join(Audiences, ", ")))
		}
		if !contains(Goals, p.Goal) {
			bad = append(bad, fmt.Sprintf("%s: goal %q is not one of %s", p.Path, p.Goal, strings.Join(Goals, ", ")))
		}
		if p.Order <= 0 {
			bad = append(bad, p.Path+": no order (a number above 0)")
		}
		if p.Tip == "" || len([]rune(p.Tip)) > 140 || strings.Contains(p.Tip, "\n") {
			bad = append(bad, p.Path+": the tip is missing, longer than 140 characters or on more than one line")
		}
		for i, st := range p.Tour {
			if !contains(TourTargets, st.Target) {
				bad = append(bad, fmt.Sprintf("%s: tour stop %d points at %q, which is not a target", p.Path, i+1, st.Target))
			}
			if st.Page != "user" && st.Page != "admin" {
				bad = append(bad, fmt.Sprintf("%s: tour stop %d is for the page %q, which is not user or admin", p.Path, i+1, st.Page))
			}
			if st.Title == "" || st.Text == "" || len([]rune(st.Text)) > 240 || strings.Contains(st.Text, "\n") {
				bad = append(bad, fmt.Sprintf("%s: tour stop %d needs a title and a text of one line, 240 characters at most", p.Path, i+1))
			}
		}
		for k, v := range p.Tips {
			if !kebab.MatchString(k) {
				bad = append(bad, fmt.Sprintf("%s: tip key %q is not a lower-case key with hyphens", p.Path, k))
			}
			if v == "" || len([]rune(v)) > 140 || strings.Contains(v, "\n") {
				bad = append(bad, fmt.Sprintf("%s: the tip %q is empty, longer than 140 characters or on more than one line", p.Path, k))
			}
		}
	}
	for _, p := range pages {
		for i, st := range p.Tour {
			if st.Feature != "" {
				if _, ok := seen[st.Feature]; !ok {
					bad = append(bad, fmt.Sprintf("%s: tour stop %d names the guide %q, which does not exist", p.Path, i+1, st.Feature))
				}
			}
		}
	}
	sort.Strings(bad)
	return bad
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

var (
	tabsBlock = regexp.MustCompile(`(?ms)^:::tabs\n(.*?)^:::[ \t]*$`)
	tabHead   = regexp.MustCompile(`(?m)^### (.+)$`)
	alert     = regexp.MustCompile(`<blockquote>\s*<p>\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]\s*`)
	md        = goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(html.WithUnsafe()), // the pages are our own, embedded at build time
	)
)

// ExpandTabs turns the tabs fence into the HTML that Help styles and the site's plugin turns into components:
//
//	:::tabs
//	### Docker Compose
//	...Markdown...
//	### docker run
//	...Markdown...
//	:::
//
// On GitHub the fence shows as plain text with two headings, which still reads well.
func ExpandTabs(body string) string {
	return tabsBlock.ReplaceAllStringFunc(body, func(block string) string {
		inner := tabsBlock.FindStringSubmatch(block)[1]
		heads := tabHead.FindAllStringSubmatchIndex(inner, -1)
		if len(heads) == 0 {
			return block
		}
		var b strings.Builder
		b.WriteString("<div class=\"doc-tabs\">\n\n")
		for i, h := range heads {
			end := len(inner)
			if i+1 < len(heads) {
				end = heads[i+1][0]
			}
			label := inner[h[2]:h[3]]
			fmt.Fprintf(&b, "<div class=\"doc-tab\" data-label=\"%s\">\n\n%s\n\n</div>\n\n", template.HTMLEscapeString(label), strings.TrimSpace(inner[h[1]:end]))
		}
		b.WriteString("</div>")
		return b.String()
	})
}

// Render turns a page's Markdown into HTML: tables and task lists as on GitHub, headings with ids, alerts as
// <blockquote class="alert note"> and the tabs fence as .doc-tabs.
func Render(body string) (string, error) {
	var out bytes.Buffer
	if err := md.Convert([]byte(ExpandTabs(body)), &out); err != nil {
		return "", err
	}
	return alert.ReplaceAllStringFunc(out.String(), func(m string) string {
		kind := alert.FindStringSubmatch(m)[1]
		return `<blockquote class="alert ` + strings.ToLower(kind) + `"><p><strong>` + strings.ToUpper(kind[:1]) + strings.ToLower(kind[1:]) + `</strong> `
	}), nil
}
