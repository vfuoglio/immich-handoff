package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// The footage for the product film (tools/video/STORYBOARD.md): real scenes on the demo (demo/), filmed in Chrome at the
// size of the film, with a visible cursor that glides, and a ripple where it clicks. Chrome's screencast does not draw a
// cursor, so a small script puts one on the page that follows the mouse events the scenes send.
//
//	DEMO_URL=http://localhost:2295 go run . video <scene> <outdir>      scene "list" names them

// cursorJS draws the cursor, a ring where a press happens, and (for a phone) a round fingertip.
const cursorJS = `(() => {
  if (window.__cursor) return; window.__cursor = true;
  const make = () => {
    if (!document.body) return requestAnimationFrame(make);
    const css = document.createElement('style');
    css.textContent = '#fx-cur{position:fixed;left:0;top:0;z-index:2147483647;pointer-events:none;width:30px;height:30px;transform:translate(-100px,-100px);filter:drop-shadow(0 3px 5px rgb(0 0 0 / .45))}' +
      '#fx-cur.touch{width:46px;height:46px;margin:-23px 0 0 -23px;border-radius:50%;background:rgb(255 255 255 / .55);border:2px solid rgb(255 255 255 / .9);filter:none}' +
      '.fx-rip{position:fixed;z-index:2147483646;pointer-events:none;width:14px;height:14px;margin:-7px 0 0 -7px;border-radius:50%;border:3px solid #e1409a;animation:fxrip .55s ease-out forwards}' +
      '@keyframes fxrip{to{transform:scale(5);opacity:0}}';
    document.head.append(css);
    const c = document.createElement('div'); c.id = 'fx-cur';
    c.innerHTML = '<svg viewBox="0 0 24 24" width="30" height="30"><path d="M3 2l17 8-7 2-3 7z" fill="#fff" stroke="#1b1b2e" stroke-width="1.6" stroke-linejoin="round"/></svg>';
    if (window.__touch) { c.className = 'touch'; c.innerHTML = ''; }
    document.body.append(c);
    addEventListener('mousemove', e => { c.style.transform = 'translate(' + e.clientX + 'px,' + e.clientY + 'px)'; }, true);
    addEventListener('mousedown', e => { const r = document.createElement('div'); r.className = 'fx-rip'; r.style.left = e.clientX + 'px'; r.style.top = e.clientY + 'px'; document.body.append(r); setTimeout(() => r.remove(), 700); }, true);
  };
  make();
})()`

type vframe struct {
	name string
	at   time.Time
}

type clip struct {
	ctx       context.Context
	dir       string
	w, h      int64
	mu        sync.Mutex
	frames    []vframe
	n         int
	x, y      float64
	base      string
	cancelAll context.CancelFunc
}

func demoBase() string { return strings.TrimSuffix(env("DEMO_URL", "http://localhost:2295"), "/") }

// newClip opens Chrome at the film's size. mobile makes a phone (a fingertip instead of an arrow).
func newClip(dir string, w, h int64, dsf float64, mobile, dark bool, who string) *clip {
	os.RemoveAll(dir)
	os.MkdirAll(dir, 0o755)
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("no-sandbox", true), chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("disable-gpu", true), chromedp.Flag("blink-settings", "primaryHoverType=2,availableHoverTypes=2,primaryPointerType=4,availablePointerTypes=4"))
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(actx)
	ctx, cancelT := context.WithTimeout(ctx, 6*time.Minute)
	c := &clip{ctx: ctx, dir: dir, w: int64(float64(w) * dsf), h: int64(float64(h) * dsf), base: demoBase(), cancelAll: func() { cancelT(); cancel(); cancelA() }}
	scheme := "light"
	if dark {
		scheme = "dark"
	}
	tasks := chromedp.Tasks{
		emulation.SetDeviceMetricsOverride(w, h, dsf, mobile),
		inEnglish(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: scheme}}).Do(ctx)
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			js := cursorJS
			if mobile {
				js = "window.__touch = true;" + js
			}
			_, err := page.AddScriptToEvaluateOnNewDocument(js).Do(ctx)
			return err
		}),
	}
	if who != "" {
		u, _ := url.Parse(c.base)
		for _, ck := range demoCookies(who) {
			ck := ck
			tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
				return network.SetCookie(ck.Name, ck.Value).WithDomain(u.Hostname()).WithPath("/").WithHTTPOnly(ck.HttpOnly).Do(ctx)
			}))
		}
	}
	if err := chromedp.Run(ctx, tasks...); err != nil {
		log.Fatal(err)
	}
	return c
}

