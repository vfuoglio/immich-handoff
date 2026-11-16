package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// The graphics of the product film are small web pages whose animations are driven by a clock the renderer sets, so every
// frame is exact whatever the machine's speed: `video render <page.html> <outdir> <seconds> [fps]` writes one PNG for each
// frame, and `video stills <page.html> <outdir>` writes one transparent PNG for each element with a data-still name (the
// captions). See tools/video/build.py.

func newRenderer(w, h int64, transparent bool) (context.Context, context.CancelFunc) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("no-sandbox", true), chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("disable-gpu", true), chromedp.Flag("allow-file-access-from-files", true), chromedp.Flag("force-device-scale-factor", "1"))
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(actx)
	ctx, cancelT := context.WithTimeout(ctx, 20*time.Minute)
	tasks := chromedp.Tasks{emulation.SetDeviceMetricsOverride(w, h, 1, false)}
	if transparent {
		tasks = append(tasks, emulation.SetDefaultBackgroundColorOverride().WithColor(&cdp.RGBA{R: 0, G: 0, B: 0, A: 0})) // nil would put the default, white, back
	}
	if err := chromedp.Run(ctx, tasks...); err != nil {
		log.Fatal(err)
	}
	return ctx, func() { cancelT(); cancel(); cancelA() }
}

func load(ctx context.Context, file string) {
	abs, _ := filepath.Abs(file)
	if err := chromedp.Run(ctx, chromedp.Navigate("file://"+abs), chromedp.WaitReady("body")); err != nil {
		log.Fatal(err)
	}
	// fonts and pictures must be there before the first frame
	err := chromedp.Run(ctx, chromedp.Poll(`document.fonts.status === 'loaded' && [...document.images].every(i => i.complete)`, nil, chromedp.WithPollingTimeout(60*time.Second)))
	if err != nil {
		log.Fatalf("the page did not finish loading: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
}

const seekJS = `(t) => { document.getAnimations({subtree: true}).forEach(a => { a.pause(); a.currentTime = t * 1000; }); return true; }`

func renderFrames(file, outdir string, seconds float64, fps int) {
	os.RemoveAll(outdir)
	os.MkdirAll(outdir, 0o755)
	ctx, done := newRenderer(1920, 1080, false)
	defer done()
	load(ctx, file)
	n := int(seconds * float64(fps))
	for i := 0; i < n; i++ {
		t := float64(i) / float64(fps)
		var buf []byte
		if err := chromedp.Run(ctx,
			chromedp.Evaluate(fmt.Sprintf("(%s)(%f)", seekJS, t), nil),
			chromedp.ActionFunc(func(ctx context.Context) error {
				var err error
				buf, err = page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatPng).Do(ctx)
				return err
			})); err != nil {
			log.Fatalf("frame %d: %v", i, err)
		}
		os.WriteFile(filepath.Join(outdir, fmt.Sprintf("%05d.png", i)), buf, 0o644)
	}
	fmt.Printf("%s: %d frames\n", filepath.Base(file), n)
}

func renderStills(file, outdir string) {
	os.MkdirAll(outdir, 0o755)
	ctx, done := newRenderer(1920, 3200, true) // tall enough for every caption to be on the page
	defer done()
	load(ctx, file)
	var names []string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`[...document.querySelectorAll('[data-still]')].map(e => e.dataset.still)`, &names)); err != nil {
		log.Fatal(err)
	}
	for _, name := range names {
		var box struct{ X, Y, W, H float64 }
		chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`(() => { const r = document.querySelector('[data-still=%q]').getBoundingClientRect(); return {X: r.x, Y: r.y, W: r.width, H: r.height}; })()`, name), &box))
		var buf []byte
		if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			buf, err = page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatPng).
				WithClip(&page.Viewport{X: box.X, Y: box.Y, Width: box.W, Height: box.H, Scale: 1}).Do(ctx)
			return err
		})); err != nil {
			log.Fatalf("%s: %v", name, err)
		}
		os.WriteFile(filepath.Join(outdir, name+".png"), buf, 0o644)
	}
	fmt.Printf("%d stills\n", len(names))
}
