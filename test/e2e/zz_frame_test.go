//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestFrame: a frame shows one of the owner's albums on a screen with no account. A real album and a real Immich, the
// frame page in headless Chrome with no sign-in: it shows a photo, picks up one added to the album, drops one taken out,
// and the link stops when the owner ends it.
func TestFrame(t *testing.T) {
	alice, _, _, _ := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 63)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir(), "FRAME_REFRESH=5s")
	owner := loginCookie(t, "alice@e2e.test")

	first, _ := upload(t, alice, "frame-1.jpg", texturedJPEG(31))
	var album struct{ ID string }
	mustOK(t, call(t, "POST", "/albums", keyAuth(alice), map[string]any{"albumName": "Frame album", "assetIds": []string{first}}, &album), "make the album")

	code, body := postBody(t, port, "/frames", owner, url.Values{"name": {"Living room"}, "album": {album.ID}, "ends": {"0"}, "hold": {"8"}})
	if code != 204 {
		t.Fatalf("make the frame: %d %s", code, body)
	}
	m := regexp.MustCompile(`data-link="frame/([0-9a-f]{64})"`).FindStringSubmatch(page(t, port, owner))
	if m == nil {
		t.Fatal("the frame's link is not on the owner's page")
	}
	token := m[1]
	base := "http://localhost:" + port + "/frame/" + token
	feed := func() (int, []string) {
		res, err := http.Get(base + "/feed")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		var out struct{ Items []struct{ ID string } }
		json.Unmarshal(b, &out)
		var ids []string
		for _, i := range out.Items {
			ids = append(ids, i.ID)
		}
		return res.StatusCode, ids
	}

	// the form that makes a frame lists the owner's albums when it is opened (it is loaded lazily, inside a closed details)
	octx := browse(t, port, owner, look{})
	run(t, octx, chromeNavigate("http://localhost:"+port+"/"))
	until(t, octx, "the album list loads when the form is opened", `(() => {
		const d = [...document.querySelectorAll('details.offer')].find(x => x.querySelector('#fr-name'));
		if (!d) return false;
		d.open = true;
		const s = document.querySelector('#fr-album');
		return !!s && s.options.length > 0;
	})()`, 20*time.Second)

	// a screen with no account opens the link and shows the photo
	ctx := browse(t, port, "x=y", look{})
	run(t, ctx, chromeNavigate(base))
	until(t, ctx, "a photo is on the screen", `(() => { const i = document.querySelector('.wall-stage figure.single img'); return !!i && i.complete && i.naturalWidth > 0; })()`, 40*time.Second)
	if got := js[bool](t, ctx, `document.cookie === '' || !document.cookie.includes('immich')`); !got {
		t.Error("the frame page must hold no Immich session")
	}
	run(t, ctx, chromeEvaluate(`window.__seen = new Set(); window.__fresh = false; setInterval(() => { document.querySelectorAll('.wall-stage img').forEach(i => window.__seen.add(i.src)); if (document.querySelector('.wall-stage figure.single.fresh')) window.__fresh = true; }, 200); 0`))

	// a photo added to the album appears on the screen within a refresh and a hold
	second, _ := upload(t, alice, "frame-2.jpg", texturedJPEG(32))
	mustOK(t, call(t, "PUT", "/albums/"+album.ID+"/assets", keyAuth(alice), map[string]any{"ids": []string{second}}, nil), "add a photo to the album")
	eventually(t, "the feed lists both photos", 40*time.Second, func() bool { _, ids := feed(); return len(ids) == 2 })
	until(t, ctx, "the new photo is shown", `window.__seen.size >= 2`, 60*time.Second)
	// a photo that arrives is given a moment: a ring settles round it
	until(t, ctx, "the new arrival is given a ring", `window.__fresh === true`, 5*time.Second)

	// the buttons choose how photos give way and what is behind them; the screen remembers, and the address can choose too
	run(t, ctx, chromedp.Click(`.wall-controls button[data-fx="zoom"]`, chromedp.ByQuery), chromedp.Click(`.wall-controls button[data-bg="black"]`, chromedp.ByQuery))
	if got := js[string](t, ctx, `document.querySelector('.wall-stage').dataset.fx + '/' + document.getElementById('frame').dataset.bg`); got != "zoom/black" {
		t.Errorf("the buttons choose the transition and the backdrop: %q", got)
	}
	run(t, ctx, chromeNavigate(base))
	until(t, ctx, "the choice is remembered after a reload", `document.querySelector('.wall-stage').dataset.fx === 'zoom' && document.getElementById('frame').dataset.bg === 'black'`, 10*time.Second)
	until(t, ctx, "a zoomed photo drifts", `(() => { const i = document.querySelector('.wall-stage figure.single img'); return !!i && i.complete && i.getAnimations().length > 0; })()`, 40*time.Second)
	run(t, ctx, chromeNavigate(base+"?fx=slide&bg=glow"))
	until(t, ctx, "the address wins over what the screen remembered", `document.querySelector('.wall-stage').dataset.fx === 'slide' && document.getElementById('frame').dataset.bg === 'glow'`, 10*time.Second)
	// with reduced motion no photo drifts and nothing is drawn moving behind it
	still := browse(t, port, "x=y", look{reduce: true})
	run(t, still, chromeNavigate(base+"?fx=zoom"))
	until(t, still, "a photo is on the screen", `(() => { const i = document.querySelector('.wall-stage figure.single img'); return !!i && i.complete && i.naturalWidth > 0; })()`, 40*time.Second)
	if n := js[float64](t, still, `document.getAnimations().filter(a => a.animationName && a.animationName.startsWith('wall-')).length`); n != 0 {
		t.Errorf("reduced motion: %v wall animations are still running", n)
	}

	// a photo taken out of the album is gone from the list, and its picture cannot be fetched any more
	_, both := feed()
	mustOK(t, call(t, "DELETE", "/albums/"+album.ID+"/assets", keyAuth(alice), map[string]any{"ids": []string{second}}, nil), "take a photo out of the album")
	eventually(t, "the feed lists one photo", 40*time.Second, func() bool { _, ids := feed(); return len(ids) == 1 })
	gone := both[0]
	if _, now := feed(); now[0] == both[0] {
		gone = both[1]
	}
	if res, err := http.Get(base + "/photo/" + gone); err != nil || res.StatusCode != 404 {
		t.Errorf("a photo taken out of the album: %v %v", res, err)
	}

	// the owner ends it: the link stops at once
	var id string
	if m := regexp.MustCompile(`hx-vals='\{"frame":"([0-9a-f-]{36})"\}'|&#34;frame&#34;:&#34;([0-9a-f-]{36})&#34;`).FindStringSubmatch(page(t, port, owner)); m != nil {
		id = m[1] + m[2]
	}
	if id == "" {
		t.Fatal("the end button is not on the owner's page")
	}
	if code, body := postBody(t, port, "/frames/end", owner, url.Values{"frame": {id}}); code != 204 {
		t.Fatalf("end the frame: %d %s", code, body)
	}
	if code, _ := feed(); code != 404 {
		t.Errorf("an ended frame's feed: %d", code)
	}
	if res, err := http.Get(base); err != nil || res.StatusCode != 404 {
		t.Errorf("an ended frame's page: %v %v", res, err)
	}
	for _, k := range keyNames(t, alice) {
		if k == "handoff: frame Living room" {
			t.Error("the frame's key must be deleted from the owner's account")
		}
	}
}

func chromeNavigate(u string) chromedp.Action { return chromedp.Navigate(u) }

func chromeEvaluate(expr string) chromedp.Action { return chromedp.Evaluate(expr, nil) }
