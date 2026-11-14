package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// sendShots takes the pictures of the Send page on a phone: the page with photos chosen, the queue while they go, and the card
// that offers to install the app. It signs in as Alice, who sends to the service Family, and needs three sample photos
// (`go run . gen <dir>` made them) that are not yet in her library, so it takes the first three of the folder it is given.
//
//	go run . send <outdir> <photo folder>
func sendShots(outdir, photos string) {
	os.MkdirAll(outdir, 0o755)
	var files []string
	first := 2040 // FIRST moves on to other sample photos, for a second run against the same Immich (it ignores a photo it already has)
	fmt.Sscan(env("FIRST", "2040"), &first)
	for i := 0; i < 3; i++ {
		p := filepath.Join(photos, fmt.Sprintf("IMG_%04d.jpg", first+i))
		if _, err := os.Stat(p); err != nil {
			log.Fatalf("sample photo %s: %v (run `go run . gen <dir>` first)", p, err)
		}
		files = append(files, p)
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("no-sandbox", true), chromedp.Flag("hide-scrollbars", true), chromedp.Flag("disable-gpu", true))
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelA()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()
	ctx, cancelT := context.WithTimeout(ctx, 3*time.Minute)
	defer cancelT()

	setup := chromedp.Tasks{
		emulation.SetDeviceMetricsOverride(390, 844, 1.5, true),
		emulation.SetTouchEmulationEnabled(true),
		inEnglish(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: "light"}}).Do(ctx)
		}),
	}
	for _, c := range loginCookies("alice@test.local") {
		c := c
		setup = append(setup, chromedp.ActionFunc(func(ctx context.Context) error {
			return network.SetCookie(c.Name, c.Value).WithDomain(c.Domain).WithPath(c.Path).WithHTTPOnly(c.HTTPOnly).Do(ctx)
		}))
	}
	var buf []byte
	save := func(name string) {
		if err := os.WriteFile(filepath.Join(outdir, name+".png"), buf, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%-28s %6d KB\n", name+".png", len(buf)/1024)
	}
	must := func(what string, a ...chromedp.Action) {
		if err := chromedp.Run(ctx, a...); err != nil {
			log.Fatalf("%s: %v", what, err)
		}
	}
	must("setup", setup)
	must("open", chromedp.Navigate(appURL+"send"), chromedp.WaitVisible(`#send-files`, chromedp.ByQuery), chromedp.Sleep(800*time.Millisecond))

	// the card that offers to install (Chrome shows it only where it can; here it is shown for the picture)
	must("install card", chromedp.Evaluate(`document.getElementById('send-install').hidden = false`, nil), chromedp.Sleep(300*time.Millisecond),
		screenshotElement(`//section[@id='send-install']`, 12, &buf))
	save("send-install")
	must("hide the card", chromedp.Evaluate(`document.getElementById('send-install').hidden = true`, nil))

	// photos chosen, a service picked
	must("choose", chromedp.SetUploadFiles(`#send-files`, files, chromedp.ByQuery), chromedp.Poll(`document.getElementById('send-go').disabled === false`, nil), chromedp.Sleep(500*time.Millisecond), chromedp.CaptureScreenshot(&buf))
	save("send-phone")

	// sending, slowly enough to catch the queue half way (the first send asks about locations)
	must("slow", network.EmulateNetworkConditions(false, 0, -1, 50_000))
	must("send", chromedp.Click(`#send-go`, chromedp.ByQuery), chromedp.Sleep(700*time.Millisecond))
	for i := 0; i < 20; i++ {
		var open bool
		chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('confirm').open`, &open))
		if !open {
			break
		}
		chromedp.Run(ctx, chromedp.Click(`#confirm-ok`, chromedp.ByQuery))
		time.Sleep(400 * time.Millisecond)
	}
	halfway := func() error {
		return chromedp.Run(ctx, chromedp.Poll(`document.querySelector('.send-item.done') !== null && document.querySelector('.send-item.sending') !== null && document.querySelector('.send-item.waiting') !== null`, nil, chromedp.WithPollingTimeout(60*time.Second)))
	}
	if err := halfway(); err != nil {
		var dump string
		chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('send-list').innerText + ' | ' + document.querySelector('.send-summary').textContent + ' | ' + document.getElementById('flash').textContent + ' | confirm open: ' + document.getElementById('confirm').open`, &dump))
		log.Fatalf("the queue never showed a file half way: %v; the page shows: %s", err, dump)
	}
	must("shot", chromedp.Evaluate(`document.getElementById('send-list').scrollIntoView({block: 'center'})`, nil), chromedp.Sleep(300*time.Millisecond), chromedp.CaptureScreenshot(&buf))
	save("send-queue")
}
