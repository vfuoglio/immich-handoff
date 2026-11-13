package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// A scene is a short thing to film: who is looking, how big the window is, what they do and when. film records it
// with Chrome's screencast (a frame whenever something on the page changes, with its time), so transitions, hovers
// and the live scene come out as they really move. gif.sh then cuts the frames into a GIF.
type scene struct {
	name, who, url string
	w, h           int64
	mobile, dark   bool
	secs           float64
	clip           string // an XPath: only this element is kept (the crop is written to crop.txt)
	pad            float64
	steps          []step
	prep           func() (string, []step) // sets the scene up and says where to look (and what to do while filming)
}

type step struct {
	at  float64 // seconds after the filming starts
	run func(ctx context.Context)
}

func scenes() map[string]scene {
	return map[string]scene{
		// the live scene of Bob's Holiday 2026 while two senders' photos are handed over (light)
		"live-scene": {name: "live-scene", who: "bob@test.local", url: appURL, w: 1280, h: 900, secs: 36, clip: `//section[@id='scene']`, pad: 12,
			steps: []step{{7, fire}}},
		// Alice's whole Overview on a desktop, in the dark, while her photos go: the numbers roll, the light flares
		"arrival-dark": {name: "arrival-dark", who: "alice@test.local", url: appURL, w: 1280, h: 860, dark: true, secs: 30,
			steps: []step{{4, fire}}},
		// the command palette, in the dark: open it, type, move down, choose
		"palette-dark": {name: "palette-dark", who: "bob@test.local", url: appURL, w: 1280, h: 720, dark: true, secs: 9,
			steps: []step{{1.2, click("#palette")}, {2.4, typeText("#cmd-q", "ser", 280*time.Millisecond)}, {4.2, key("ArrowDown")}, {5.4, key("Enter")}}},
		// the two buttons in the bar: light, dark, light; then the colour scheme menu and the other scheme, in light and dark
		"theme-switch": {name: "theme-switch", who: "bob@test.local", url: appURL, w: 1280, h: 720, secs: 15,
			steps: []step{{1.2, click("#theme")}, {3, click("#theme")}, {4.8, click("details.scheme > summary")}, {6, click(`button[data-palette="immich"]`)},
				{8, click("#theme")}, {10, click("details.scheme > summary")}, {11.2, click(`button[data-palette="twilight"]`)}}},
		// tabs, and a service row that opens in place, with the cards tilting toward the pointer (light)
		"services-light": {name: "services-light", who: "bob@test.local", url: appURL, w: 1280, h: 760, secs: 10,
			steps: []step{{1, click(`nav.tabs a[data-view="services"]`)}, {2.4, click("#v-services details.svc > summary")},
				{4, moveMouse(300, 520, 900, 440, 1400*time.Millisecond)}, {5.8, moveMouse(900, 440, 420, 600, 1400*time.Millisecond)},
				{7.8, click(`nav.tabs a[data-view="activity"]`)}}},
		// an invitation: the link, a ready-written message, and a QR code a phone can scan (light)
		"invite-qr": {name: "invite-qr", who: "bob@test.local", url: appURL + "#services", w: 1000, h: 660, secs: 9,
			steps: []step{{0.8, js(`const r=[...document.querySelectorAll('#v-services details.svc')].find(d=>d.querySelector('input.invite')); r.open=true`)},
				{1.8, js(`document.querySelector('input.invite').scrollIntoView({block:'center', behavior:'smooth'})`)},
				{3.2, click("button.show-qr")}, {5.6, click("button.copy-message")}}},
		// the wall on a screen, in the dark: a photo arrives, then the styles and the layouts are tried
		"wall": {name: "wall", w: 1280, h: 720, dark: true, secs: 36, prep: wallScene},
		// the page on a phone, in the dark, while photos are handed over
		"phone-dark": {name: "phone-dark", who: "alice@test.local", url: appURL, w: 390, h: 844, mobile: true, dark: true, secs: 36,
			steps: []step{{0.2, js(`window.scrollTo(0, 400)`)}, {3, fire}}},
	}
}

