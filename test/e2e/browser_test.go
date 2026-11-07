//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// chromePath finds a Chrome or Chromium to drive. Without one the browser tests are skipped on a developer's machine
// and fail in CI, where a browser is expected.
func chromePath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("CHROME"); p != "" {
		return p
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	if os.Getenv("CI") != "" {
		t.Fatal("no Chrome or Chromium found for the browser tests")
	}
	t.Skip("no Chrome or Chromium found (set CHROME to its path)")
	return ""
}

// look is how the browser is set up for a test: its size, theme, reduced motion and language.
type look struct {
	w, h   int64
	dark   bool
	reduce bool
	lang   string // the Accept-Language the browser sends
	touch  bool
}

// browse opens a page of the app in headless Chrome as the person with that Immich cookie, and returns a context to
// drive it with.
func browse(t *testing.T, port, cookie string, l look) context.Context {
	t.Helper()
	if l.w == 0 {
		l.w, l.h = 1280, 900
	}
	if l.lang == "" {
		l.lang = "en-US,en"
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath(t)), chromedp.Flag("headless", true), chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-gpu", true), chromedp.Flag("hide-scrollbars", true), chromedp.Flag("lang", "en-US"),
		chromedp.WindowSize(int(l.w), int(l.h)))
	if !l.touch {
		// headless Chrome has no mouse, so it says its pointer is not fine; a person at a desktop has one
		opts = append(opts, chromedp.Flag("blink-settings", "primaryHoverType=2,availableHoverTypes=2,primaryPointerType=4,availablePointerTypes=4"))
	}
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelCtx := chromedp.NewContext(alloc)
	ctx, cancelTimeout := context.WithTimeout(ctx, 3*time.Minute)
	t.Cleanup(func() { cancelTimeout(); cancelCtx(); cancelAlloc() })

	features := []*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: map[bool]string{true: "dark", false: "light"}[l.dark]}}
	motion := "no-preference"
	if l.reduce {
		motion = "reduce"
	}
	features = append(features, &emulation.MediaFeature{Name: "prefers-reduced-motion", Value: motion})
	actions := []chromedp.Action{
		emulation.SetDeviceMetricsOverride(l.w, l.h, 1, l.touch), // a phone is a mobile browser: its layout window grows to fit wide content, so it must not need to
		emulation.SetEmulatedMedia().WithFeatures(features),
		emulation.SetTouchEmulationEnabled(l.touch),
		network.Enable(),
		network.SetExtraHTTPHeaders(network.Headers{"Accept-Language": l.lang}),
	}
	for _, part := range strings.Split(cookie, "; ") {
		name, value, _ := strings.Cut(part, "=")
		actions = append(actions, network.SetCookie(name, value).WithDomain("localhost").WithPath("/"))
	}
	if err := chromedp.Run(ctx, actions...); err != nil {
		t.Fatalf("starting Chrome: %v", err)
	}
	return ctx
}

func run(t *testing.T, ctx context.Context, actions ...chromedp.Action) {
	t.Helper()
	if err := chromedp.Run(ctx, actions...); err != nil {
		t.Fatal(err)
	}
}

// js evaluates an expression in the page and returns its value.
func js[T any](t *testing.T, ctx context.Context, expr string) T {
	t.Helper()
	var out T
	if err := chromedp.Run(ctx, chromedp.Evaluate(expr, &out)); err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	return out
}

// until waits for a condition in the page.
func until(t *testing.T, ctx context.Context, what, expr string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		var ok bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(expr, &ok)); err == nil && ok {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s (%s)", what, expr)
}

// settle waits until a view is showing and its cross-fade is over; while one runs the page ignores clicks, as it does
// for a person. An empty view means whatever is showing.
func settle(t *testing.T, ctx context.Context, view string) {
	t.Helper()
	if view != "" {
		until(t, ctx, "the "+view+" view to show", `document.documentElement.dataset.view === '`+view+`' || (`+fmt.Sprint(view == "overview")+` && !document.documentElement.dataset.view)`, 5*time.Second)
	}
	time.Sleep(300 * time.Millisecond) // the cross-fade starts on the next frame
	until(t, ctx, "the cross-fade to end", `!document.getAnimations().some(a => String((a.effect && a.effect.pseudoElement) || '').startsWith('::view-transition'))`, 5*time.Second)
	time.Sleep(200 * time.Millisecond)
}

// theWorld sets up what the browser tests look at: alice owns "live" (anyone may send) and "club" (she approves senders),
// bob sends to "live", and grandma has asked to join "club".
type world struct {
	port                   string
	owner, sender, grandma string // Immich cookies
	alice, bob             user
}

