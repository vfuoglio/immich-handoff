package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// insideShots takes the pictures of what Handoff adds to Immich's own pages: the Hand over... button in the photo viewer,
// its list of services, the message with Undo, and the selection bar. It needs the try-it-first demo running (demo/,
// everything on one address, http://localhost:2295) and signs in as Bob, who sends to Family album.
//
//	DEMO_URL=http://localhost:2295 go run . inside <outdir>
func insideShots(outdir string) {
	os.MkdirAll(outdir, 0o755)
	base := env("DEMO_URL", "http://localhost:2295")
	u, _ := url.Parse(base)
	body, _ := json.Marshal(map[string]string{"email": "bob@demo.test", "password": "Demo-pass-1"})
	res, err := http.Post(base+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	cookies := res.Cookies()
	if len(cookies) == 0 {
		log.Fatal("signing in as bob@demo.test set no cookies: is the demo running and seeded?")
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("no-sandbox", true), chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("disable-gpu", true), chromedp.Flag("blink-settings", "primaryHoverType=2,availableHoverTypes=2,primaryPointerType=4,availablePointerTypes=4"))
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelA()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()
	ctx, cancelT := context.WithTimeout(ctx, 3*time.Minute)
	defer cancelT()

	act := chromedp.Tasks{
		emulation.SetDeviceMetricsOverride(1280, 760, 1.5, false),
		inEnglish(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: "light"}}).Do(ctx)
		}),
	}
	for _, c := range cookies {
		c := c
		act = append(act, chromedp.ActionFunc(func(ctx context.Context) error {
			return network.SetCookie(c.Name, c.Value).WithDomain(u.Hostname()).WithPath("/").WithHTTPOnly(c.HttpOnly).Do(ctx)
		}))
	}
	if err := chromedp.Run(ctx, act); err != nil {
		log.Fatal(err)
	}
	shot := func(name string) {
		var buf []byte
		if err := chromedp.Run(ctx, chromedp.Sleep(700*time.Millisecond), chromedp.CaptureScreenshot(&buf)); err != nil {
			log.Fatalf("%s: %v", name, err)
		}
		os.WriteFile(filepath.Join(outdir, name+".png"), buf, 0o644)
		fmt.Printf("%-34s %5d KB\n", name+".png", len(buf)/1024)
	}
	must := func(what string, a ...chromedp.Action) {
		if err := chromedp.Run(ctx, a...); err != nil {
			log.Fatalf("%s: %v", what, err)
		}
	}
	wait := func(what, expr string) {
		must(what, chromedp.Poll(expr, nil, chromedp.WithPollingTimeout(40*time.Second)))
	}
	hover := func(sel string) {
		var at []float64
		must("find "+sel, chromedp.Evaluate(fmt.Sprintf(`(() => { const r = document.querySelector(%q).getBoundingClientRect(); return [r.x + r.width / 2, r.y + r.height / 2]; })()`, sel), &at))
		must("hover "+sel, input.DispatchMouseEvent(input.MouseMoved, at[0], at[1]))
		time.Sleep(500 * time.Millisecond)
	}

	// the grid, with photos selected: the bar and the button
	must("open the photos", chromedp.Navigate(base+"/photos"))
	wait("the photos", `document.querySelectorAll('[data-thumbnail-focus-container][data-asset]').length >= 4`)
	for i := 0; i < 3; i++ {
		var at []float64
		must("find tile", chromedp.Evaluate(fmt.Sprintf(`(() => { const r = document.querySelectorAll('[data-thumbnail-focus-container]')[%d].getBoundingClientRect(); return [r.x + r.width / 2, r.y + r.height / 2]; })()`, i), &at))
		must("hover tile", input.DispatchMouseEvent(input.MouseMoved, at[0], at[1]))
		time.Sleep(400 * time.Millisecond)
		must("tick tile", chromedp.Evaluate(fmt.Sprintf(`document.querySelectorAll('[data-thumbnail-focus-container]')[%d].querySelector('button[role=checkbox]').click()`, i), nil))
	}
	wait("the button in the selection bar", `document.getElementById('handoff-bar-act') !== null`)
	hover(`#handoff-bar-act`)
	shot("immich-button-selection")
	must("open the list", chromedp.Click(`#handoff-bar-act`, chromedp.ByQuery))
	wait("the list", `document.getElementById('handoff-menu') !== null`)
	shot("immich-button-selection-list")

	// the viewer: the button, the list, the message
	must("close the list", chromedp.KeyEvent("\x1b"))
	var id string
	tile := 3
	fmt.Sscan(env("INSIDE_TILE", "3"), &tile) // which photo of the grid the viewer shows
	must("a photo", chromedp.Evaluate(fmt.Sprintf(`document.querySelectorAll('[data-thumbnail-focus-container]')[%d].dataset.asset`, tile), &id))
	must("open the viewer", chromedp.Navigate(base+"/photos/"+id))
	wait("the button in the viewer", `document.getElementById('handoff-viewer-act') !== null`)
	time.Sleep(1500 * time.Millisecond)
	shot("immich-button-viewer")
	must("open the list", chromedp.Click(`#handoff-viewer-act`, chromedp.ByQuery))
	wait("the list", `document.getElementById('handoff-menu') !== null`)
	shot("immich-button-viewer-list")
	must("choose the service", chromedp.Click(`#handoff-menu [role=menuitem]`, chromedp.ByQuery))
	wait("the message", `document.getElementById('handoff-toast') && /handed over/.test(document.getElementById('handoff-toast').textContent)`)
	shot("immich-button-done")
}

