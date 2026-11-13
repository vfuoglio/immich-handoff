// Command screenshots makes the sample photos and the images used in the README and docs.
//
//	go run . gen   <dir> [count]   write sample photos (generated landscapes, no real people)
//	go run . shots <set> <outdir>  take screenshots of a running demo with headless Chrome
//	go run . hero  <outdir>        compose the README hero image from earlier shots
//	go run . film <scene> <framedir>  film a scene of the running demo as frames, for the README animations (see README.md)
//
// It lives in its own module so Chrome automation stays out of the app's dependencies.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

var (
	immichURL = env("IMMICH_URL", "http://localhost:2284")
	appURL    = env("APP_URL", "http://localhost:8080/")
	password  = env("DEMO_PASSWORD", "Passw0rd!x")
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func main() {
	if len(os.Args) < 3 {
		log.Fatal("usage: screenshots gen <dir> [count] | shots <set> <outdir> | hero <outdir>")
	}
	switch os.Args[1] {
	case "gen":
		n := 24
		if len(os.Args) > 3 {
			fmt.Sscan(os.Args[3], &n)
		}
		gen(os.Args[2], n)
	case "shots":
		if len(os.Args) < 4 {
			log.Fatal("shots needs a set name and an output directory")
		}
		shots(os.Args[2], os.Args[3])
	case "hero":
		hero(os.Args[2])
	case "film":
		if len(os.Args) < 4 {
			log.Fatal("film needs a scene name and a directory for the frames")
		}
		film(os.Args[2], os.Args[3])
	case "proxycheck":
		proxycheck(os.Args[2])
	default:
		log.Fatal("unknown command")
	}
}

// inEnglish makes the browser ask for English and choose it in the page's language menu, so every picture is in
// English whatever the machine's language is (the pictures are for the English documentation).
func inEnglish() chromedp.Action {
	return chromedp.Tasks{
		network.Enable(),
		network.SetExtraHTTPHeaders(network.Headers{"Accept-Language": "en"}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			u, err := url.Parse(appURL)
			if err != nil {
				return err
			}
			return network.SetCookie("handoff-lang", "en").WithDomain(u.Hostname()).WithPath("/").Do(ctx)
		}),
	}
}

// ---------------------------------------------------------------- sample photos

type palette struct {
	top, bottom, sun color.RGBA
	hills            [3]color.RGBA
}

var palettes = []palette{
	{rgb(255, 143, 94), rgb(255, 216, 150), rgb(255, 245, 210), [3]color.RGBA{rgb(150, 82, 110), rgb(98, 58, 100), rgb(52, 36, 78)}},
	{rgb(46, 74, 140), rgb(244, 168, 130), rgb(255, 238, 200), [3]color.RGBA{rgb(70, 70, 120), rgb(44, 48, 96), rgb(24, 28, 64)}},
	{rgb(88, 160, 220), rgb(214, 238, 250), rgb(255, 255, 240), [3]color.RGBA{rgb(96, 162, 118), rgb(52, 120, 92), rgb(28, 84, 70)}},
	{rgb(20, 28, 64), rgb(92, 76, 140), rgb(236, 234, 255), [3]color.RGBA{rgb(52, 52, 100), rgb(32, 34, 74), rgb(14, 16, 44)}},
	{rgb(240, 190, 90), rgb(252, 232, 170), rgb(255, 250, 225), [3]color.RGBA{rgb(190, 132, 70), rgb(130, 96, 60), rgb(80, 64, 50)}},
	{rgb(40, 130, 150), rgb(170, 226, 220), rgb(240, 255, 250), [3]color.RGBA{rgb(40, 110, 120), rgb(26, 84, 100), rgb(16, 56, 74)}},
}

func rgb(r, g, b uint8) color.RGBA { return color.RGBA{r, g, b, 255} }

func clamp(v int) uint8 { return uint8(max(0, min(255, v))) }

func mix(a, b color.RGBA, t float64) color.RGBA {
	t = math.Max(0, math.Min(1, t))
	return color.RGBA{uint8(float64(a.R)*(1-t) + float64(b.R)*t), uint8(float64(a.G)*(1-t) + float64(b.G)*t), uint8(float64(a.B)*(1-t) + float64(b.B)*t), 255}
}

