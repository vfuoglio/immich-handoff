package docs

import (
	"strings"
	"testing"
)

func TestParseReadsTheFrontMatterAndKeepsTheBody(t *testing.T) {
	p, err := Parse("guides/show/stories.md", []byte("---\ntitle: \"Stories\"\nfeature: stories\naudience: user\ngoal: show\norder: 40\ntip: \"A recap.\"\n---\n\n# Stories\n\nText.\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Feature != "stories" || p.Goal != "show" || p.Order != 40 || p.Tip != "A recap." || !strings.HasPrefix(p.Body, "# Stories") {
		t.Fatalf("%+v", p)
	}
}

func TestParseRefusesAnUnknownKeyAndAnOpenFrontMatter(t *testing.T) {
	if _, err := Parse("a.md", []byte("---\ntitel: x\n---\nbody")); err == nil {
		t.Error("a misspelt key is accepted")
	}
	if _, err := Parse("a.md", []byte("---\ntitle: x\nbody")); err == nil {
		t.Error("a front matter that never closes is accepted")
	}
}

func TestAPageWithoutFrontMatterIsAllBody(t *testing.T) {
	p, err := Parse("README.md", []byte("# Docs\n"))
	if err != nil || p.Title != "" || p.Body != "# Docs\n" {
		t.Fatalf("%+v %v", p, err)
	}
}

func page(path, feature string) Page {
	return Page{Path: path, Title: "T", Feature: feature, Audience: "user", Goal: "show", Order: 10, Tip: "A tip."}
}

func TestValidateCatchesWhatWouldBreakTheLinksAndTheTooltips(t *testing.T) {
	cases := map[string]struct {
		pages []Page
		want  string
	}{
		"duplicate feature": {[]Page{page("a.md", "x"), page("b.md", "x")}, `feature "x" is also`},
		"bad key":           {[]Page{page("a.md", "Not Kebab")}, "lower-case key"},
		"bad goal":          {[]Page{{Path: "a.md", Title: "T", Feature: "x", Audience: "user", Goal: "play", Order: 1, Tip: "t"}}, `goal "play"`},
		"bad audience":      {[]Page{{Path: "a.md", Title: "T", Feature: "x", Audience: "robot", Goal: "show", Order: 1, Tip: "t"}}, `audience "robot"`},
		"no order":          {[]Page{{Path: "a.md", Title: "T", Feature: "x", Audience: "user", Goal: "show", Tip: "t"}}, "no order"},
		"no tip":            {[]Page{{Path: "a.md", Title: "T", Feature: "x", Audience: "user", Goal: "show", Order: 1}}, "tip is missing"},
		"long tip":          {[]Page{{Path: "a.md", Title: "T", Feature: "x", Audience: "user", Goal: "show", Order: 1, Tip: strings.Repeat("a", 141)}}, "tip is missing"},
		"no title":          {[]Page{{Path: "a.md", Feature: "x", Audience: "user", Goal: "show", Order: 1, Tip: "t"}}, "no title"},
	}
	for name, c := range cases {
		got := strings.Join(Validate(c.pages), "\n")
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: %q does not mention %q", name, got, c.want)
		}
	}
	if bad := Validate([]Page{page("a.md", "x"), page("b.md", "y"), {Path: "contributing/architecture.md"}, {Path: "README.md"}}); len(bad) != 0 {
		t.Errorf("a good set is refused: %v", bad)
	}
}

func TestAlertsTabsAndTablesRender(t *testing.T) {
	out, err := Render("> [!WARNING]\n> Careful.\n\n:::tabs\n### Compose\n```bash\ndocker compose up -d\n```\n### Run\nUse `docker run`.\n:::\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n## A heading\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`class="alert warning"><p><strong>Warning</strong> Careful.`, `<div class="doc-tab" data-label="Compose">`, `<div class="doc-tab" data-label="Run">`, "docker compose up -d", "<table>", `id="a-heading"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, ":::") {
		t.Errorf("the fence is left in:\n%s", out)
	}
}

func TestAPageThatIsNotATabsFenceIsLeftAlone(t *testing.T) {
	in := "Plain text with ::: in the middle.\n"
	if got := ExpandTabs(in); got != in {
		t.Errorf("%q", got)
	}
}
