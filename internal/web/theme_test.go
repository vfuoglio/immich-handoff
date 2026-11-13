package web_test

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/web"
)

func TestNormalizeTheme(t *testing.T) {
	for in, want := range map[string]struct {
		v  string
		ok bool
	}{"": {"twilight", true}, "twilight": {"twilight", true}, " Immich ": {"immich", true}, "IMMICH": {"immich", true}, "neon": {"twilight", false}} {
		if v, ok := web.NormalizeTheme(in); v != want.v || ok != want.ok {
			t.Errorf("NormalizeTheme(%q) = %q, %v; want %q, %v", in, v, ok, want.v, want.ok)
		}
	}
	for in, want := range map[string]struct {
		v  string
		ok bool
	}{"": {"auto", true}, "auto": {"auto", true}, "Dark": {"dark", true}, "light": {"light", true}, "sepia": {"auto", false}} {
		if v, ok := web.NormalizeThemeMode(in); v != want.v || ok != want.ok {
			t.Errorf("NormalizeThemeMode(%q) = %q, %v; want %q, %v", in, v, ok, want.v, want.ok)
		}
	}
}

// With nothing set the page starts in twilight and follows the browser; THEME and THEME_MODE change where it starts,
// and the menu in the bar offers both schemes whatever the administrator chose.
func TestThePageStartsInTheAdministratorsThemeAndOffersBoth(t *testing.T) {
	app, _ := setup(t)
	_, body := do(t, "GET", app.URL+"/", "sess=alice", nil)
	if !strings.Contains(body, `data-palette="twilight"`) || strings.Contains(body, `<html lang="en" data-palette="twilight" data-theme`) {
		t.Errorf("by default: twilight, and the variant is left to the browser: %.200s", body)
	}
	for _, want := range []string{`data-palette="twilight"`, `data-palette="immich"`, `class="scheme"`, `id="theme"`, `content="#f5f2fd"`, `content="#0d0b20"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	app, _ = setup(t, func(s *web.Server) { s.Theme, s.ThemeMode = "immich", "dark" })
	_, body = do(t, "GET", app.URL+"/", "sess=alice", nil)
	if !regexp.MustCompile(`<html[^>]*data-palette="immich"`).MatchString(body) || !regexp.MustCompile(`<html[^>]*data-theme="dark"`).MatchString(body) {
		t.Errorf("THEME=immich THEME_MODE=dark: %.250s", body)
	}
	if !strings.Contains(body, `content="#07080d"`) || !strings.Contains(body, `content="#f5f7fd"`) {
		t.Error("the phone's address bar must match the scheme")
	}
	if !regexp.MustCompile(`data-palette="immich" aria-checked="true"`).MatchString(body) {
		t.Error("the menu must show the scheme the page starts in")
	}
}

// Both schemes define every colour the page uses, for light and dark, and a person's own choice is applied before painting.
func TestBothSchemesDefineTheSameColoursAndTheScriptRemembersTheChoice(t *testing.T) {
	app, _ := setup(t)
	res, err := http.Get(app.URL + "/static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	css := string(b)
	block := func(sel string) string {
		i := strings.Index(css, sel+" {")
		if i < 0 {
			t.Fatalf("no rule for %s", sel)
		}
		return css[i : i+strings.Index(css[i:], "\n}")]
	}
	names := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, x := range regexp.MustCompile(`(--[a-z-]+):`).FindAllStringSubmatch(s, -1) {
			m[x[1]] = true
		}
		return m
	}
	tw, im := names(block(`:root, :root[data-palette="twilight"]`)), names(block(`:root[data-palette="immich"]`))
	for n := range tw {
		if !im[n] {
			t.Errorf("immich does not define %s", n)
		}
	}
	for n := range im {
		if !tw[n] {
			t.Errorf("twilight does not define %s", n)
		}
	}
	for _, n := range []string{"--bg", "--surface", "--text", "--muted", "--border", "--accent", "--on-accent", "--grad"} {
		if !tw[n] {
			t.Errorf("a scheme must define %s", n)
		}
	}
	res, err = http.Get(app.URL + "/static/theme.js")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(res.Body)
	res.Body.Close()
	for _, want := range []string{"handoff-palette", "handoff-theme", "immich", "twilight"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("theme.js lost %q", want)
		}
	}
}

// The wall and the frame run on old televisions and tablets, so everything that moves there moves with transform and
// opacity alone (which the graphics card does without redrawing), and reduced motion stops all of it.
func TestTheWallAndFrameAnimateOnlyWhatIsCheapAndStopForReducedMotion(t *testing.T) {
	app, _ := setup(t)
	res, err := http.Get(app.URL + "/static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	css := string(b)
	names := regexp.MustCompile(`@keyframes (wall-[a-z-]+) \{`).FindAllStringSubmatch(css, -1)
	if len(names) < 6 {
		t.Fatalf("the wall's animations are missing: %v", names)
	}
	for _, n := range names {
		start := strings.Index(css, "@keyframes "+n[1]+" {")
		rest := css[start:]
		// a keyframes rule ends where its braces balance
		depth, end := 0, 0
		for i, c := range rest {
			if c == '{' {
				depth++
			} else if c == '}' {
				depth--
				if depth == 0 {
					end = i
					break
				}
			}
		}
		for _, prop := range regexp.MustCompile(`([a-z-]+)\s*:`).FindAllStringSubmatch(rest[strings.Index(rest, "{")+1:end], -1) {
			if prop[1] != "transform" && prop[1] != "opacity" {
				t.Errorf("%s animates %s; on a television only transform and opacity are cheap", n[1], prop[1])
			}
		}
	}
	i := strings.LastIndex(css, "@media (prefers-reduced-motion: reduce) {")
	if i < 0 {
		t.Fatal("no reduced-motion rule")
	}
	reduced := css[i:]
	for _, want := range []string{"wall-stage figure.arrive", "figure.single.fresh::after", ".wall-wait::before", `[data-fx] figure.single img`, "animation: none !important"} {
		if !strings.Contains(reduced, want) {
			t.Errorf("reduced motion must stop %s", want)
		}
	}
}
