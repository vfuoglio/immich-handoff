package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// storiesShots takes the pictures of the Stories guide: the form that makes a story, the card of a story that is ready (with its
// link and QR code), and the story itself on a phone. Alice uploads twelve sample photos over a week, makes an album of them and a
// story of the album through the same requests the page makes, and waits until it is ready.
//
//	go run . stories <outdir> <photo folder>      (needs the sample photos from `go run . gen <dir> 80`; FIRST picks twelve others in a row)
func storiesShots(outdir, photos string) {
	os.MkdirAll(outdir, 0o755)
	// the sample photos with a bright sky (the generated landscapes cycle through six palettes; the dusk and night ones look dim in a story)
	pick := []int{2088, 2090, 2092, 2093, 2094, 2096, 2098, 2099, 2100, 2102, 2104, 2105}
	if v := env("FIRST", ""); v != "" { // or twelve in a row from FIRST, as for `room`
		first := 0
		fmt.Sscan(v, &first)
		for i := range pick {
			pick[i] = first + i
		}
	}
	token := alice()
	var ids []string
	days := []time.Time{time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC), time.Date(2026, 4, 29, 11, 30, 0, 0, time.UTC), time.Date(2026, 4, 30, 16, 10, 0, 0, time.UTC),
		time.Date(2026, 5, 1, 9, 20, 0, 0, time.UTC), time.Date(2026, 5, 2, 14, 5, 0, 0, time.UTC), time.Date(2026, 5, 3, 18, 40, 0, 0, time.UTC)}
	for i := 0; i < 12; i++ {
		p := filepath.Join(photos, fmt.Sprintf("IMG_%04d.jpg", pick[i]))
		b, err := os.ReadFile(p)
		if err != nil {
			log.Fatalf("sample photo %s: %v (run `go run . gen <dir> 40` first)", p, err)
		}
		ids = append(ids, upload(token, filepath.Base(p), b, days[i/2].Add(time.Duration(i%2)*47*time.Minute)))
	}
	for _, id := range ids {
		waitProcessed(token, id)
	}
	album := apiJSON(token, "POST", "/api/albums", map[string]any{"albumName": "Lisbon, spring 2026", "assetIds": ids})["id"].(string)

	owner := ""
	for _, c := range loginCookies("alice@test.local") {
		owner += c.Name + "=" + c.Value + "; "
	}
	owner = strings.TrimSuffix(owner, "; ")
	base := strings.TrimSuffix(appURL, "/")

	// the form, before there is a story (so the card is not crowded)
	take(shot{name: "story-form", who: "alice@test.local", url: appURL, width: 1280, height: 900, js: storyOpenForm, wait: `document.readyState==='complete'`, pause: 3 * time.Second, after: 3 * time.Second,
		xpath: `//div[contains(@class,'storiescard')]`, pad: 16}, outdir)

	form := url.Values{"title": {"A week in Lisbon"}, "kind": {"album"}, "album": {album}, "ends": {"30"}}
	req, _ := http.NewRequest("POST", base+"/stories", strings.NewReader(form.Encode()))
	req.Header.Set("Cookie", owner)
	req.Header.Set("X-Requested-With", "immich-handoff")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode >= 300 {
		log.Fatalf("making the story: %d %s", res.StatusCode, b)
	}
	link := regexp.MustCompile(`data-link="story/([0-9a-f]+)"`)
	var tokenOfStory string
	for i := 0; i < 90 && tokenOfStory == ""; i++ {
		time.Sleep(2 * time.Second)
		if m := link.FindStringSubmatch(getText(owner, base+"/")); m != nil {
			tokenOfStory = m[1]
		}
	}
	if tokenOfStory == "" {
		log.Fatal("the story did not become ready")
	}
	take(shot{name: "story-card", who: "alice@test.local", url: appURL, width: 1280, height: 900, js: storyShowQR, pause: 2 * time.Second,
		xpath: `//div[contains(@class,'storiescard')]`, pad: 16}, outdir)

	// the story itself, as someone who got the link sees it: on a computer, no account
	storyURL := base + "/story/" + tokenOfStory
	advance := func(n int) string {
		return fmt.Sprintf(`(async()=>{for(let i=0;i<%d;i++){document.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true}));window.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true}));await new Promise(r=>setTimeout(r,350))}})()`, n)
	}
	// the title card lasts 3.5 seconds, a chapter card 3 and a photo 6: look at each a moment after it has come in
	for _, s := range []struct {
		name          string
		steps         int
		before, after time.Duration
	}{{"story-title", 0, 1200 * time.Millisecond, 0}, {"story-photo", 2, 700 * time.Millisecond, 3500 * time.Millisecond}} {
		storyScreen(storyURL, outdir, s.name, advance(s.steps), s.before, s.after)
	}
	fmt.Println("the story:", storyURL)
}

// storyScreen takes the public story page in a 1280 by 720 window (no sign-in), after running js (to move on a few slides).
func storyScreen(u, outdir, name, js string, before, after time.Duration) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("no-sandbox", true), chromedp.Flag("hide-scrollbars", true), chromedp.Flag("disable-gpu", true))
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelA()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()
	ctx, cancelT := context.WithTimeout(ctx, time.Minute)
	defer cancelT()
	var buf []byte
	err := chromedp.Run(ctx,
		emulation.SetDeviceMetricsOverride(1280, 720, 1.5, false), inEnglish(),
		network.Enable(),
		chromedp.Navigate(u), chromedp.WaitReady("body"), chromedp.Sleep(before),
		chromedp.Evaluate(js, nil), chromedp.Sleep(after),
		chromedp.CaptureScreenshot(&buf),
	)
	if err != nil {
		log.Fatalf("%s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(outdir, name+".png"), buf, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%-28s %6d KB\n", name+".png", len(buf)/1024)
}

// the bar is sticky: it would show through a picture of one card
const storyHideBar = `document.querySelectorAll('header.bar, nav.tabs').forEach(e => e.style.visibility = 'hidden');`

const storyOpenForm = storyHideBar + `document.querySelector('nav.tabs a[data-view="settings"]').click();
		const cat = document.querySelector('.settings-nav [data-cat="sharing"]'); if (cat) cat.click();
		const d = document.querySelector('.storiescard details.offer'); d.open = true;`

const storyShowQR = storyHideBar + `document.querySelector('nav.tabs a[data-view="settings"]').click();
		const cat = document.querySelector('.settings-nav [data-cat="sharing"]'); if (cat) cat.click();
		document.querySelector('.storiescard .show-qr').click();`