// rulesShots takes the pictures of a rule at work: the suggestion with its photos on the Overview and the rule on the
// service's row. It needs the demo running, and a rule made as Bob for Family album that has found photos (see
// demo/README.md); it signs in as Bob.
//
//	DEMO_URL=http://localhost:2295 go run . rules <outdir>
func rulesShots(outdir string) {
	os.MkdirAll(outdir, 0o755)
	base := env("DEMO_URL", "http://localhost:2295")
	u, _ := url.Parse(base)
	body, _ := json.Marshal(map[string]string{"email": "bob@demo.test", "password": "Demo-pass-1"})
	res, err := http.Post(base+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("no-sandbox", true), chromedp.Flag("hide-scrollbars", true), chromedp.Flag("disable-gpu", true))
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelA()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()
	ctx, cancelT := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelT()
	act := chromedp.Tasks{emulation.SetDeviceMetricsOverride(1280, 900, 1.5, false), inEnglish()}
	for _, c := range res.Cookies() {
		c := c
		act = append(act, chromedp.ActionFunc(func(ctx context.Context) error {
			return network.SetCookie(c.Name, c.Value).WithDomain(u.Hostname()).WithPath("/").WithHTTPOnly(c.HttpOnly).Do(ctx)
		}))
	}
	var card, row []byte
	act = append(act,
		chromedp.Navigate(base+"/handoff/"),
		chromedp.Poll(`document.querySelector('.card.need') !== null`, nil, chromedp.WithPollingTimeout(40*time.Second)),
		chromedp.Sleep(2500*time.Millisecond),
		screenshotElement(`//*[contains(@class,'need')][1]`, 24, &card),
		chromedp.Navigate(base+"/handoff/#services"),
		chromedp.Poll(`document.querySelector('#v-services details.svc') !== null`, nil, chromedp.WithPollingTimeout(20*time.Second)),
		chromedp.Evaluate(`document.querySelector('#v-services details.svc').open = true; [...document.querySelectorAll('#v-services details.svc')].forEach(d => { if (d.textContent.includes('Family album')) d.open = true })`, nil),
		chromedp.Evaluate(`document.querySelector('header.bar').style.visibility = 'hidden'`, nil),
		chromedp.Sleep(1200*time.Millisecond),
		screenshotElement(`//details[contains(@class,'svc')][contains(.,'Family album')]`, 16, &row),
	)
	if err := chromedp.Run(ctx, act); err != nil {
		log.Fatal(err)
	}
	os.WriteFile(filepath.Join(outdir, "rules-suggestion.png"), card, 0o644)
	os.WriteFile(filepath.Join(outdir, "rules-row.png"), row, 0o644)
	fmt.Printf("rules-suggestion.png %d KB, rules-row.png %d KB\n", len(card)/1024, len(row)/1024)
}
