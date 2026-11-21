//go:build e2e

package e2e

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestCast: a TV is paired by a code on a real Immich. A headless Chrome with no sign-in opens /tv and shows a code; alice types it
// on her page for an album; the TV shows that album's photos, does what she tells it from her phone (pause, next), and stops when
// she stops it, after which her cast key is gone from Immich. Five wrong codes silence a person.
func TestCast(t *testing.T) {
	alice, _, _, _ := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 99)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir(), "FRAME_REFRESH=5s")
	cookie := loginCookie(t, "alice@e2e.test")
	stamp := fmt.Sprint(time.Now().UnixNano())

	first, _ := upload(t, alice, "cast-1.jpg", texturedJPEG(41))
	second, _ := upload(t, alice, "cast-2.jpg", texturedJPEG(42))
	var album struct{ ID string }
	mustOK(t, call(t, "POST", "/albums", keyAuth(alice), map[string]any{"albumName": "Cast album " + stamp, "assetIds": []string{first, second}}, &album), "make the album")
	castKeys := func() int {
		var keys []struct{ Name string }
		call(t, "GET", "/api-keys", keyAuth(alice), nil, &keys)
		n := 0
		for _, k := range keys {
			if k.Name == "handoff: cast" {
				n++
			}
		}
		return n
	}

	if code, _ := getBody(t, port, "/tv", ""); code != 200 {
		t.Fatalf("the TV page needs no sign-in: %d", code)
	}
	// the TV: a browser with no account
	tv := browse(t, port, "x=y", look{w: 1920, h: 1080})
	run(t, tv, chromeNavigate("http://localhost:"+port+"/tv"))
	until(t, tv, "the TV shows a six-character code", `(() => { const c = document.querySelector('.tv-code'); return !!c && /^[A-Z0-9]{6}$/.test(c.textContent.trim()); })()`, 30*time.Second)
	code := js[string](t, tv, `document.querySelector('.tv-code').textContent.trim()`)
	if h := js[string](t, tv, `document.querySelector('.tv-hint').textContent`); !strings.Contains(h, "localhost:"+port) {
		t.Errorf("the TV says which address to open on the phone: %q", h)
	}

	// the phone: wrong codes first (five silence the person), then the right one
	form := func(path string, v url.Values) (int, string) { return postBody(t, port, path, cookie, v) }
	for i := 0; i < 4; i++ {
		if c, _ := form("/cast/claim", url.Values{"code": {"zzzzzz"}, "kind": {"new"}}); c != 409 {
			t.Fatalf("a wrong code: %d", c)
		}
	}
	if c, b := form("/cast/claim", url.Values{"code": {strings.ToLower(code)}, "kind": {"album"}, "album": {album.ID}, "seconds": {"8"}, "fx": {"zoom"}}); c != 204 {
		t.Fatalf("the right code: %d %s", c, b)
	}
	if castKeys() != 1 {
		t.Fatalf("one key for casting in Immich: %d", castKeys())
	}
	if p := page(t, port, cookie); !strings.Contains(p, "Cast album "+stamp) || !strings.Contains(p, "Showing") {
		t.Errorf("the card lists the TV")
	}

	// the TV starts showing the album's photos
	until(t, tv, "the code goes away and a photo is on the screen", `(() => document.querySelector('.tv-pair').hidden && !!document.querySelector('.wall-stage figure img'))()`, 60*time.Second)
	n := js[int](t, tv, `document.querySelectorAll('.wall-stage figure').length`)
	if n < 1 {
		t.Fatalf("a photo is shown: %d", n)
	}
	// the phone pauses it and asks for the next photo
	tvID := regexp.MustCompile(`[0-9a-f]{64}`).FindString(page(t, port, cookie))
	if tvID == "" {
		t.Fatal("the TV's id is on the card")
	}
	if c, b := form("/cast/control", url.Values{"tv": {tvID}, "do": {"pause"}}); c != 204 {
		t.Fatalf("pause: %d %s", c, b)
	}
	if c, b := form("/cast/control", url.Values{"tv": {tvID}, "do": {"next"}}); c != 204 {
		t.Fatalf("next: %d %s", c, b)
	}
	shown := func() string {
		return js[string](t, tv, `[...document.querySelectorAll('.wall-stage figure.in img')].map(i => i.src).join(',')`)
	}
	before := shown()
	until(t, tv, "the TV carries out next", fmt.Sprintf(`(() => [...document.querySelectorAll('.wall-stage figure.in img')].map(i => i.src).join(',') !== %q)()`, before), 20*time.Second)

	// an unknown id gets nothing, and a code is used once
	if c, _ := getBody(t, port, "/tv/feed?s="+strings.Repeat("0", 64), ""); c != 404 {
		t.Errorf("an unknown TV: %d", c)
	}
	if c, _ := form("/cast/claim", url.Values{"code": {code}, "kind": {"new"}}); c != 409 {
		t.Errorf("a code works once: %d", c)
	}

	// stopping: the TV starts over with a new code, and the key is gone from Immich
	if c, b := form("/cast/control", url.Values{"tv": {tvID}, "do": {"stop"}}); c != 204 {
		t.Fatalf("stop: %d %s", c, b)
	}
	if castKeys() != 0 {
		t.Errorf("the cast key is deleted with the last TV: %d", castKeys())
	}
	until(t, tv, "the TV shows a new code", fmt.Sprintf(`(() => { const c = document.querySelector('.tv-code'); return !document.querySelector('.tv-pair').hidden && /^[A-Z0-9]{6}$/.test(c.textContent.trim()) && c.textContent.trim() !== %q; })()`, code), 40*time.Second)

	// the legacy page does the same with no modern script
	old := browse(t, port, "x=y", look{w: 1280, h: 720})
	run(t, old, chromeNavigate("http://localhost:"+port+"/tv/legacy"))
	until(t, old, "the legacy page shows a code", `(() => { const c = document.querySelector('.tv-code'); return !!c && /^[A-Z0-9]{6}$/.test(c.textContent.trim()); })()`, 30*time.Second)
	legacyCode := js[string](t, old, `document.querySelector('.tv-code').textContent.trim()`)
	if c, b := form("/cast/claim", url.Values{"code": {legacyCode}, "kind": {"album"}, "album": {album.ID}, "seconds": {"8"}}); c != 204 {
		t.Fatalf("the legacy TV's code: %d %s", c, b)
	}
	until(t, old, "the legacy page shows a photo", `(() => [...document.querySelectorAll('.tvl-layer.in')].some(l => (l.style.backgroundImage || '').indexOf('tv/photo') >= 0))()`, 60*time.Second)

	// five wrong codes silence the person, even for a right one
	for i := 0; i < 5; i++ {
		form("/cast/claim", url.Values{"code": {"zzzzzz"}, "kind": {"new"}})
	}
	other := browse(t, port, "x=y", look{})
	run(t, other, chromeNavigate("http://localhost:"+port+"/tv"))
	until(t, other, "another TV shows a code", `(() => { const c = document.querySelector('.tv-code'); return !!c && /^[A-Z0-9]{6}$/.test(c.textContent.trim()); })()`, 30*time.Second)
	right := js[string](t, other, `document.querySelector('.tv-code').textContent.trim()`)
	if c, b := form("/cast/claim", url.Values{"code": {right}, "kind": {"new"}}); c != 409 || !strings.Contains(b, "wrong codes") {
		t.Errorf("a silenced person: %d %s", c, b)
	}
}