// gen writes landscape-like pictures: a gradient sky, a glowing sun and three layers of hills.
func gen(dir string, n int) {
	os.MkdirAll(dir, 0o755)
	const w, h = 1600, 1066
	for i := 0; i < n; i++ {
		r := rand.New(rand.NewSource(int64(i)*7919 + 13))
		p := palettes[i%len(palettes)]
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		sx, sy := float64(w)*(0.2+0.6*r.Float64()), float64(h)*(0.28+0.2*r.Float64())
		sr := 50 + 40*r.Float64()
		var ridge [3][]float64
		for l := 0; l < 3; l++ {
			base := float64(h) * (0.58 + 0.12*float64(l))
			a1, a2 := 30+25*r.Float64(), 12+10*r.Float64()
			f1, f2 := 0.002+0.003*r.Float64(), 0.007+0.006*r.Float64()
			p1, p2 := r.Float64()*6.28, r.Float64()*6.28
			ridge[l] = make([]float64, w)
			for x := 0; x < w; x++ {
				ridge[l][x] = base + a1*math.Sin(float64(x)*f1+p1) + a2*math.Sin(float64(x)*f2+p2)
			}
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				c := mix(p.top, p.bottom, float64(y)/float64(h)*1.3)
				d := math.Hypot(float64(x)-sx, float64(y)-sy)
				if d < sr {
					c = p.sun
				} else if d < sr*4 {
					c = mix(c, p.sun, 0.55*math.Pow(1-(d-sr)/(sr*3), 2))
				}
				for l := 0; l < 3; l++ {
					if float64(y) >= ridge[l][x] {
						c = mix(p.hills[l], p.hills[2], float64(y-int(ridge[l][x]))/float64(h)*0.9)
					}
				}
				g := r.Intn(7) - 3 // a little grain so it reads as a photo
				c.R, c.G, c.B = clamp(int(c.R)+g), clamp(int(c.G)+g), clamp(int(c.B)+g)
				img.SetRGBA(x, y, c)
			}
		}
		f, err := os.Create(filepath.Join(dir, fmt.Sprintf("IMG_%04d.jpg", 2040+i)))
		if err != nil {
			log.Fatal(err)
		}
		jpeg.Encode(f, img, &jpeg.Options{Quality: 86})
		f.Close()
	}
	fmt.Println("wrote", n, "photos to", dir)
}

// ---------------------------------------------------------------- browser

type shot struct {
	name   string
	who    string // email to sign in as; empty: not signed in
	url    string
	width  int
	height int
	dark   bool
	mobile bool
	fold   bool   // only what shows without scrolling (the window's size), not the whole page
	xpath  string // clip to this element instead of the full page
	pad    int    // extra pixels around a clipped element
	wait   string // JS condition that must become true
	js     string // JS to run once the page has settled (for example to open a section)
	pause  time.Duration
}

// loginCookies signs in to Immich like a browser would and returns the cookies it sets.
func loginCookies(email string) []*network.CookieParam {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	res, err := http.Post(immichURL+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	var out []*network.CookieParam
	for _, c := range res.Cookies() {
		out = append(out, &network.CookieParam{Name: c.Name, Value: c.Value, Domain: "localhost", Path: "/", HTTPOnly: c.HttpOnly})
	}
	if len(out) == 0 {
		log.Fatalf("login as %s set no cookies", email)
	}
	return out
}

const settled = `document.readyState==='complete' && document.querySelectorAll('.skeleton').length===0`

func take(s shot, outdir string) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("no-sandbox", true), chromedp.Flag("hide-scrollbars", true), chromedp.Flag("disable-gpu", true))
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelA()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()
	ctx, cancelT := context.WithTimeout(ctx, 90*time.Second)
	defer cancelT()

	scale := 1.5
	if s.height == 0 {
		s.height = 900
	}
	var buf []byte
	tasks := chromedp.Tasks{
		emulation.SetDeviceMetricsOverride(int64(s.width), int64(s.height), scale, s.mobile),
		inEnglish(),
	}
	scheme := "light"
	if s.dark {
		scheme = "dark"
	}
	tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
		return emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: scheme}}).Do(ctx)
	}))
	if s.who != "" {
		for _, c := range loginCookies(s.who) {
			c := c
			tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
				return network.SetCookie(c.Name, c.Value).WithDomain(c.Domain).WithPath(c.Path).WithHTTPOnly(c.HTTPOnly).Do(ctx)
			}))
		}
	}
	wait := s.wait
	if wait == "" {
		wait = settled
	}
	tasks = append(tasks,
		chromedp.Navigate(s.url),
		chromedp.WaitReady("body"),
		chromedp.Poll(wait, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Sleep(max(s.pause, 600*time.Millisecond)),
	)
	if s.js != "" {
		tasks = append(tasks, chromedp.Evaluate(s.js, nil), chromedp.Sleep(400*time.Millisecond))
	}
	if s.xpath != "" {
		tasks = append(tasks, screenshotElement(s.xpath, s.pad, &buf))
	} else if s.fold {
		tasks = append(tasks, chromedp.CaptureScreenshot(&buf))
	} else {
		tasks = append(tasks, chromedp.FullScreenshot(&buf, 100))
	}
	if err := chromedp.Run(ctx, tasks); err != nil {
		log.Fatalf("%s: %v", s.name, err)
	}
	path := filepath.Join(outdir, s.name+".png")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%-28s %6d KB\n", s.name+".png", len(buf)/1024)
}