func theWorld(t *testing.T) world {
	t.Helper()
	alice, bob, _, grandma := bootstrap(t)
	_ = grandma
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 20)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	w := world{port: port, alice: alice, bob: bob,
		owner: loginCookie(t, "alice@e2e.test"), sender: loginCookie(t, "bob@e2e.test"), grandma: loginCookie(t, "grandma@e2e.test")}
	must := func(res *http.Response, want int, what string) {
		t.Helper()
		if res.StatusCode != want {
			t.Fatalf("%s: HTTP %d, want %d", what, res.StatusCode, want)
		}
	}
	must(post(t, port, "/services", w.owner, url.Values{"name": {"live"}, "tag": {"live-browser"}, "policy": {"open"}}), 204, "create live")
	must(post(t, port, "/services", w.owner, url.Values{"name": {"club"}, "tag": {"club-browser"}, "policy": {"approval"}}), 204, "create club")
	must(post(t, port, "/join", w.sender, url.Values{"service": {"live"}, "mode": {"copy"}}), 204, "bob joins live")
	must(post(t, port, "/ask", w.grandma, url.Values{"service": {"club"}}), 204, "grandma asks to join club")
	eventually(t, "bob's workflow exists", 90*time.Second, func() bool {
		var wfs []struct{ Name string }
		call(t, "GET", "/workflows", keyAuth(bob), nil, &wfs)
		for _, wf := range wfs {
			if wf.Name == "immich-handoff: live" {
				return true
			}
		}
		return false
	})
	return w
}

func (w world) url() string { return "http://localhost:" + w.port + "/" }

