package docs

import (
	"regexp"
	"strings"
	"testing"
)

func TestTheBundleHoldsEveryPageForUsersAndAdmins(t *testing.T) {
	index, err := Index()
	if err != nil || len(index) < 30 {
		t.Fatalf("index: %d pages, %v", len(index), err)
	}
	in := map[string]bool{}
	for _, e := range index {
		in[e.Path] = true
		if _, _, p, ok := Tip(e.Feature); !ok || p != e.Path {
			t.Errorf("%s: its tip is not found by feature %q", e.Path, e.Feature)
		}
		if e.Audience == "contributor" {
			t.Errorf("%s: a contributors' page is in the app's bundle", e.Path)
		}
	}
	for _, p := range Paths() {
		if !in[p] {
			t.Errorf("%s is in the bundle but not in the index", p)
		}
	}
}

var (
	link  = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	image = regexp.MustCompile(`(?:src="|!\[[^\]]*\]\()([^")\s]+)`)
)

func TestEveryBundledPageRendersWithoutAnythingFromAnotherSite(t *testing.T) {
	for _, p := range Paths() {
		body, err := Source("en", p)
		if err != nil {
			t.Fatal(err)
		}
		out, err := Render(body)
		if err != nil || strings.TrimSpace(out) == "" {
			t.Errorf("%s: %v", p, err)
		}
		if strings.Contains(out, ":::") {
			t.Errorf("%s: a fence is left in the page", p)
		}
		for _, m := range image.FindAllStringSubmatch(body, -1) {
			if strings.HasPrefix(m[1], "http") {
				t.Errorf("%s: an image from another site (%s): the app loads nothing from outside", p, m[1])
			}
		}
	}
}

func TestAPageInAnotherLanguageFallsBackToEnglish(t *testing.T) {
	en, err := Source("en", "start/quickstart.md")
	if err != nil {
		t.Fatal(err)
	}
	if de, err := Source("de", "start/quickstart.md"); err != nil || de != en {
		t.Errorf("no fallback: %v", err)
	}
	if _, err := Source("en", "../../go.mod"); err == nil {
		t.Error("a path out of the bundle is read")
	}
}