// screenshotElement captures one element (found by XPath) with some padding around it.
func screenshotElement(xpath string, pad int, out *[]byte) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		var r struct{ X, Y, W, H float64 }
		js := fmt.Sprintf(`(function(){const n=document.evaluate(%q,document,null,9,null).singleNodeValue;
			if(!n) return {X:0,Y:0,W:0,H:0}; n.scrollIntoView({block:'center'}); const b=n.getBoundingClientRect();
			return {X:b.left+scrollX,Y:b.top+scrollY,W:b.width,H:b.height}})()`, xpath)
		if err := chromedp.Evaluate(js, &r).Do(ctx); err != nil {
			return err
		}
		if r.W == 0 {
			return fmt.Errorf("element not found: %s", xpath)
		}
		p := float64(pad)
		data, err := page.CaptureScreenshot().WithCaptureBeyondViewport(true).WithClip(&page.Viewport{
			X: max(r.X-p, 0), Y: max(r.Y-p, 0), Width: r.W + 2*p, Height: r.H + 2*p, Scale: 1,
		}).Do(ctx)
		*out = data
		return err
	})
}

// ---------------------------------------------------------------- shot sets

func shots(set, outdir string) {
	os.MkdirAll(outdir, 0o755)
	const desktop = 1280
	const live = 2500 * time.Millisecond // the numbers roll and the scene settles
	openFirstRow := `document.querySelector('#v-services details.svc').open = true`
	switch set {
	case "sender": // Alice with photos waiting to be handed over
		take(shot{name: "dashboard-sender", who: "alice@test.local", url: appURL, width: desktop, height: 900, fold: true, pause: live}, outdir)
		take(shot{name: "dashboard-sender-dark", who: "alice@test.local", url: appURL, width: desktop, height: 900, fold: true, dark: true, pause: live}, outdir)
		take(shot{name: "dashboard-mobile", who: "alice@test.local", url: appURL, width: 390, height: 844, mobile: true, fold: true, pause: live}, outdir)
		take(shot{name: "dashboard-mobile-dark", who: "alice@test.local", url: appURL, width: 390, height: 844, mobile: true, fold: true, dark: true, pause: live}, outdir)
		take(shot{name: "live-scene", who: "alice@test.local", url: appURL, width: desktop, height: 900, pause: live,
			xpath: `//section[@id='scene']`, pad: 12}, outdir)
		take(shot{name: "services-sender", who: "alice@test.local", url: appURL + "#services", width: desktop, height: 900, fold: true, pause: live, js: openFirstRow}, outdir)
	case "owner":
		take(shot{name: "dashboard-owner", who: "bob@test.local", url: appURL, width: desktop, height: 900, fold: true, pause: live}, outdir)
		take(shot{name: "owner-service", who: "bob@test.local", url: appURL + "#services", width: desktop, height: 900,
			js: openFirstRow, xpath: `//section[@id='v-services']//details[contains(@class,'svc')][1]`, pad: 16}, outdir)
		take(shot{name: "offer-form", who: "bob@test.local", url: appURL + "#services", width: desktop, height: 900,
			js:    `document.querySelector('details.offer').open = true`,
			xpath: `//details[contains(@class,'offer')]`, pad: 16}, outdir)
		take(shot{name: "join-choice", who: "dave@test.local", url: appURL + "#services", width: desktop, height: 900,
			xpath: `(//section[@id='v-services']//article[contains(@class,'card')])[1]`, pad: 16}, outdir)
		take(shot{name: "activity", who: "bob@test.local", url: appURL + "#activity", width: desktop, height: 900, fold: true, pause: live}, outdir)
		take(shot{name: "palette", who: "bob@test.local", url: appURL, width: desktop, height: 900, fold: true, pause: live,
			js: `document.dispatchEvent(new KeyboardEvent('keydown', {key: 'k', ctrlKey: true, bubbles: true})); window.dispatchEvent(new KeyboardEvent('keydown', {key: 'k', ctrlKey: true, bubbles: true}))`}, outdir)
	case "audit": // every page that is not in the README, in both colour schemes and on a phone, to look at (not for the documentation)
		openAll := `document.querySelectorAll('main details').forEach(d => d.open = true)`
		scheme := func(name string) string {
			return fmt.Sprintf(`localStorage.setItem('handoff-palette','%[1]s'); document.documentElement.dataset.palette='%[1]s'`, name)
		}
		for _, v := range []struct {
			name, palette string
			dark, mobile  bool
			width         int
		}{{"twilight-dark", "twilight", true, false, desktop}, {"immich-light", "immich", false, false, desktop}, {"immich-dark-phone", "immich", true, true, 390}} {
			take(shot{name: "audit-services-" + v.name, who: "bob@test.local", url: appURL + "#services", width: v.width, height: 900, dark: v.dark, mobile: v.mobile, pause: live, js: scheme(v.palette) + ";" + openAll}, outdir)
			take(shot{name: "audit-activity-" + v.name, who: "bob@test.local", url: appURL + "#activity", width: v.width, height: 900, dark: v.dark, mobile: v.mobile, pause: live, js: scheme(v.palette)}, outdir)
			take(shot{name: "audit-admin-" + v.name, who: "admin@test.local", url: appURL + "admin", width: v.width, height: 900, dark: v.dark, mobile: v.mobile, pause: live, js: scheme(v.palette) + ";" + openAll}, outdir)
		}
	case "lost":
		take(shot{name: "connection-lost", who: "carol@test.local", url: appURL, width: desktop, height: 900,
			xpath: `//div[contains(@class,'needs')]/article[contains(@class,'need')][1]`, pad: 16}, outdir)
	case "immich":
		take(shot{name: "immich-family", who: "family@test.local", url: immichURL + "/photos", width: 1280, height: 660,
			wait:  `document.readyState==='complete' && document.querySelectorAll('img').length>3 && Array.from(document.images).every(i=>i.complete)`,
			pause: 2500 * time.Millisecond}, outdir)
	default:
		log.Fatal("unknown set " + set)
	}
}

