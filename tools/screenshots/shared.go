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
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// sharedShots takes the pictures of an album shared with someone who has no Handoff: the form that lists exactly what the key can
// do, and the row of an album being shared both ways. "Cousin" is Family's account on the demo Immich with a key made for the
// purpose, so one Immich and one app are enough. The app must run with SHARED_ALBUMS_ENABLE=true, BRIDGE_ALLOW_PRIVATE=true and a
// PUBLIC_APP_URL (SHARED=1 seed.sh reset does that).
//
//	go run . shared <outdir> <photo folder>      (FIRST moves on to other sample photos, as for `send`)
func sharedShots(outdir, photos string) {
	os.MkdirAll(outdir, 0o755)
	first := 2100
	fmt.Sscan(env("FIRST", "2100"), &first)
	token := alice()
	var ids []string
	for i := 0; i < 4; i++ {
		p := filepath.Join(photos, fmt.Sprintf("IMG_%04d.jpg", first+i))
		b, err := os.ReadFile(p)
		if err != nil {
			log.Fatalf("sample photo %s: %v (run `go run . gen <dir> 40` first)", p, err)
		}
		ids = append(ids, upload(token, filepath.Base(p), b, time.Date(2026, 7, 4, 10, i*9, 0, 0, time.UTC)))
	}
	for _, id := range ids {
		waitProcessed(token, id)
	}
	apiJSON(token, "POST", "/api/albums", map[string]any{"albumName": "Summer 2026", "assetIds": ids})

	// the key the cousin makes on their own Immich: the three reading permissions added to the four that receive
	famToken := loginToken("family@test.local")
	body, _ := json.Marshal(map[string]any{"name": "from Alice's Handoff", "permissions": []string{"user.read", "asset.upload", "album.create", "albumAsset.create", "asset.read", "asset.download", "album.read"}})
	req, _ := http.NewRequest("POST", immichURL+"/api/api-keys", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+famToken)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	var k struct{ Secret string }
	json.NewDecoder(res.Body).Decode(&k)
	res.Body.Close()
	if k.Secret == "" {
		log.Fatalf("the cousin's key was not made: HTTP %d", res.StatusCode)
	}

	cookies := loginCookies("alice@test.local")
	owner := ""
	for _, c := range cookies {
		owner += c.Name + "=" + c.Value + "; "
	}
	owner = strings.TrimSuffix(owner, "; ")
	post := func(path string, form url.Values) {
		req, _ := http.NewRequest("POST", strings.TrimSuffix(appURL, "/")+path, strings.NewReader(form.Encode()))
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
			log.Fatalf("%s: %d %s", path, res.StatusCode, b)
		}
	}
	post("/peers/share-immich", url.Values{"person": {"Cousin Marco"}, "address": {strings.TrimSuffix(immichURL, "/")}, "key": {k.Secret}, "name": {"Summer 2026"}, "album": {"new"}, "history": {"all"}})
	home := getText(owner, strings.TrimSuffix(appURL, "/")+"/")
	m := sharedIDRe.FindStringSubmatch(home)
	if m == nil {
		log.Fatal("the shared album is not on Alice's page")
	}
	post("/shared/sync", url.Values{"shared": {m[1]}})
	time.Sleep(6 * time.Second)

	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("no-sandbox", true), chromedp.Flag("hide-scrollbars", true), chromedp.Flag("disable-gpu", true))
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelA()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()
	ctx, cancelT := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelT()
	tasks := chromedp.Tasks{emulation.SetDeviceMetricsOverride(1280, 900, 1.5, false), inEnglish(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: "light"}}).Do(ctx)
		})}
	for _, c := range cookies {
		c := c
		tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
			return network.SetCookie(c.Name, c.Value).WithDomain(c.Domain).WithPath(c.Path).WithHTTPOnly(c.HTTPOnly).Do(ctx)
		}))
	}
	if err := chromedp.Run(ctx, tasks); err != nil {
		log.Fatal(err)
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
	must("page", chromedp.Navigate(appURL+"#services"), chromedp.WaitVisible(`#v-services`, chromedp.ByQuery), chromedp.Sleep(1500*time.Millisecond),
		chromedp.Evaluate(`(function(){ const d = [...document.querySelectorAll('#v-services details.offer')].find(x => x.textContent.includes('no Handoff')); if (d) d.open = true })()`, nil), chromedp.Sleep(1200*time.Millisecond),
		screenshotElement(`//details[contains(@class,'offer')][contains(.,'no Handoff')]`, 16, &buf))
	save("shared-oneapp-form")
	must("row", chromedp.Evaluate(`(function(){ const d = [...document.querySelectorAll('#v-services details.svc')].find(x => x.textContent.includes('Cousin Marco')); if (d) d.open = true })()`, nil), chromedp.Sleep(400*time.Millisecond),
		screenshotElement(`//details[contains(@class,'svc')][contains(.,'Cousin Marco')]`, 16, &buf))
	save("shared-oneapp-row")
}

var sharedIDRe = regexp.MustCompile(`shared(?:&#34;|"):(?:&#34;|")([0-9a-f-]{36})`)