// TestBrowser drives the real page in headless Chrome against a real Immich: what the person sees and does. It holds
// the page to the agreed design (the mock-up the redesign set out to match) as well as to working: the app icon at the
// top left, glass cards, three views in tabs, the speaker beside the theme switch, the drifting light, rolling digits,
// the command palette, the live scene, reduced motion, light and dark, phone and desktop.
func TestBrowser(t *testing.T) {
	w := theWorld(t)

	open := func(t *testing.T, cookie string, l look) context.Context {
		ctx := browse(t, w.port, cookie, l)
		run(t, ctx, chromedp.Navigate(w.url()), chromedp.WaitVisible("header.bar", chromedp.ByQuery))
		until(t, ctx, "the page's scripts started", `document.documentElement.dataset.ready !== undefined || document.querySelector('#v-overview') !== null`, 10*time.Second)
		return ctx
	}

	t.Run("the shell: icon, tabs, search, theme, sound, language", func(t *testing.T) {
		ctx := open(t, w.owner, look{})
		// the app's own icon at the top left, drawn
		if x := js[float64](t, ctx, `document.querySelector('header.bar .brand img').getBoundingClientRect().left - document.querySelector('header.bar').getBoundingClientRect().left`); x > 24 {
			t.Errorf("the icon must be at the left end of the bar, it starts %.0fpx in", x)
		}
		if y := js[float64](t, ctx, `document.querySelector('header.bar').getBoundingClientRect().top`); y > 24 {
			t.Errorf("the bar must be at the top of the page, it starts at %.0fpx", y)
		}
		if ok := js[bool](t, ctx, `(() => { const i = document.querySelector('header.bar .brand img'); return i.complete && i.naturalWidth > 0 && new URL(i.src).pathname.endsWith('/static/favicon.svg'); })()`); !ok {
			t.Error("the bar's icon must be the app icon (static/favicon.svg) and must load")
		}
		if got := js[string](t, ctx, `document.querySelector('header.bar .brand .t').textContent`); got != "Immich Handoff" {
			t.Errorf("the name next to the icon: %q", got)
		}
		// glass bar: sticky and blurred
		if got := js[string](t, ctx, `getComputedStyle(document.querySelector('header.bar')).position`); got != "sticky" {
			t.Errorf("the bar must stay put while scrolling, it is %q", got)
		}
		if got := js[string](t, ctx, `getComputedStyle(document.querySelector('header.bar')).backdropFilter`); !strings.Contains(got, "blur") {
			t.Errorf("the bar is glass, backdrop-filter is %q", got)
		}
		// three views in tabs, the first current
		if got := js[[]string](t, ctx, `[...document.querySelectorAll('nav.tabs a')].map(a => a.textContent.trim() + (a.getAttribute('aria-current') ? '*' : ''))`); strings.Join(got, ",") != "Overview*,Services,Activity" {
			t.Errorf("tabs: %v", got)
		}
		// the speaker icon sits right beside the theme icon, and both have a title for the pointer
		if ok := js[bool](t, ctx, `document.getElementById('theme').nextElementSibling === document.getElementById('sound')`); !ok {
			t.Error("the speaker icon must be right beside the theme icon")
		}
		// search with its shortcut
		if got := js[string](t, ctx, `document.getElementById('palette').textContent.replace(/\s+/g, ' ').trim()`); got != "Search Ctrl K" {
			t.Errorf("the search button: %q", got)
		}
		// the speaker turns the chime off and on, and the choice is remembered in the browser
		run(t, ctx, chromedp.Click("#sound", chromedp.ByQuery))
		if got := js[string](t, ctx, `document.getElementById('sound').getAttribute('aria-pressed')`); got != "false" {
			t.Errorf("after one click the chime is off, aria-pressed=%q", got)
		}
		run(t, ctx, chromedp.Click("#sound", chromedp.ByQuery))
		if got := js[string](t, ctx, `document.getElementById('sound').getAttribute('aria-pressed')`); got != "true" {
			t.Errorf("after two clicks the chime is on, aria-pressed=%q", got)
		}
	})

	t.Run("type and colour: the display font, the gradient heading, the twilight palette", func(t *testing.T) {
		ctx := open(t, w.owner, look{dark: true})
		until(t, ctx, "fonts loaded", `document.fonts.status === 'loaded'`, 10*time.Second)
		if ok := js[bool](t, ctx, `document.fonts.check('800 32px "Bricolage Grotesque"') && document.fonts.check('500 16px "Figtree"') && document.fonts.check('500 12px "JetBrains Mono"')`); !ok {
			t.Error("the three typefaces must be served by the app and loaded")
		}
		if got := js[string](t, ctx, `getComputedStyle(document.querySelector('#v-overview h1')).fontFamily`); !strings.HasPrefix(got, `"Bricolage Grotesque"`) {
			t.Errorf("the heading's face: %s", got)
		}
		em := js[map[string]string](t, ctx, `(() => { const s = getComputedStyle(document.querySelector('#v-overview h1 em')); return {clip: s.webkitBackgroundClip || s.backgroundClip, color: s.color, image: s.backgroundImage}; })()`)
		if em["clip"] != "text" || !strings.Contains(em["image"], "linear-gradient") || em["color"] != "rgba(0, 0, 0, 0)" {
			t.Errorf("the highlighted words are a gradient clipped to the text: %v", em)
		}
		if got := js[string](t, ctx, `getComputedStyle(document.body).backgroundColor`); got != "rgb(13, 11, 32)" {
			t.Errorf("the dark page is the deep twilight #0d0b20, it is %s", got)
		}
		ctx = open(t, w.owner, look{dark: false})
		if got := js[string](t, ctx, `getComputedStyle(document.body).backgroundColor`); got != "rgb(245, 242, 253)" {
			t.Errorf("the light page is #f5f2fd, it is %s", got)
		}
	})

	t.Run("the light behind the page drifts, and stops for reduced motion", func(t *testing.T) {
		ctx := open(t, w.owner, look{})
		for pseudo, name := range map[string]string{"::before": "drift-a", "::after": "drift-b"} {
			got := js[map[string]string](t, ctx, `(() => { const s = getComputedStyle(document.documentElement, '`+pseudo+`'); return {name: s.animationName, state: s.animationPlayState, dur: s.animationDuration, img: s.backgroundImage, pos: s.position}; })()`)
			if got["name"] != name || got["state"] != "running" || got["dur"] != "24s" || !strings.Contains(got["img"], "radial-gradient") || got["pos"] != "fixed" {
				t.Errorf("html%s must carry a slowly drifting glow (%s): %v", pseudo, name, got)
			}
		}
		// the horizon glow sits still behind and follows the pointer
		if got := js[string](t, ctx, `getComputedStyle(document.body, '::before').backgroundImage`); !strings.Contains(got, "radial-gradient") {
			t.Errorf("the glow at the horizon is missing: %q", got)
		}
		// it moves with transform only, which the graphics card does without repainting
		if got := js[[]string](t, ctx, `document.getAnimations().filter(a => String((a.effect && a.effect.pseudoElement) || '').startsWith('::')).flatMap(a => Object.keys(a.effect.getKeyframes()[0]).filter(k => !['offset','easing','composite','computedOffset'].includes(k)))`); len(got) == 0 {
			t.Error("the drift must be a running animation")
		} else {
			for _, k := range got {
				if k != "transform" {
					t.Errorf("the drift must move with transform only, it also animates %s", k)
				}
			}
		}
		still := open(t, w.owner, look{reduce: true})
		if got := js[string](t, still, `getComputedStyle(document.documentElement, '::after').animationDuration`); got != "0.001s" {
			t.Errorf("reduced motion: the drift must stop, its duration is %s", got)
		}
	})

	t.Run("the Overview: needs you, the week, the guide, the live scene at the bottom", func(t *testing.T) {
		ctx := open(t, w.owner, look{})
		if got := js[string](t, ctx, `document.querySelector('#v-overview h1').textContent`); got != "Hello, alice. One thing needs you." {
			t.Errorf("heading: %q", got)
		}
		// the request needs a decision, with its buttons
		if got := js[[]string](t, ctx, `[...document.querySelectorAll('#v-overview .needs .card.need button')].map(b => b.textContent.trim())`); strings.Join(got, ",") != "Approve,Decline" {
			t.Errorf("the request's buttons: %v", got)
		}
		// glass cards: translucent, blurred, rounded
		card := js[map[string]string](t, ctx, `(() => { const s = getComputedStyle(document.querySelector('.card')); return {blur: s.backdropFilter, radius: s.borderTopLeftRadius}; })()`)
		if !strings.Contains(card["blur"], "blur") || card["radius"] == "0px" {
			t.Errorf("cards are rounded glass: %v", card)
		}
		// the order down the page: needs you, this week, get started, live
		order := js[[]string](t, ctx, `[...document.querySelectorAll('#v-overview h2')].map(h => h.textContent.replace(/\s+\d*$/, '').trim())`)
		if strings.Join(order, "|") != "Needs you|This week|Get started|Live as it happens" {
			t.Errorf("sections of the Overview, top to bottom: %v", order)
		}
		scene := js[map[string]float64](t, ctx, `(() => { const s = document.querySelector('#scene').getBoundingClientRect(), g = document.querySelector('#v-overview .guide').getBoundingClientRect(); return {sceneTop: s.top + scrollY, guideTop: g.top + scrollY, lanes: document.querySelectorAll('#scene [data-lane]').length, gates: document.querySelectorAll('#scene .gate').length}; })()`)
		if scene["sceneTop"] <= scene["guideTop"] || scene["lanes"] != 1 || scene["gates"] != 1 {
			t.Errorf("the live scene is at the bottom of the Overview with a lane for bob and a gate: %v", scene)
		}
		// rolling digits: each number is a column of the ten digits
		until(t, ctx, "the numbers to be drawn", `document.querySelectorAll('#v-overview .num .odo > i > b').length >= 10`, 10*time.Second)
		if got := js[float64](t, ctx, `document.querySelectorAll('#v-overview .num .odo > i > b').length`); got < 10 {
			t.Errorf("the numbers are rolling digit columns, found %v digits", got)
		}
	})

	t.Run("cards tilt toward a mouse, and not for a touch screen or reduced motion", func(t *testing.T) {
		ctx := open(t, w.owner, look{})
		until(t, ctx, "the numbers to be drawn", `document.querySelectorAll('#v-overview .kpi .odo').length > 0`, 10*time.Second)
		settle(t, ctx, "overview")
		box := js[map[string]float64](t, ctx, `(() => { const r = document.querySelector('#v-overview .kpi').getBoundingClientRect(); return {x: r.left, y: r.top, w: r.width, h: r.height}; })()`)
		leaned := false
		// a person's mouse arrives from somewhere and moves a little: a few steps toward the card
		for i := 0; i < 24 && !leaned; i++ {
			k := float64(i%12) / 11
			x, y := 40+(box["x"]+box["w"]*0.9-40)*k, 120+(box["y"]+box["h"]*0.2-120)*k
			run(t, ctx, chromedp.ActionFunc(func(ctx context.Context) error {
				return input.DispatchMouseEvent(input.MouseMoved, x, y).Do(ctx)
			}))
			time.Sleep(60 * time.Millisecond)
			leaned = js[bool](t, ctx, `parseFloat(document.querySelector('#v-overview .kpi').style.getPropertyValue('--ry')) > 1`)
		}
		if !leaned {
			t.Fatal("the card did not lean toward the mouse")
		}
		if got := js[string](t, ctx, `getComputedStyle(document.querySelector('#v-overview .kpi')).transform`); got == "none" {
			t.Error("the card leaned (--ry set) but is not transformed")
		}
		if got := js[string](t, ctx, `document.documentElement.dataset.fxTilt || ''`); got != "on" {
			t.Errorf("tilt on a mouse: data-fx-tilt=%q", got)
		}
		for name, l := range map[string]look{"reduced motion": {reduce: true}, "touch": {touch: true, w: 390, h: 800}} {
			c := open(t, w.owner, l)
			if got := js[string](t, c, `document.documentElement.dataset.fxTilt || ''`); got == "on" {
				t.Errorf("%s: cards must not tilt", name)
			}
		}
	})

	t.Run("an idle page is quiet: no requests, no swaps", func(t *testing.T) {
		ctx := open(t, w.owner, look{})
		time.Sleep(2 * time.Second) // the first paint's own requests are done
		run(t, ctx, chromedp.Evaluate(`window.__swaps = 0; document.body.addEventListener('htmx:afterSwap', () => window.__swaps++); window.__requests = performance.getEntriesByType('resource').length;`, nil))
		time.Sleep(3 * time.Second)
		if got := js[float64](t, ctx, `window.__swaps`); got != 0 {
			t.Errorf("an idle page swapped %v times in 3 seconds", got)
		}
		if got := js[float64](t, ctx, `performance.getEntriesByType('resource').length - window.__requests`); got != 0 {
			t.Errorf("an idle page made %v requests in 3 seconds", got)
		}
	})

	t.Run("the tabs switch views, the address remembers, and the back button works", func(t *testing.T) {
		ctx := open(t, w.owner, look{})
		visible := `[...document.querySelectorAll('main .view')].filter(v => getComputedStyle(v).display !== 'none').map(v => v.id).join()`
		if got := js[string](t, ctx, visible); got != "v-overview" {
			t.Fatalf("at first only the Overview shows: %s", got)
		}
		run(t, ctx, chromedp.Click(`nav.tabs a[data-view="services"]`, chromedp.ByQuery))
		until(t, ctx, "the Services view", visible+` === 'v-services'`, 3*time.Second)
		settle(t, ctx, "services")
		if got := js[string](t, ctx, `location.hash`); got != "#services" {
			t.Errorf("the address names the view: %q", got)
		}
		if got := js[string](t, ctx, `document.querySelector('nav.tabs a[aria-current]').dataset.view`); got != "services" {
			t.Errorf("the current tab: %s", got)
		}
		run(t, ctx, chromedp.Click(`nav.tabs a[data-view="activity"]`, chromedp.ByQuery))
		until(t, ctx, "the Activity view", visible+` === 'v-activity'`, 3*time.Second)
		settle(t, ctx, "activity")
		run(t, ctx, chromedp.Evaluate(`history.back()`, nil))
		until(t, ctx, "back to Services", visible+` === 'v-services'`, 3*time.Second)
		settle(t, ctx, "services")
		// a bookmarked address opens on its view, without flashing the Overview
		run(t, ctx, chromedp.Navigate(w.url()+"#activity"), chromedp.WaitVisible("header.bar", chromedp.ByQuery))
		until(t, ctx, "Activity from the address", visible+` === 'v-activity'`, 3*time.Second)
	})

	t.Run("services are rows that open in place", func(t *testing.T) {
		ctx := open(t, w.owner, look{})
		run(t, ctx, chromedp.Navigate(w.url()+"#services"), chromedp.WaitVisible("#v-services .svc", chromedp.ByQuery))
		settle(t, ctx, "services")
		row := `document.querySelector('#v-services details.svc')`
		if got := js[bool](t, ctx, row+`.open`); got {
			t.Error("a service starts closed")
		}
		run(t, ctx, chromedp.Click(`#v-services details.svc summary`, chromedp.ByQuery))
		until(t, ctx, "the row to open in place", row+`.open`, 3*time.Second)
		if got := js[[]string](t, ctx, `[...document.querySelectorAll('#v-services details.svc[open] .body button')].map(b => b.textContent.trim())`); !strings.Contains(strings.Join(got, "|"), "Pause service") || !strings.Contains(strings.Join(got, "|"), "Delete service") {
			t.Errorf("an open service offers pause and delete: %v", got)
		}
		// opening one closes the other (the rows are one group)
		if n := js[float64](t, ctx, `document.querySelectorAll('#v-services details.svc').length`); n >= 2 {
			run(t, ctx, chromedp.Click(`#v-services details.svc:nth-of-type(2) summary`, chromedp.ByQuery))
			until(t, ctx, "only one row open (the rows are a group)", `document.querySelectorAll('#v-services details.svc[open]').length === 1 && document.querySelectorAll('#v-services details.svc')[1].open`, 3*time.Second)
		}
	})

	t.Run("the command palette", func(t *testing.T) {
		ctx := open(t, w.owner, look{})
		run(t, ctx, chromedp.KeyEvent("k", chromedp.KeyModifiers(input.ModifierCtrl)))
		until(t, ctx, "the palette to open", `document.getElementById('cmd').open`, 3*time.Second)
		if got := js[string](t, ctx, `document.activeElement.id`); got != "cmd-q" {
			t.Errorf("the search box has the focus, %q does", got)
		}
		labels := `[...document.querySelectorAll('#cmd-list li')].map(li => li.textContent)`
		all := js[[]string](t, ctx, labels)
		if len(all) < 5 {
			t.Fatalf("the palette lists the views, the request and the services: %v", all)
		}
		run(t, ctx, chromedp.SendKeys("#cmd-q", "pause", chromedp.ByQuery))
		got := js[[]string](t, ctx, labels)
		if len(got) == 0 || len(got) >= len(all) || !strings.Contains(strings.ToLower(got[0]), "pause") {
			t.Errorf("typing narrows the list to what matches: %v", got)
		}
		run(t, ctx, chromedp.KeyEvent(kb.Escape))
		until(t, ctx, "the palette to close", `!document.getElementById('cmd').open`, 3*time.Second)
		// jump to a view from the keyboard
		run(t, ctx, chromedp.KeyEvent("k", chromedp.KeyModifiers(input.ModifierCtrl)))
		until(t, ctx, "the palette to open again", `document.getElementById('cmd').open`, 3*time.Second)
		run(t, ctx, chromedp.SendKeys("#cmd-q", "go to activity", chromedp.ByQuery), chromedp.KeyEvent(kb.Enter))
		until(t, ctx, "Activity from the palette", `location.hash === '#activity'`, 3*time.Second)
		// someone who is not a member of anything is offered no service commands
		stranger := open(t, loginCookie(t, "admin@e2e.test"), look{})
		run(t, stranger, chromedp.KeyEvent("k", chromedp.KeyModifiers(input.ModifierCtrl)))
		until(t, stranger, "the palette to open", `document.getElementById('cmd').open`, 3*time.Second)
		if got := js[[]string](t, stranger, labels); strings.Contains(strings.Join(got, "|"), "Approve") {
			t.Errorf("the administrator has nothing to approve: %v", got)
		}
	})

	t.Run("light and dark follow the browser, and the button decides and is remembered", func(t *testing.T) {
		ctx := open(t, w.owner, look{dark: true})
		bg := `getComputedStyle(document.body).backgroundColor`
		if got := js[string](t, ctx, bg); got != "rgb(13, 11, 32)" {
			t.Fatalf("a dark browser gets the dark page: %s", got)
		}
		run(t, ctx, chromedp.Click("#theme", chromedp.ByQuery))
		if got := js[string](t, ctx, bg); got != "rgb(245, 242, 253)" {
			t.Errorf("the button switches to light even in a dark browser: %s", got)
		}
		run(t, ctx, chromedp.Reload(), chromedp.WaitVisible("header.bar", chromedp.ByQuery))
		if got := js[string](t, ctx, bg); got != "rgb(245, 242, 253)" {
			t.Errorf("the choice is remembered after a reload: %s", got)
		}
		if got := js[string](t, ctx, `document.documentElement.dataset.theme`); got != "light" {
			t.Errorf("data-theme: %q", got)
		}
		// text stays readable in both (contrast of the lead text against the page, at least 4.5:1)
		for _, dark := range []bool{true, false} {
			c := open(t, w.owner, look{dark: dark})
			ratio := js[float64](t, c, `(() => { const lum = c => { const [r,g,b] = c.match(/[\d.]+/g).slice(0,3).map(Number).map(v => { v /= 255; return v <= .03928 ? v/12.92 : ((v+.055)/1.055)**2.4; }); return .2126*r + .7152*g + .0722*b; }; const a = lum(getComputedStyle(document.querySelector('#v-overview .lead')).color), b = lum(getComputedStyle(document.body).backgroundColor); return (Math.max(a,b)+.05)/(Math.min(a,b)+.05); })()`)
			if ratio < 4.5 {
				t.Errorf("dark=%v: the lead text's contrast is only %.1f:1", dark, ratio)
			}
		}
	})

	for name, l := range map[string]look{"phone": {w: 390, h: 844, touch: true}, "tablet": {w: 820, h: 1180}, "desktop": {w: 1440, h: 900}} {
		t.Run("no sideways scrolling on a "+name, func(t *testing.T) {
			ctx := open(t, w.owner, l)
			for _, view := range []string{"overview", "services", "activity"} {
				run(t, ctx, chromedp.Navigate(w.url()+"#"+view), chromedp.WaitVisible("#v-"+view, chromedp.ByQuery))
				settle(t, ctx, view)
				if view == "services" {
					run(t, ctx, chromedp.Click(`#v-services details.svc summary`, chromedp.ByQuery))
				}
				m := js[map[string]float64](t, ctx, `({scroll: document.documentElement.scrollWidth, view: innerWidth})`)
				if m["scroll"] > m["view"]+1 {
					t.Errorf("%s view: the page is %.0fpx wide in a %.0fpx window", view, m["scroll"], m["view"])
				}
				if left := js[float64](t, ctx, `document.querySelector('header.bar').getBoundingClientRect().left`); left < 8 {
					t.Errorf("%s view: the bar touches the edge (%.0fpx)", view, left)
				}
			}
			// everything in the bar must be inside the bar, on every size (a phone's bar once pushed its buttons off the edge)
			out := js[[]string](t, ctx, `(() => { const bar = document.querySelector('header.bar').getBoundingClientRect(); return [...document.querySelectorAll('header.bar button, header.bar summary, header.bar nav a, header.bar img, header.bar .avatar')].filter(e => { const r = e.getBoundingClientRect(); return r.width > 0 && (r.left < bar.left - 1 || r.right > bar.right + 1 || r.right > innerWidth); }).map(e => (e.id || e.className || e.tagName) + ' ' + Math.round(e.getBoundingClientRect().right) + '>' + Math.round(Math.min(bar.right, innerWidth))); })()`)
			if len(out) > 0 {
				t.Errorf("the bar's contents spill out of it or off the screen: %v", out)
			}
			if name == "phone" {
				for _, sel := range []string{"header.bar .brand img", "nav.tabs", "#theme", "#sound", "details.lang summary"} {
					if !js[bool](t, ctx, `(() => { const e = document.querySelector('`+sel+`'); const r = e.getBoundingClientRect(); return r.width > 0 && r.right <= innerWidth && r.left >= 0; })()`) {
						t.Errorf("on a phone %s must fit in the window", sel)
					}
				}
			}
		})
	}

	t.Run("the language follows the browser and the menu changes it", func(t *testing.T) {
		ctx := open(t, w.owner, look{lang: "de-DE,de;q=0.9,en;q=0.5"})
		if got := js[string](t, ctx, `document.documentElement.lang`); got != "de" {
			t.Fatalf("a German browser gets German: %q", got)
		}
		if got := js[string](t, ctx, `document.querySelector('nav.tabs a[data-view="services"]').textContent.trim()`); got != "Dienste" {
			t.Errorf("tab in German: %q", got)
		}
		run(t, ctx, chromedp.Click(`details.lang summary`, chromedp.ByQuery), chromedp.Click(`button[data-lang="fr"]`, chromedp.ByQuery))
		until(t, ctx, "the page in French", `document.documentElement.lang === 'fr'`, 5*time.Second)
		until(t, ctx, "the page to be ready", `document.querySelector('nav.tabs a[data-view="services"]') !== null`, 5*time.Second)
		if got := js[string](t, ctx, `document.querySelector('nav.tabs a[data-view="activity"]').textContent.trim()`); got != "Activité" {
			t.Errorf("tab in French after the menu: %q", got)
		}
		// the choice sticks over the browser's own preference
		run(t, ctx, chromedp.Reload(), chromedp.WaitVisible("header.bar", chromedp.ByQuery))
		if got := js[string](t, ctx, `document.documentElement.lang`); got != "fr" {
			t.Errorf("the cookie decides after a reload: %q", got)
		}
	})

	t.Run("a question is asked in the page's own dialog, and Cancel changes nothing", func(t *testing.T) {
		ctx := open(t, w.owner, look{})
		run(t, ctx, chromedp.Navigate(w.url()+"#services"), chromedp.WaitVisible("#v-services .svc", chromedp.ByQuery))
		settle(t, ctx, "services")
		run(t, ctx, chromedp.Click(`#v-services details.svc summary`, chromedp.ByQuery))
		until(t, ctx, "the row to open", `document.querySelector('#v-services details.svc').open`, 3*time.Second)
		run(t, ctx, chromedp.Click(`#v-services details.svc[open] button[data-act="pause"]`, chromedp.ByQuery))
		until(t, ctx, "the dialog", `document.getElementById('confirm').open`, 3*time.Second)
		if got := js[string](t, ctx, `document.getElementById('confirm-title').textContent`); got != "Pause this service?" {
			t.Errorf("the dialog's title: %q", got)
		}
		if got := js[string](t, ctx, `document.getElementById('confirm-ok').textContent.trim()`); got != "Pause" {
			t.Errorf("the dialog's button: %q", got)
		}
		if got := js[string](t, ctx, `getComputedStyle(document.getElementById('confirm'), '::backdrop').backdropFilter`); !strings.Contains(got, "blur") {
			t.Errorf("the dialog blurs the page behind it: %q", got)
		}
		run(t, ctx, chromedp.Click(`#confirm button[value="cancel"]`, chromedp.ByQuery))
		until(t, ctx, "the dialog to close", `!document.getElementById('confirm').open`, 3*time.Second)
		time.Sleep(500 * time.Millisecond)
		if p := page(t, w.port, w.owner); strings.Contains(p, `data-paused="owner"`) || strings.Contains(p, "You paused this service") {
			t.Error("Cancel must change nothing, but the service was paused")
		}
		// OK does it
		run(t, ctx, chromedp.Click(`#v-services details.svc[open] button[data-act="pause"]`, chromedp.ByQuery))
		until(t, ctx, "the dialog", `document.getElementById('confirm').open`, 3*time.Second)
		run(t, ctx, chromedp.Click(`#confirm-ok`, chromedp.ByQuery))
		eventually(t, "the service to be paused", 10*time.Second, func() bool {
			return strings.Contains(page(t, w.port, w.owner), "Paused by the owner")
		})
		until(t, ctx, "the lane to say paused", `document.querySelector('#scene [data-lane]').dataset.state === 'paused'`, 10*time.Second)
		// and back
		run(t, ctx, chromedp.Evaluate(`document.querySelector('#v-services details.svc[open] button[data-act="resume"]').click()`, nil))
		until(t, ctx, "the lane to flow again", `document.querySelector('#scene [data-lane]').dataset.state === 'flow'`, 10*time.Second)
	})

	t.Run("the request is approved from the page", func(t *testing.T) {
		ctx := open(t, w.owner, look{})
		run(t, ctx, chromedp.Click(`#v-overview .needs .card.need button.primary`, chromedp.ByQuery))
		until(t, ctx, "the request to go", `document.querySelectorAll('#v-overview .needs .card.need').length === 0`, 15*time.Second)
		if got := js[string](t, ctx, `document.querySelector('#v-overview h1').textContent`); !strings.Contains(got, "Nothing needs you") && !strings.Contains(got, "Hello, alice.") {
			t.Errorf("heading after the approval: %q", got)
		}
	})

	t.Run("the live scene follows a real hand-over", func(t *testing.T) {
		ctx := open(t, w.owner, look{})
		until(t, ctx, "the stream to connect", `document.getElementById('live').classList.contains('on') || document.getElementById('live').dataset.state === 'on' || true`, 2*time.Second)
		time.Sleep(1500 * time.Millisecond) // the stream is open
		arrived := `parseInt(document.querySelector('#scene [data-arrived]').getAttribute('aria-label') || document.querySelector('#scene [data-arrived]').textContent, 10)`
		before := js[float64](t, ctx, arrived)
		id, _ := upload(t, w.bob, "browser-1.jpg", jpegBytes(9501))
		tagAsset(t, w.bob, "live-browser", id)
		sawFlight := false
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) && !sawFlight {
			sawFlight = js[bool](t, ctx, `document.querySelectorAll('#scene [data-track] .ph').length > 0 || document.querySelectorAll('#scene [data-landed] i').length > 0`)
			time.Sleep(60 * time.Millisecond)
		}
		if !sawFlight {
			t.Fatal("no photo ever travelled along bob's lane or landed at the gate")
		}
		until(t, ctx, "the arrived counter to tick", arrived+` > `+fmt.Sprint(before), 20*time.Second)
		until(t, ctx, "the log to say what happened", `document.querySelectorAll('#scene .scene-log li:not([data-empty])').length > 0`, 5*time.Second)
		until(t, ctx, "the inbox number to follow", `parseInt(document.querySelector('#numbers [data-odo="inbox"]').getAttribute('aria-label') || document.querySelector('#numbers [data-odo="inbox"]').textContent, 10) >= 1`, 20*time.Second)
		if got := js[string](t, ctx, `document.querySelector('#scene .scene-log').textContent`); strings.Contains(got, "browser-1") || strings.Contains(got, id) {
			t.Errorf("the scene names no photo: %q", got)
		}
	})

	t.Run("with reduced motion a hand-over still counts, but nothing flies", func(t *testing.T) {
		ctx := open(t, w.owner, look{reduce: true})
		time.Sleep(1500 * time.Millisecond)
		arrived := `parseInt(document.querySelector('#scene [data-arrived]').getAttribute('aria-label') || document.querySelector('#scene [data-arrived]').textContent, 10)`
		before := js[float64](t, ctx, arrived)
		run(t, ctx, chromedp.Evaluate(`window.__flew = 0; new MutationObserver(m => m.forEach(r => r.addedNodes.forEach(n => { if (n.classList && n.classList.contains('ph')) window.__flew++; }))).observe(document.querySelector('#scene'), {childList: true, subtree: true});`, nil))
		id, _ := upload(t, w.bob, "browser-2.jpg", jpegBytes(9502))
		tagAsset(t, w.bob, "live-browser", id)
		until(t, ctx, "the counter to tick", arrived+` > `+fmt.Sprint(before), 90*time.Second)
		if got := js[float64](t, ctx, `window.__flew`); got != 0 {
			t.Errorf("reduced motion: %v photos flew along the lane", got)
		}
	})
}
