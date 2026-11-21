//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestStories: a person makes a story from an album on a real Immich. The photos are chosen in the background, the public link opens
// the player in headless Chrome with no sign-in (a title card, a chapter card, photos by tap), the editor's switches change what the
// page says, the story plays on a TV through the cast code, and removing it deletes its key.
func TestStories(t *testing.T) {
	alice, _, _, _ := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 100)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir(), "PUBLIC_APP_URL=https://photos.example.test")
	cookie := loginCookie(t, "alice@e2e.test")
	stamp := fmt.Sprint(time.Now().UnixNano())

	var ids []string
	for i := 0; i < 6; i++ {
		id, _ := upload(t, alice, fmt.Sprintf("story-%d.jpg", i), texturedJPEG(uint32(200+i)))
		ids = append(ids, id)
	}
	var album struct{ ID string }
	mustOK(t, call(t, "POST", "/albums", keyAuth(alice), map[string]any{"albumName": "Story album " + stamp, "assetIds": ids}, &album), "make the album")
	storyKeys := func() int {
		var keys []struct{ Name string }
		call(t, "GET", "/api-keys", keyAuth(alice), nil, &keys)
		n := 0
		for _, k := range keys {
			if strings.HasPrefix(k.Name, "handoff: story ") {
				n++
			}
		}
		return n
	}
	form := func(path string, v url.Values) (int, string) { return postBody(t, port, path, cookie, v) }

	if c, b := form("/stories", url.Values{"kind": {"album"}, "album": {"00000000-0000-4000-8000-000000000000"}}); c != 409 {
		t.Errorf("an album that is not theirs: %d %s", c, b)
	}
	if c, b := form("/stories", url.Values{"title": {"Summer " + stamp[len(stamp)-6:]}, "kind": {"album"}, "album": {album.ID}, "ends": {"30"}}); c != 204 {
		t.Fatalf("make the story: %d %s", c, b)
	}
	if storyKeys() != 1 {
		t.Fatalf("one key for the story: %d", storyKeys())
	}
	var token, id string
	eventually(t, "the story is ready and has its link on the card", 180*time.Second, func() bool {
		p := page(t, port, cookie)
		m := regexp.MustCompile(`data-link="story/([0-9a-f]{64})"`).FindStringSubmatch(p)
		n := regexp.MustCompile(`href="stories/([0-9a-f-]{36})"`).FindStringSubmatch(p)
		if m == nil || n == nil {
			return false
		}
		token, id = m[1], n[1]
		return true
	})
	base := "http://localhost:" + port + "/story/" + token
	code, data := getBody(t, port, "/story/"+token+"/data", "x=y")
	if code != 200 || !strings.Contains(data, `"scenes"`) || strings.Contains(data, ids[0]) {
		t.Fatalf("the public data: %d %.300s", code, data)
	}
	if c, _ := getBody(t, port, "/story/"+strings.Repeat("0", 64), "x=y"); c != 404 {
		t.Errorf("an unknown story: %d", c)
	}

	// the player, with no account
	ctx := browse(t, port, "x=y", look{w: 390, h: 844, touch: true})
	run(t, ctx, chromeNavigate(base))
	until(t, ctx, "the title card", `(() => !!document.querySelector('.st-title') && document.querySelector('.st-title').textContent.startsWith('Summer'))()`, 30*time.Second)
	until(t, ctx, "a photo after a tap or two", `(() => { const r = document.getElementById('story'); r.dispatchEvent(new PointerEvent('pointerdown', {clientX: 300, clientY: 400, bubbles: true})); r.dispatchEvent(new PointerEvent('pointerup', {clientX: 300, clientY: 400, bubbles: true})); return !!document.querySelector('.wall-stage figure img:not(.st-back)'); })()`, 60*time.Second)
	if d := js[string](t, ctx, `getComputedStyle(document.querySelector('.wall-wait')).display`); d != "none" {
		t.Errorf("the waiting message and its circle are gone once the story plays: display %q", d)
	}
	if h := js[string](t, ctx, `document.querySelector('.st-bar').style.transform`); !strings.HasPrefix(h, "scaleX(") {
		t.Errorf("a progress bar: %q", h)
	}
	if n := js[int](t, ctx, `document.querySelectorAll('.wall-stage figure').length`); n < 1 {
		t.Errorf("the stage has a scene: %d", n)
	}

	// the editor: dates off, the preview on
	if c, b := getBody(t, port, "/stories/"+id, cookie); c != 200 || !strings.Contains(b, "What the page shows") {
		t.Fatalf("the editor: %d", c)
	}
	if c, b := form("/stories/edit", url.Values{"story": {id}, "title": {"Summer"}, "places": {"1"}, "preview": {"1"}, "ends": {"keep"}}); c != 204 {
		t.Fatalf("edit: %d %s", c, b)
	}
	_, data = getBody(t, port, "/story/"+token+"/data", "x=y")
	if strings.Contains(data, `"date":"20`) {
		t.Errorf("dates are off: %.300s", data)
	}
	_, pageHTML := getBody(t, port, "/story/"+token, "x=y")
	if !strings.Contains(pageHTML, `og:image" content="https://photos.example.test/story/`+token+`/cover"`) {
		t.Errorf("the preview tags")
	}
	eventually(t, "the cover comes through (Immich makes thumbnails a moment after an upload)", 90*time.Second, func() bool {
		c, _ := getBody(t, port, "/story/"+token+"/cover", "x=y")
		return c == 200
	})

	// a TV plays it in order
	tv := browse(t, port, "x=y", look{w: 1920, h: 1080})
	run(t, tv, chromeNavigate("http://localhost:"+port+"/tv"))
	until(t, tv, "the TV shows a code", `(() => { const c = document.querySelector('.tv-code'); return !!c && /^[A-Z0-9]{6}$/.test(c.textContent.trim()); })()`, 30*time.Second)
	codeText := js[string](t, tv, `document.querySelector('.tv-code').textContent.trim()`)
	if c, b := form("/cast/claim", url.Values{"code": {codeText}, "kind": {"story"}, "story": {id}, "seconds": {"8"}}); c != 204 {
		t.Fatalf("cast the story: %d %s", c, b)
	}
	until(t, tv, "the TV shows a photo of the story", `(() => document.querySelector('.tv-pair').hidden && !!document.querySelector('.wall-stage figure img'))()`, 60*time.Second)

	// removing the story deletes its link and its key
	if c, b := form("/stories/end", url.Values{"story": {id}}); c != 204 {
		t.Fatalf("remove: %d %s", c, b)
	}
	res, err := http.Get(base + "/data")
	if err != nil || res.StatusCode != 404 {
		t.Errorf("a removed story: %v", res)
	}
	if storyKeys() != 0 {
		t.Errorf("the story's key is deleted: %d", storyKeys())
	}
}