// demoCookies signs in to the demo's Immich as one of its people.
func demoCookies(who string) []*http.Cookie {
	body, _ := json.Marshal(map[string]string{"email": who + "@demo.test", "password": "Demo-pass-1"})
	res, err := http.Post(demoBase()+"/api/auth/login", "application/json", strings.NewReader(string(body)))
	if err != nil {
		log.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if len(res.Cookies()) == 0 {
		log.Fatalf("signing in as %s set no cookies: is the demo running and seeded?", who)
	}
	return res.Cookies()
}

func (c *clip) must(what string, a ...chromedp.Action) {
	if err := chromedp.Run(c.ctx, a...); err != nil {
		log.Fatalf("%s: %v", what, err)
	}
}

// open goes to a page and waits for it.
func (c *clip) open(path string) {
	c.must("open "+path, chromedp.Navigate(c.base+path), chromedp.WaitReady("body"))
}

func (c *clip) wait(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

// until waits for a condition in the page; the page may reload while it waits.
func (c *clip) until(what, expr string, secs int) {
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		if err := chromedp.Run(c.ctx, chromedp.Evaluate(expr, &ok)); err == nil && ok {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	log.Fatalf("timed out waiting for %s (%s)", what, expr)
}

func (c *clip) eval(expr string) { c.must("eval", chromedp.Evaluate(expr, nil)) }

// startFilming records everything that happens from here until stop.
func (c *clip) startFilming() {
	chromedp.ListenTarget(c.ctx, func(ev interface{}) {
		e, ok := ev.(*page.EventScreencastFrame)
		if !ok {
			return
		}
		go chromedp.Run(c.ctx, page.ScreencastFrameAck(e.SessionID))
		var data []byte
		switch v := any(e.Data).(type) {
		case string:
			data, _ = base64.StdEncoding.DecodeString(v)
		case []byte:
			data = v
		}
		c.mu.Lock()
		c.n++
		name := fmt.Sprintf("frame-%05d.jpg", c.n)
		c.mu.Unlock()
		if err := os.WriteFile(filepath.Join(c.dir, name), data, 0o644); err != nil {
			return
		}
		c.mu.Lock()
		c.frames = append(c.frames, vframe{name, e.Metadata.Timestamp.Time()})
		c.mu.Unlock()
	})
	c.must("start filming", page.StartScreencast().WithFormat(page.ScreencastFormatJpeg).WithQuality(92).WithMaxWidth(c.w).WithMaxHeight(c.h).WithEveryNthFrame(1))
}

// stop ends the filming and writes frames.txt (each frame with how long it stays), which tools/video/clip.sh turns into a video.
func (c *clip) stop(name string) {
	chromedp.Run(c.ctx, page.StopScreencast())
	time.Sleep(600 * time.Millisecond)
	c.mu.Lock()
	frames := append([]vframe(nil), c.frames...)
	c.mu.Unlock()
	if len(frames) < 2 {
		log.Fatalf("%s: only %d frames", name, len(frames))
	}
	var list strings.Builder
	for i, f := range frames {
		d := 0.1
		if i+1 < len(frames) {
			d = frames[i+1].at.Sub(f.at).Seconds()
		}
		fmt.Fprintf(&list, "file '%s'\nduration %.4f\n", f.name, math.Max(d, 0.001))
	}
	fmt.Fprintf(&list, "file '%s'\n", frames[len(frames)-1].name)
	os.WriteFile(filepath.Join(c.dir, "frames.txt"), []byte(list.String()), 0o644)
	span := frames[len(frames)-1].at.Sub(frames[0].at).Seconds()
	fmt.Printf("%s: %d frames in %.1fs\n", name, len(frames), span)
	c.cancelAll()
}

// glide moves the pointer to a point over ms milliseconds, easing in and out like a hand.
func (c *clip) glide(x, y float64, ms int) {
	x0, y0 := c.x, c.y
	n := ms / 16
	if n < 1 {
		n = 1
	}
	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n)
		t = t * t * (3 - 2*t)
		chromedp.Run(c.ctx, input.DispatchMouseEvent(input.MouseMoved, x0+(x-x0)*t, y0+(y-y0)*t))
		time.Sleep(16 * time.Millisecond)
	}
	c.x, c.y = x, y
}

// mark finds an element with a piece of JavaScript (an expression that returns it) and gives back a selector for it.
func (c *clip) mark(find string) string {
	c.eval("document.querySelectorAll('[data-fx]').forEach(e => e.removeAttribute('data-fx')); { const e = " + find + "; if (e) e.setAttribute('data-fx', '1'); }")
	return `[data-fx="1"]`
}

// centre scrolls an element smoothly into the middle of the view, waits for the scrolling to stop, and says where it is.
func (c *clip) centre(sel string) (float64, float64) {
	var at []float64
	c.must("scroll to "+sel, chromedp.Evaluate(fmt.Sprintf(`(() => { const e = document.querySelector(%q); if (!e) return false; const r = e.getBoundingClientRect(); if (r.top < 120 || r.bottom > innerHeight - 120) e.scrollIntoView({block: 'center', behavior: 'smooth'}); return true; })()`, sel), nil))
	last := -1.0
	for i := 0; i < 40; i++ {
		c.must("find "+sel, chromedp.Evaluate(fmt.Sprintf(`(() => { const e = document.querySelector(%q); if (!e) return null; const r = e.getBoundingClientRect(); return [r.x + r.width / 2, r.y + r.height / 2]; })()`, sel), &at))
		if len(at) < 2 {
			log.Fatalf("no element %s", sel)
		}
		if at[1] == last {
			break
		}
		last = at[1]
		time.Sleep(110 * time.Millisecond)
	}
	return at[0], at[1]
}

// pointAt glides to an element.
func (c *clip) pointAt(sel string, ms int) {
	x, y := c.centre(sel)
	c.glide(x, y, ms)
}

func (c *clip) click() {
	chromedp.Run(c.ctx, input.DispatchMouseEvent(input.MousePressed, c.x, c.y).WithButton(input.Left).WithClickCount(1))
	time.Sleep(70 * time.Millisecond)
	chromedp.Run(c.ctx, input.DispatchMouseEvent(input.MouseReleased, c.x, c.y).WithButton(input.Left).WithClickCount(1))
}

func (c *clip) clickOn(sel string, ms int) {
	c.pointAt(sel, ms)
	c.wait(180)
	c.click()
}

// typeInto types text into a field a letter at a time.
func (c *clip) typeInto(sel, text string, gapMs int) {
	c.must("focus "+sel, chromedp.Focus(sel, chromedp.ByQuery))
	for _, r := range text {
		// a key event with its character is quick; chromedp.SendKeys takes about half a second a letter
		chromedp.Run(c.ctx, input.DispatchKeyEvent(input.KeyChar).WithText(string(r)))
		time.Sleep(time.Duration(gapMs) * time.Millisecond)
	}
}

// appRequest makes a request to the app as a signed-in person, as a page would.
func appRequest(who, method, path string, form url.Values) (int, string) {
	var rd io.Reader
	if form != nil {
		rd = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, demoBase()+"/handoff"+path, rd)
	var cookie []string
	for _, c := range demoCookies(who) {
		cookie = append(cookie, c.Name+"="+c.Value)
	}
	req.Header.Set("Cookie", strings.Join(cookie, "; "))
	req.Header.Set("X-Requested-With", "immich-handoff")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

type videoScene func(dir string)

func videoScenes() map[string]videoScene {
	return map[string]videoScene{
		"inside":   sceneInside,
		"host":     sceneHost,
		"guest":    sceneGuest,
		"arrive":   sceneArrive,
		"wall":     sceneWall,
		"rule":     sceneRule,
		"ruleprep": ruleprep,
		"client":   sceneClient,
	}
}

func videoMain(args []string) {
	if len(args) >= 4 && args[0] == "render" {
		seconds, fps := 5.0, 30
		fmt.Sscan(args[3], &seconds)
		if len(args) > 4 {
			fmt.Sscan(args[4], &fps)
		}
		renderFrames(args[1], args[2], seconds, fps)
		return
	}
	if len(args) >= 3 && args[0] == "stills" {
		renderStills(args[1], args[2])
		return
	}
	if len(args) < 1 {
		log.Fatal("usage: video <scene|list> <outdir>")
	}
	scenes := videoScenes()
	if args[0] == "list" {
		for n := range scenes {
			fmt.Println(n)
		}
		return
	}
	s, ok := scenes[args[0]]
	if !ok || len(args) < 2 {
		log.Fatalf("usage: video <scene> <outdir> (unknown scene %q?)", args[0])
	}
	s(args[1])
}