// ---------------------------------------------------------------- hero

func dataURI(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b)
}

// hero lays the desktop page and the phone page over a soft gradient: the first picture of the README.
func hero(dir string) {
	page := fmt.Sprintf(`<!doctype html><meta charset=utf-8><style>
html,body{margin:0;width:1600px;height:860px;overflow:hidden;font-family:system-ui}
body{background:radial-gradient(1200px 700px at 15%% 10%%,#8b9bff 0,transparent 60%%),linear-gradient(135deg,#3b47a8,#5b6bd8 55%%,#9db9f5)}
.win{position:absolute;left:110px;top:96px;width:900px;height:667px;border-radius:18px;overflow:hidden;background:#fff;
  box-shadow:0 40px 90px rgba(10,14,60,.45),0 0 0 1px rgba(255,255,255,.35)}
.win .bar{height:34px;background:#eceef8;display:flex;align-items:center;gap:7px;padding-left:14px}
.win .bar i{width:11px;height:11px;border-radius:50%%;background:#c9cde6;display:block}
.win img{display:block;width:900px;margin-top:0}
.phone{position:absolute;left:1060px;top:60px;width:340px;height:740px;border-radius:46px;background:#0d0f1c;padding:12px;box-sizing:border-box;
  box-shadow:0 40px 90px rgba(10,14,60,.5),0 0 0 2px rgba(255,255,255,.25)}
.phone .screen{width:100%%;height:100%%;border-radius:36px;overflow:hidden;background:#000}
.phone img{display:block;width:100%%}
</style>
<div class=win><div class=bar><i></i><i></i><i></i></div><img src="%s"></div>
<div class=phone><div class=screen><img src="%s"></div></div>`,
		dataURI(filepath.Join(dir, "dashboard-owner.png")), dataURI(filepath.Join(dir, "dashboard-mobile-dark.png")))

	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("no-sandbox", true), chromedp.Flag("hide-scrollbars", true))
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelA()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()
	tmp := filepath.Join(os.TempDir(), "hero.html")
	os.WriteFile(tmp, []byte(page), 0o644)
	var buf []byte
	err := chromedp.Run(ctx,
		emulation.SetDeviceMetricsOverride(1600, 860, 1, false),
		chromedp.Navigate("file://"+tmp),
		chromedp.Sleep(800*time.Millisecond),
		chromedp.CaptureScreenshot(&buf),
	)
	if err != nil {
		log.Fatal(err)
	}
	out := filepath.Join(dir, "hero.png")
	os.WriteFile(out, buf, 0o644)
	fmt.Printf("hero.png %d KB\n", len(buf)/1024)
}

