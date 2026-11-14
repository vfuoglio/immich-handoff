//go:build e2e

// Named to sort last, like the other zz_ tests: it leaves an album, photos and a key in the shared Immich.
package e2e

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// roomFixture starts the app and makes alice's album of three real photos.
func roomFixture(t *testing.T, offset int) (port, owner string, alice user, album string, photos []string) {
	t.Helper()
	alice, _, _, _ = bootstrap(t)
	bin := buildApp(t)
	port = fmt.Sprint(atoi(appPort) + offset)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	owner = loginCookie(t, "alice@e2e.test")
	seed := uint32(time.Now().UnixNano())
	for i := 0; i < 3; i++ {
		id, _ := upload(t, alice, fmt.Sprintf("room-%d-%d.jpg", seed, i), noiseJPEG(seed+uint32(i), 40, 220))
		waitProcessed(t, alice, id)
		photos = append(photos, id)
	}
	var a struct {
		ID string `json:"id"`
	}
	mustOK(t, call(t, "POST", "/albums", keyAuth(alice), map[string]any{"albumName": fmt.Sprintf("Shoot %d", seed), "assetIds": photos}, &a), "create album")
	return port, owner, alice, a.ID, photos
}

// TestRoom: a photographer makes a client room from an album; a client on a phone opens the link, gives the PIN, sees marked
// pictures (never the plain preview), looks at them larger; the photographer pauses and ends the room.
func TestRoom(t *testing.T) {
	port, owner, alice, album, photos := roomFixture(t, 74)
	name := fmt.Sprintf("rossi-%d", time.Now().UnixNano()%1_000_000)
	code, body := postBody(t, port, "/rooms", owner, url.Values{"name": {name}, "title": {"Chiara and Luca"}, "album": {album}, "pin": {"2468"}, "mark": {"text"}, "marktext": {"Chiara Bianchi"}, "ends": {"0"}, "pickmode": {"shared"}, "picklimit": {"2"}})
	if code != 204 {
		t.Fatalf("make a room: %d %s", code, body)
	}
	perms := keyPermissions(t, alice, "handoff: room "+name)
	if !has(perms, "asset.view") || !has(perms, "asset.read") || has(perms, "asset.download") || has(perms, "asset.delete") {
		t.Fatalf("the room's key looks at pictures and lists an album, and cannot download: %v", perms)
	}
	op := page(t, port, owner)
	token := regexp.MustCompile(`data-link="room/([0-9a-f]{64})"`).FindStringSubmatch(op)[1]
	roomID := regexp.MustCompile(`(?:&#34;|")room(?:&#34;|"):(?:&#34;|")([0-9a-f-]{36})`).FindStringSubmatch(op)[1]

	phone := browse(t, port, "visitor=1", look{w: 390, h: 800, touch: true})
	run(t, phone, chromedp.Navigate("http://localhost:"+port+"/room/"+token), chromedp.WaitVisible(`#room-pin`, chromedp.ByQuery))
	if strings.Contains(js[string](t, phone, `document.body.innerText`), "Chiara") {
		t.Error("the PIN page shows nothing of the room")
	}
	run(t, phone, chromedp.SendKeys(`#room-pin`, "0000", chromedp.ByQuery), chromedp.Click(`form button.primary`, chromedp.ByQuery), chromedp.WaitVisible(`.bad-note`, chromedp.ByQuery))
	run(t, phone, chromedp.SendKeys(`#room-pin`, "2468", chromedp.ByQuery), chromedp.Click(`form button.primary`, chromedp.ByQuery), chromedp.WaitVisible(`.room-grid`, chromedp.ByQuery))
	until(t, phone, "three marked pictures are shown", `document.querySelectorAll('.room-tile img').length === 3 && [...document.querySelectorAll('.room-tile img')].every(i => i.complete && i.naturalWidth > 0)`, 30*time.Second)
	if js[bool](t, phone, `document.documentElement.scrollWidth > innerWidth + 1`) {
		t.Error("the client's page must not scroll sideways on a phone")
	}
	if got := js[string](t, phone, `document.querySelector('h1').textContent`); got != "Chiara and Luca" {
		t.Errorf("the client's title: %q", got)
	}

	// the picture a client gets is Immich's preview with the mark on it, not the preview
	unlock := func() string {
		req, _ := http.NewRequest("POST", "http://localhost:"+port+"/room/"+token+"/unlock", strings.NewReader("pin=2468"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil || len(res.Cookies()) == 0 {
			t.Fatalf("unlock: %v", err)
		}
		res.Body.Close()
		return res.Cookies()[0].Name + "=" + res.Cookies()[0].Value
	}
	cookie := unlock()
	src := js[string](t, phone, `document.querySelector('.room-tile img').getAttribute('src')`)
	req, _ := http.NewRequest("GET", "http://localhost:"+port+"/"+src, nil)
	req.Header.Set("Cookie", cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	marked, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("the picture: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	markedImg, err := jpeg.Decode(bytes.NewReader(marked))
	if err != nil {
		t.Fatal(err)
	}
	same := 0
	for _, id := range photos {
		var plain bytes.Buffer
		r, _ := http.NewRequest("GET", baseURL+"/api/assets/"+id+"/thumbnail?size=preview", nil)
		r.Header.Set("Authorization", "Bearer "+alice.token)
		pr, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(&plain, pr.Body)
		pr.Body.Close()
		if pr.StatusCode != 200 {
			t.Fatalf("Immich's own preview: %d", pr.StatusCode)
		}
		if bytes.Equal(plain.Bytes(), marked) {
			t.Error("the plain preview went out")
		}
		if img, err := jpeg.Decode(&plain); err == nil && img.Bounds() == markedImg.Bounds() {
			same++
		}
	}
	if same == 0 {
		t.Error("the marked picture is one of the album's previews, at the same size")
	}

	// larger, one at a time
	run(t, phone, chromedp.Click(`.room-tile`, chromedp.ByQuery))
	until(t, phone, "the picture opens larger", `document.querySelector('dialog.room-lightbox').open && document.querySelector('.room-lightbox img').complete && document.querySelector('.room-lightbox img').naturalWidth > 0`, 10*time.Second)
	if got := js[string](t, phone, `document.querySelector('.room-caption').textContent`); got != "Photo 1 of 3" {
		t.Errorf("the caption: %q", got)
	}
	run(t, phone, chromedp.KeyEvent("'")) // ArrowRight is sent below through the page's own handler
	run(t, phone, chromedp.Evaluate(`document.querySelector('dialog.room-lightbox').dispatchEvent(new KeyboardEvent('keydown', {key: 'ArrowRight', bubbles: true}))`, nil))
	until(t, phone, "the next picture", `document.querySelector('.room-caption').textContent === 'Photo 2 of 3'`, 5*time.Second)

	// choosing: up to the limit, a refusal in words beyond it, and Done locks the choice
	run(t, phone, chromedp.Evaluate(`document.querySelector('dialog.room-lightbox').close()`, nil))
	heart := func(n int) string {
		return fmt.Sprintf(`.room-grid li:nth-child(%d) form.room-pick button`, n)
	}
	counter := `document.querySelector('[data-room-count]').textContent`
	run(t, phone, chromedp.Click(heart(1), chromedp.ByQuery))
	until(t, phone, "one chosen", counter+` === '1 of 2 chosen'`, 10*time.Second)
	run(t, phone, chromedp.Click(heart(2), chromedp.ByQuery))
	until(t, phone, "two chosen", counter+` === '2 of 2 chosen'`, 10*time.Second)
	run(t, phone, chromedp.Click(heart(3), chromedp.ByQuery))
	until(t, phone, "the limit is told in words", `!document.getElementById('room-msg').hidden && /most there can be/.test(document.getElementById('room-msg').textContent)`, 10*time.Second)
	if js[string](t, phone, counter) != "2 of 2 chosen" || !js[bool](t, phone, `document.querySelector('.room-grid li:nth-child(1)').classList.contains('picked') && !document.querySelector('.room-grid li:nth-child(3)').classList.contains('picked')`) {
		t.Errorf("a refused pick changes nothing: %q %v", js[string](t, phone, counter), js[[]string](t, phone, `[...document.querySelectorAll('.room-grid li')].map(li => li.className)`))
	}
	// the choice survives a reload (it is kept by the app, not by the page)
	run(t, phone, chromedp.Reload(), chromedp.WaitVisible(`.room-grid`, chromedp.ByQuery))
	if got := js[string](t, phone, counter); got != "2 of 2 chosen" {
		t.Errorf("after a reload: %q", got)
	}
	run(t, phone, chromedp.Click(`form.room-done button`, chromedp.ByQuery))
	until(t, phone, "the choice is sent", `/your choice has been sent/i.test(document.body.textContent) && document.querySelectorAll('form.room-pick').length === 0`, 15*time.Second)
	if js[int](t, phone, `document.querySelectorAll('li.picked').length`) != 2 {
		t.Error("the picks stay shown, read-only")
	}

	// the photographer sees it on the Overview and in the room's row, and can give the choice back
	op = page(t, port, owner)
	for _, want := range []string{"Choice sent", "finished choosing: 2 photos.", "2 photos chosen", "Let them change it"} {
		if !strings.Contains(op, want) {
			t.Errorf("the photographer's page lacks %q", want)
		}
	}
	if code, b := postBody(t, port, "/rooms/reopen", owner, url.Values{"room": {roomID}, "who": {""}}); code != 204 {
		t.Fatalf("give the choice back: %d %s", code, b)
	}
	run(t, phone, chromedp.Reload(), chromedp.WaitVisible(`.room-grid`, chromedp.ByQuery))
	if js[int](t, phone, `document.querySelectorAll('form.room-pick').length`) != 3 {
		t.Error("the client can choose again")
	}

	run(t, phone, chromedp.Click(`form.room-done button`, chromedp.ByQuery))
	until(t, phone, "the choice is sent again", `/your choice has been sent/i.test(document.body.textContent) && document.querySelectorAll('form.room-pick').length === 0`, 15*time.Second)

	// until the photographer releases, a client can take nothing; then each gets the originals of their own choice
	if c, _ := guestGetAs(t, cookie, port, "/room/"+token+"/zip"); c != 404 {
		t.Errorf("before the release: %d", c)
	}
	if c, b := postBody(t, port, "/rooms/release", owner, url.Values{"room": {roomID}, "days": {"7"}}); c != 204 {
		t.Fatalf("release: %d %s", c, b)
	}
	if !has(keyPermissions(t, alice, "handoff: room "+name), "asset.download") {
		t.Error("the photographer's session gave the key the download permission at the release")
	}
	run(t, phone, chromedp.Reload(), chromedp.WaitVisible(`.room-grid`, chromedp.ByQuery))
	if !js[bool](t, phone, `/your photos are ready/i.test(document.body.textContent) && document.querySelectorAll('a.room-dl').length === 2`) {
		t.Error("the client's page offers the ZIP and a download for each chosen photo")
	}
	zreq, _ := http.NewRequest("GET", "http://localhost:"+port+"/room/"+token+"/zip", nil)
	zreq.Header.Set("Cookie", cookie)
	zres, err := http.DefaultClient.Do(zreq)
	if err != nil {
		t.Fatal(err)
	}
	zdata, _ := io.ReadAll(zres.Body)
	zres.Body.Close()
	entries := zipEntries(t, zdata)
	if zres.StatusCode != 200 || len(entries) != 2 {
		t.Fatalf("the ZIP of the two chosen photos: %d %v", zres.StatusCode, entries)
	}
	want := map[int]bool{}
	for _, id := range photos {
		oreq, _ := http.NewRequest("GET", baseURL+"/api/assets/"+id+"/original", nil)
		oreq.Header.Set("Authorization", "Bearer "+alice.token)
		ores, err := http.DefaultClient.Do(oreq)
		if err != nil {
			t.Fatal(err)
		}
		ob, _ := io.ReadAll(ores.Body)
		ores.Body.Close()
		want[len(ob)] = true
	}
	for name, size := range entries {
		if !want[size] {
			t.Errorf("%s in the ZIP is %d bytes, which is not the size of an original of the album", name, size)
		}
	}
	postBody(t, port, "/rooms/withdraw", owner, url.Values{"room": {roomID}})
	if has(keyPermissions(t, alice, "handoff: room "+name), "asset.download") {
		t.Error("taking the release back takes the permission off")
	}
	if c, _ := guestGetAs(t, cookie, port, "/room/"+token+"/zip"); c != 404 {
		t.Errorf("after taking it back: %d", c)
	}

	// paused: the link says closed for now and gives no picture; resumed: it opens again; ended: gone, and the key with it
	if c, _ := postBody(t, port, "/rooms/pause", owner, url.Values{"room": {roomID}, "paused": {"1"}}); c != 204 {
		t.Fatalf("pause: %d", c)
	}
	if c, b := guestGetAs(t, cookie, port, "/room/"+token); c != 200 || !strings.Contains(b, "closed for now") {
		t.Errorf("paused: %d", c)
	}
	if c, _ := guestGetAs(t, cookie, port, "/"+src); c != 404 {
		t.Errorf("a paused room gives no picture: %d", c)
	}
	postBody(t, port, "/rooms/pause", owner, url.Values{"room": {roomID}, "paused": {"0"}})
	if c, _ := guestGetAs(t, cookie, port, "/"+src); c != 200 {
		t.Errorf("resumed: %d", c)
	}
	if c, _ := postBody(t, port, "/rooms/end", owner, url.Values{"room": {roomID}}); c != 204 {
		t.Fatalf("end: %d", c)
	}
	if c, _ := guestGetAs(t, cookie, port, "/room/"+token); c != 404 {
		t.Errorf("ended: %d", c)
	}
	if hasName(keyNames(t, alice), "handoff: room "+name) {
		t.Error("ending the room deletes its key")
	}
}
