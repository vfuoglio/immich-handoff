package docs

import (
	"regexp"
	"strings"
	"testing"
)

var links = Links{Base: "../", Site: "https://site.example/docs", Repo: "https://github.com/x/y"}

func TestLinksInHelpGoToHelpTheSiteOrTheRepository(t *testing.T) {
	body := "[a page](../automate/rules.md#a-heading) [contributors](../../contributing/architecture.md) [changelog](../../../CHANGELOG.md) [out](https://example.org/x) [here](#top)\n\n![pic](../../images/hero.png)\n"
	out, err := RenderHelp("guides/show/stories.md", body, links)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<a href="../help/rules#a-heading"`,
		`<a class="leaves" target="_blank" rel="noopener noreferrer" href="https://site.example/docs/contributing/architecture/"`,
		`href="https://github.com/x/y/blob/main/CHANGELOG.md"`,
		`href="https://example.org/x"`,
		`<a href="#top"`,
		`src="../help/img/hero.jpg"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in\n%s", want, out)
		}
	}
}

// every link between pages of the bundle goes to a page that is in it, every anchor to a heading that is there, every picture to
// one that was kept, and no page shows a picture from another site
func TestEveryLinkInHelpWorks(t *testing.T) {
	index, _ := Index()
	ids := map[string]map[string]bool{}
	html := map[string]string{}
	idRe := regexp.MustCompile(`id="([^"]+)"`)
	for _, e := range index {
		body, _ := Source("en", e.Path)
		out, err := RenderHelp(e.Path, body, links)
		if err != nil {
			t.Fatal(err)
		}
		html[e.Feature] = out
		ids[e.Feature] = map[string]bool{}
		for _, m := range idRe.FindAllStringSubmatch(out, -1) {
			ids[e.Feature][m[1]] = true
		}
	}
	hrefRe := regexp.MustCompile(`<a href="\.\./help/([a-z0-9-]+)(?:#([^"]+))?"`)
	imgRe := regexp.MustCompile(`<img loading="lazy" src="\.\./help/img/([^"]+)"`)
	for f, out := range html {
		for _, m := range hrefRe.FindAllStringSubmatch(out, -1) {
			if _, ok := ids[m[1]]; !ok {
				t.Errorf("%s links to %s, which is not a page of Help", f, m[1])
			} else if m[2] != "" && !ids[m[1]][m[2]] {
				t.Errorf("%s links to %s#%s, which is not a heading there", f, m[1], m[2])
			}
		}
		for _, m := range imgRe.FindAllStringSubmatch(out, -1) {
			if _, _, ok := Image(m[1]); !ok {
				t.Errorf("%s shows %s, which the bundle does not hold", f, m[1])
			}
		}
		if strings.Contains(out, `src="http`) {
			t.Errorf("%s shows a picture from another site", f)
		}
	}
}

func TestThePicturesAreSlim(t *testing.T) {
	total := 0
	index, _ := Index()
	seen := map[string]bool{}
	for _, e := range index {
		body, _ := Source("en", e.Path)
		out, _ := RenderHelp(e.Path, body, links)
		for _, m := range regexp.MustCompile(`help/img/([^"]+)"`).FindAllStringSubmatch(out, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				b, _, _ := Image(m[1])
				total += len(b)
			}
		}
	}
	if total > 3<<20 {
		t.Errorf("the pictures take %d KB; the budget is 3 MB", total/1024)
	}
	if _, _, ok := Image("../tips.json"); ok {
		t.Error("a path out of the pictures is read")
	}
}

func TestSearchFindsByWordPrefixAndOneTypo(t *testing.T) {
	for _, q := range []string{"stories", "stor", "storys", "Stories recap"} {
		hits := Search(q, false)
		if len(hits) == 0 || hits[0].Feature != "stories" {
			t.Errorf("%q: %+v", q, hits)
		}
	}
	if hits := Search("zzzqqq", false); len(hits) != 0 {
		t.Errorf("a nonsense word finds %+v", hits)
	}
	if Search("", false) != nil {
		t.Error("an empty search finds something")
	}
}

func TestSearchKeepsAdministratorPagesFromOthers(t *testing.T) {
	for _, h := range Search("install", false) {
		if h.Audience == "admin" {
			t.Errorf("a user finds the administrator's page %s", h.Feature)
		}
	}
	found := false
	for _, h := range Search("install", true) {
		found = found || h.Feature == "install"
	}
	if !found {
		t.Error("an administrator does not find the install page")
	}
}

func TestOneOff(t *testing.T) {
	for _, c := range [][2]string{{"storys", "story"}, {"stoies", "stories"}, {"storiez", "stories"}} {
		if !oneOff(c[0], c[1]) {
			t.Errorf("%s should be one letter off %s", c[0], c[1])
		}
	}
	for _, c := range [][2]string{{"abcd", "abxy"}, {"story", "stories"}} {
		if oneOff(c[0], c[1]) {
			t.Errorf("%s is more than one letter off %s", c[0], c[1])
		}
	}
}