func click(sel string) func(context.Context) {
	return func(ctx context.Context) {
		if err := chromedp.Run(ctx, chromedp.Click(sel, chromedp.ByQuery)); err != nil {
			log.Printf("click %s: %v", sel, err)
		}
	}
}

func js(expr string) func(context.Context) {
	return func(ctx context.Context) {
		if err := chromedp.Run(ctx, chromedp.Evaluate("(function(){"+expr+"})()", nil)); err != nil {
			log.Printf("js: %v", err)
		}
	}
}

// key presses a key the way the keyboard does but without the character that follows, so a handler that closes a dialog
// on Enter is not followed by a stray keypress on the button that gets the focus back.
func key(k string) func(context.Context) {
	codes := map[string]int64{"ArrowDown": 40, "Enter": 13}
	return func(ctx context.Context) {
		err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			for _, typ := range []input.KeyType{input.KeyRawDown, input.KeyUp} {
				if err := input.DispatchKeyEvent(typ).WithKey(k).WithCode(k).WithWindowsVirtualKeyCode(codes[k]).Do(ctx); err != nil {
					return err
				}
			}
			return nil
		}))
		if err != nil {
			log.Printf("key %s: %v", k, err)
		}
	}
}

func typeText(sel, text string, gap time.Duration) func(context.Context) {
	return func(ctx context.Context) {
		for _, r := range text {
			if err := chromedp.Run(ctx, chromedp.SendKeys(sel, string(r), chromedp.ByQuery)); err != nil {
				log.Printf("type %q into %s: %v", string(r), sel, err)
			}
			time.Sleep(gap)
		}
	}
}

func moveMouse(x1, y1, x2, y2 float64, d time.Duration) func(context.Context) {
	return func(ctx context.Context) {
		const every = 16 * time.Millisecond
		n := int(d / every)
		for i := 0; i <= n; i++ {
			t := float64(i) / float64(n)
			t = t * t * (3 - 2*t) // eases in and out, as a hand does
			chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
				return input.DispatchMouseEvent(input.MouseMoved, x1+(x2-x1)*t, y1+(y2-y1)*t).Do(ctx)
			}))
			time.Sleep(every)
		}
	}
}

// fire tags the photos that seed.sh prepare left, with the app running for real
func fire(ctx context.Context) {
	cmd := exec.Command("bash", "seed.sh", "fire")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("fire: %v %s", err, out)
	}
}

