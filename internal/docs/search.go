package docs

import (
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// Hit is one search result.
type Hit struct {
	Feature  string
	Title    string
	Tip      string
	Audience string
	Score    int
}

type doc struct {
	entry    Entry
	title    []string // words of the title, of the tip and of the headings, and of the text, lower-cased
	tip      []string
	headings []string
	text     map[string]bool
}

var (
	codeFence = regexp.MustCompile("(?s)```.*?```")
	headingRe = regexp.MustCompile(`(?m)^#{2,4} (.+)$`)
	linkText  = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
)

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

var index = sync.OnceValue(func() []doc {
	entries, err := Index()
	if err != nil {
		return nil
	}
	var out []doc
	for _, e := range entries {
		body, err := Source("en", e.Path)
		if err != nil {
			continue
		}
		d := doc{entry: e, title: words(e.Title), tip: words(e.Tip), text: map[string]bool{}}
		for _, m := range headingRe.FindAllStringSubmatch(body, -1) {
			d.headings = append(d.headings, words(linkText.ReplaceAllString(m[1], "$1"))...)
		}
		for _, w := range words(linkText.ReplaceAllString(codeFence.ReplaceAllString(body, " "), "$1")) {
			d.text[w] = true
		}
		out = append(out, d)
	}
	return out
})

// matches says how well a typed word fits a word of the page: a prefix is a good fit (so "stor" finds "stories"), and a word of
// four letters or more may be one letter off ("storys").
func matches(q, w string) bool {
	if strings.HasPrefix(w, q) {
		return true
	}
	return len([]rune(q)) >= 4 && oneOff(q, w)
}

func oneOff(a, b string) bool {
	x, y := []rune(a), []rune(b)
	if len(x) > len(y) {
		x, y = y, x
	}
	if len(y)-len(x) > 1 {
		return false
	}
	i := 0
	for i < len(x) && x[i] == y[i] {
		i++
	}
	if len(x) == len(y) {
		return string(x[i+1:]) == string(y[i+1:]) // one letter changed (or none)
	}
	return string(x[i:]) == string(y[i+1:]) // one letter missing
}

func anyMatch(q string, ws []string) bool {
	for _, w := range ws {
		if matches(q, w) {
			return true
		}
	}
	return false
}

// Search finds pages for the words typed, best first. Every word must be found somewhere in the page; a word in the title counts
// most, then the tip, then a heading, then the text. An administrator's pages are left out unless admin is true.
func Search(query string, admin bool) []Hit {
	qs := words(query)
	if len(qs) == 0 {
		return nil
	}
	var hits []Hit
next:
	for _, d := range index() {
		if d.entry.Audience == "admin" && !admin {
			continue
		}
		score := 0
		for _, q := range qs {
			switch {
			case anyMatch(q, d.title):
				score += 10
			case anyMatch(q, d.tip):
				score += 6
			case anyMatch(q, d.headings):
				score += 4
			default:
				found := false
				for w := range d.text {
					if matches(q, w) {
						found = true
						break
					}
				}
				if !found {
					continue next
				}
				score++
			}
		}
		hits = append(hits, Hit{d.entry.Feature, d.entry.Title, d.entry.Tip, d.entry.Audience, score})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	return hits
}