// proxycheck opens the page the way a reverse proxy would serve it (under a path prefix, on the same
// hostname as Immich) and checks that styling, live counts, thumbnails, the live stream, a button and
// the invitation link all work. Usage: proxycheck <page URL>, with IMMICH_URL pointing at the proxy too.
func proxycheck(page string) {
	failed := false
	check := func(name string, ok bool, detail string) {
		mark := "PASS"
		if !ok {
			mark, failed = "FAIL", true
		}
		fmt.Printf("%s  %-52s %s\n", mark, name, detail)
	}
	run := func(who string, fn func(ctx context.Context)) {
		opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("no-sandbox", true))
		actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
		defer cancelA()
		ctx, cancel := chromedp.NewContext(actx)
		defer cancel()
		ctx, cancelT := context.WithTimeout(ctx, 60*time.Second)
		defer cancelT()
		tasks := chromedp.Tasks{inEnglish()}
		for _, c := range loginCookies(who) {
			c := c
			tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
				return network.SetCookie(c.Name, c.Value).WithDomain(c.Domain).WithPath(c.Path).WithHTTPOnly(c.HTTPOnly).Do(ctx)
			}))
		}
		tasks = append(tasks, chromedp.Navigate(page), chromedp.WaitReady("body"),
			chromedp.Poll(settled, nil, chromedp.WithPollingTimeout(30*time.Second)), chromedp.Sleep(1500*time.Millisecond))
		if err := chromedp.Run(ctx, tasks); err != nil {
			check("open the page as "+who, false, err.Error())
			return
		}
		fn(ctx)
	}
	eval := func(ctx context.Context, js string, out any) {
		if err := chromedp.Run(ctx, chromedp.Evaluate(js, out)); err != nil {
			log.Fatal(err)
		}
	}

	run("alice@test.local", func(ctx context.Context) {
		var styled bool
		var thumbs, loaded int
		var live, toast string
		eval(ctx, `getComputedStyle(document.body).fontFamily.length>0 && getComputedStyle(document.body).backgroundColor!=='rgba(0, 0, 0, 0)'`, &styled)
		check("stylesheet is applied under the prefix", styled, "")
		eval(ctx, `document.querySelectorAll('.thumbs img').length`, &thumbs)
		eval(ctx, `Array.from(document.querySelectorAll('.thumbs img')).filter(i=>i.naturalWidth>0).length`, &loaded)
		check("counts load (htmx fragments through the prefix)", true, "")
		check("thumbnails load from Immich on the same host", thumbs > 0 && loaded == thumbs, fmt.Sprintf("%d of %d", loaded, thumbs))
		eval(ctx, `document.getElementById('live') ? document.getElementById('live').className : 'missing'`, &live)
		check("live updates connected (server-sent events)", !strings.Contains(live, "off"), "class="+live)
		if err := chromedp.Run(ctx, chromedp.Click(`button.primary[hx-post="sync"]`, chromedp.ByQuery), chromedp.Sleep(1500*time.Millisecond)); err != nil {
			check("press Sync now", false, err.Error())
		} else {
			eval(ctx, `document.getElementById('synctoast').textContent`, &toast)
			check("Sync now works through the prefix", strings.Contains(toast, "Sync started"), "toast="+toast)
		}
	})
	run("bob@test.local", func(ctx context.Context) {
		var link string
		eval(ctx, `(document.querySelector('input.invite')||{}).value||''`, &link)
		check("invitation link carries the prefix", strings.Contains(link, "/handoff/invite/"), link)
	})
	if failed {
		os.Exit(1)
	}
}