func film(name, dir string) {
	sc, ok := scenes()[name]
	if !ok {
		var names []string
		for n := range scenes() {
			names = append(names, n)
		}
		log.Fatalf("unknown scene %q; scenes: %s", name, strings.Join(names, ", "))
	}
	if sc.prep != nil {
		var more []step
		sc.url, more = sc.prep()
		sc.steps = append(sc.steps, more...)
	}
	os.RemoveAll(dir)
	os.MkdirAll(dir, 0o755)
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("no-sandbox", true), chromedp.Flag("hide-scrollbars", true), chromedp.Flag("disable-gpu", true))
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelA()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()
	ctx, cancelT := context.WithTimeout(ctx, time.Duration(sc.secs+120)*time.Second)
	defer cancelT()

	scheme := "light"
	if sc.dark {
		scheme = "dark"
	}
	tasks := chromedp.Tasks{
		emulation.SetDeviceMetricsOverride(sc.w, sc.h, 1, sc.mobile),
		inEnglish(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: scheme}}).Do(ctx)
		}),
		chromedp.ActionFunc(func(ctx context.Context) error { // the copy button needs the clipboard
			return browser.GrantPermissions([]browser.PermissionType{browser.PermissionTypeClipboardReadWrite, browser.PermissionTypeClipboardSanitizedWrite}).Do(ctx)
		}),
	}
	if sc.who != "" { // the wall needs no sign-in
		for _, c := range loginCookies(sc.who) {
			c := c
			tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
				return network.SetCookie(c.Name, c.Value).WithDomain(c.Domain).WithPath(c.Path).WithHTTPOnly(c.HTTPOnly).Do(ctx)
			}))
		}
	}
	tasks = append(tasks, chromedp.Navigate(sc.url), chromedp.WaitReady("body"),
		chromedp.Poll(settled, nil, chromedp.WithPollingTimeout(30*time.Second)), chromedp.Sleep(2500*time.Millisecond))
	if err := chromedp.Run(ctx, tasks); err != nil {
		log.Fatal(err)
	}

	if sc.clip != "" { // where the element is, once the page has settled, kept for the crop
		var r struct{ X, Y, W, H float64 }
		js := fmt.Sprintf(`(function(){const n=document.evaluate(%q,document,null,9,null).singleNodeValue; if(!n) return {X:0,Y:0,W:0,H:0};
			n.scrollIntoView({block:'center'}); const b=n.getBoundingClientRect(); return {X:b.left,Y:b.top,W:b.width,H:b.height}})()`, sc.clip)
		if err := chromedp.Run(ctx, chromedp.Evaluate(js, &r)); err != nil || r.W == 0 {
			log.Fatalf("%s: the element is not on the page: %v", name, err)
		}
		note := fmt.Sprintf("%d:%d:%d:%d", int(max(r.W+2*sc.pad, 2))/2*2, int(max(r.H+2*sc.pad, 2))/2*2, int(max(r.X-sc.pad, 0)), int(max(r.Y-sc.pad, 0)))
		os.WriteFile(filepath.Join(dir, "crop.txt"), []byte(note), 0o644)
	}

	type frame struct {
		name string
		at   time.Time
	}
	var frames []frame
	frameCh := make(chan frame, 4096)
	var seq atomic.Int64
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		e, ok := ev.(*page.EventScreencastFrame)
		if !ok {
			return
		}
		go chromedp.Run(ctx, page.ScreencastFrameAck(e.SessionID))
		var data []byte
		switch v := any(e.Data).(type) {
		case string:
			data, _ = base64.StdEncoding.DecodeString(v)
		case []byte:
			data = v
		}
		n := fmt.Sprintf("frame-%05d.jpg", seq.Add(1))
		if err := os.WriteFile(filepath.Join(dir, n), data, 0o644); err != nil {
			log.Print(err)
			return
		}
		frameCh <- frame{n, e.Metadata.Timestamp.Time()}
	})
	if err := chromedp.Run(ctx, page.StartScreencast().WithFormat(page.ScreencastFormatJpeg).WithQuality(88).
		WithMaxWidth(sc.w).WithMaxHeight(sc.h).WithEveryNthFrame(1)); err != nil {
		log.Fatal(err)
	}
	start := time.Now()
	for _, st := range sc.steps {
		st := st
		go func() {
			time.Sleep(time.Until(start.Add(time.Duration(st.at * float64(time.Second)))))
			st.run(ctx)
		}()
	}
	time.Sleep(time.Duration(sc.secs * float64(time.Second)))
	chromedp.Run(ctx, page.StopScreencast())
	time.Sleep(500 * time.Millisecond)
	close(frameCh)
	for f := range frameCh {
		frames = append(frames, f)
	}
	if len(frames) < 2 {
		log.Fatalf("%s: only %d frames", name, len(frames))
	}
	var list strings.Builder
	for i, f := range frames {
		d := 0.1
		if i+1 < len(frames) {
			d = frames[i+1].at.Sub(f.at).Seconds()
		}
		fmt.Fprintf(&list, "file '%s'\nduration %.3f\n", f.name, max(d, 0.001))
	}
	fmt.Fprintf(&list, "file '%s'\n", frames[len(frames)-1].name)
	os.WriteFile(filepath.Join(dir, "frames.txt"), []byte(list.String()), 0o644)
	span := frames[len(frames)-1].at.Sub(frames[0].at).Seconds()
	fmt.Printf("%s: %d frames in %.1fs (%.0f per second)\n", name, len(frames), span, float64(len(frames))/span)
}
