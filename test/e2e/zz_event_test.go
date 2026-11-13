//go:build e2e

// Named to sort last, like zz_dropbox_test.go: it leaves a box, an album and photos in the shared Immich.
package e2e

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// formPost posts a multipart form (fields, and files by field name) as a person signed in.
func formPost(t *testing.T, port, path, cookie string, fields map[string]string, files map[string][]byte) int {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	for name, data := range files {
		fw, _ := mw.CreateFormFile(name, name+".png")
		fw.Write(data)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", "http://localhost:"+port+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-Requested-With", "immich-handoff")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

// guestSend sends a file to a drop box the way the guest page's script does: one request, a name field before the file.
func guestSend(t *testing.T, c *http.Client, port, token, name string, file []byte, filename string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("guest", name)
	fw, _ := mw.CreateFormFile("files", filename)
	fw.Write(file)
	mw.Close()
	req, _ := http.NewRequest("POST", "http://localhost:"+port+"/drop/"+token+"/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), `"ok":true`) {
		t.Fatalf("send %s: %d %s", filename, res.StatusCode, b)
	}
}

func zipEntries(t *testing.T, data []byte) map[string]int {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a ZIP: %v", err)
	}
	out := map[string]int{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = len(b)
	}
	return out
}

// TestEvent: a drop box as an event, against a real Immich and a real Chrome. The owner makes a box that approves files
// and has a gallery with downloads and names; a helper approves from the moderator link on a phone; guests look at the
// photos, heart one, download an original and a ZIP; the host hides one; the owner makes the album of loved photos and a
// ZIP; the host's title and logo show; and a colour that does not read is made to.
func TestEvent(t *testing.T) {
	alice, _, _, _ := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 66)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	owner := loginCookie(t, "alice@e2e.test")

	name := fmt.Sprintf("rossi-%d", time.Now().UnixNano()%1_000_000) // a name of its own, so a second run against the same Immich starts clean
	code, body := postBody(t, port, "/dropbox", owner, url.Values{"name": {name}, "opendays": {"5"}, "approve": {"1"},
		"gallery": {"1"}, "gallerydownload": {"1"}, "gallerynames": {"1"}, "keepdays": {"7"}})
	if code != 204 {
		t.Fatalf("make an event: %d %s", code, body)
	}
	perms := keyPermissions(t, alice, "handoff: "+name+" (drop box)")
	if !has(perms, "asset.view") || !has(perms, "asset.download") || has(perms, "asset.delete") {
		t.Fatalf("the key of a gallery with downloads may look and download, and nothing more: %v", perms)
	}
	op := page(t, port, owner)
	token := regexp.MustCompile(`data-link="drop/([0-9a-f]+)"`).FindStringSubmatch(op)[1]
	boxID := regexp.MustCompile(`(?:&#34;|")service(?:&#34;|"):(?:&#34;|")([0-9a-f-]{36})`).FindStringSubmatch(op)[1]
	if code, b := postBody(t, port, "/dropbox/moderator", owner, url.Values{"service": {boxID}, "on": {"1"}}); code != 204 {
		t.Fatalf("moderator link: %d %s", code, b)
	}
	mod := regexp.MustCompile(`data-link="mod/([0-9a-f]+)"`).FindStringSubmatch(page(t, port, owner))[1]
	inbox := func() []string { return searchTag(t, alice, "inbox/"+name, false) }

	// guests send three photos, with names; nothing reaches the library, and the gallery shows nothing, until approved
	guest := &http.Client{}
	seed := uint32(time.Now().UnixNano()) // photos of their own, so a second run against the same Immich does not find them already there
	photos := map[string][]byte{"ann.jpg": noiseJPEG(seed, 40, 220), "eve.jpg": noiseJPEG(seed+1, 40, 220), "zoe.jpg": noiseJPEG(seed+2, 40, 220)}
	for name, data := range photos {
		guestSend(t, guest, port, token, strings.TrimSuffix(name, ".jpg"), data, name)
	}
	if n := len(inbox()); n != 0 {
		t.Fatalf("a box that approves keeps nothing until approved: %d", n)
	}
	if c, b := guestGet(t, port, "/drop/"+token+"/gallery"); c != 200 || strings.Contains(b, `"id":`) {
		t.Fatalf("the gallery before approval: %d %s", c, b)
	}

	// the helper, on a phone, with no account: chooses all and approves
	phone := browse(t, port, "visitor=1", look{w: 390, h: 800, touch: true})
	run(t, phone, chromedp.Navigate("http://localhost:"+port+"/mod/"+mod), chromedp.WaitVisible(`form.mod-form`, chromedp.ByQuery))
	until(t, phone, "three files wait, with pictures", `document.querySelectorAll('.mod-form .thumbs li').length === 3 && [...document.querySelectorAll('.mod-form img')].every(i => i.complete && i.naturalWidth > 0)`, 20*time.Second)
	if js[bool](t, phone, `document.documentElement.scrollWidth > innerWidth + 1`) {
		t.Error("the moderator's page must not scroll sideways on a phone")
	}
	run(t, phone, chromedp.Click(`button.select-all`, chromedp.ByQuery))
	until(t, phone, "all are chosen", `[...document.querySelectorAll('.mod-form input[name=item]')].every(i => i.checked)`, 5*time.Second)
	run(t, phone, chromedp.Click(`.mod-form button.primary`, chromedp.ByQuery))
	until(t, phone, "the page says how many were approved", `document.body.textContent.includes('3 files approved.')`, 60*time.Second)
	eventually(t, "the three photos are in the library", 90*time.Second, func() bool { return len(inbox()) == 3 })

	// Immich makes the pictures a little later
	ids := map[int64]bool{}
	for _, m := range regexp.MustCompile(`"id":(\d+)`).FindAllStringSubmatch(func() string { _, b := guestGet(t, port, "/drop/"+token+"/gallery"); return b }(), -1) {
		var id int64
		fmt.Sscan(m[1], &id)
		ids[id] = true
	}
	if len(ids) != 3 {
		t.Fatalf("the gallery shows %d photos after approval, want 3", len(ids))
	}
	for id := range ids {
		eventually(t, "Immich has made the picture", 90*time.Second, func() bool {
			res, err := http.Get(fmt.Sprintf("http://localhost:%s/drop/%s/photo/%d", port, token, id))
			if err != nil {
				return false
			}
			defer res.Body.Close()
			b, _ := io.ReadAll(res.Body)
			return res.StatusCode == 200 && strings.HasPrefix(res.Header.Get("Content-Type"), "image/") && len(b) > 500
		})
	}

	// a guest's phone: the gallery fills in, with pictures and names, and a heart can be given and kept
	gp := browse(t, port, "visitor=1", look{w: 390, h: 800, touch: true})
	run(t, gp, chromedp.Navigate("http://localhost:"+port+"/drop/"+token), chromedp.WaitVisible(`section.gallery`, chromedp.ByQuery))
	until(t, gp, "three tiles with pictures", `document.querySelectorAll('.gal-item').length === 3 && [...document.querySelectorAll('.gal-item img')].every(i => i.complete && i.naturalWidth > 0)`, 60*time.Second)
	until(t, gp, "the names are shown", `['ann','eve','zoe'].every(n => [...document.querySelectorAll('.gal-by')].some(e => e.textContent.trim() === 'by ' + n))`, 5*time.Second)
	if js[bool](t, gp, `document.documentElement.scrollWidth > innerWidth + 1`) {
		t.Error("the gallery must not scroll sideways on a phone")
	}
	if js[int](t, gp, `getComputedStyle(document.querySelector('ul.gal')).gridTemplateColumns.split(' ').length`) != 2 {
		t.Error("a phone shows the gallery two photos wide")
	}
	if !js[bool](t, gp, `!!document.querySelector('.gal-save[download]') && !!document.querySelector('.gallery-zip a[download]')`) {
		t.Error("downloads are offered")
	}
	run(t, gp, chromedp.Click(`.gal-item .gal-heart`, chromedp.ByQuery))
	until(t, gp, "the heart is given", `document.querySelector('.gal-item .gal-heart').getAttribute('aria-pressed') === 'true' && document.querySelector('.gal-item .gal-count').textContent === '1'`, 10*time.Second)
	run(t, gp, chromedp.Navigate("http://localhost:"+port+"/drop/"+token), chromedp.WaitVisible(`section.gallery`, chromedp.ByQuery))
	until(t, gp, "the heart is still there after a reload (the guest's secret is a cookie)", `document.querySelectorAll('.gal-heart[aria-pressed=true]').length === 1`, 20*time.Second)
	run(t, gp, chromedp.Click(`.gal-item .gal-open`, chromedp.ByQuery))
	until(t, gp, "a photo opens large", `document.querySelector('dialog.gal-lightbox').open && document.querySelector('dialog.gal-lightbox img').naturalWidth > 0`, 10*time.Second)

	// originals, one at a time and all in a ZIP, byte for byte what the guests sent
	sizes := map[int]bool{}
	for _, d := range photos {
		sizes[len(d)] = true
	}
	for id := range ids {
		res, err := http.Get(fmt.Sprintf("http://localhost:%s/drop/%s/original/%d", port, token, id))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Disposition"), "attachment") || !sizes[len(b)] {
			t.Errorf("original %d: %d, %d bytes, %q", id, res.StatusCode, len(b), res.Header.Get("Content-Disposition"))
		}
	}
	_, zipBody := guestGet(t, port, "/drop/"+token+"/zip")
	if entries := zipEntries(t, []byte(zipBody)); len(entries) != 3 {
		t.Errorf("the guests' ZIP: %v", entries)
	} else {
		for name, n := range entries {
			if !sizes[n] {
				t.Errorf("%s in the ZIP is %d bytes, not what was sent", name, n)
			}
		}
	}
	req, _ := http.NewRequest("GET", "http://localhost:"+port+"/dropbox/zip/"+boxID, nil)
	req.Header.Set("Cookie", owner)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	ownerZip, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || len(zipEntries(t, ownerZip)) != 3 {
		t.Errorf("the owner's ZIP: %d, %v", res.StatusCode, zipEntries(t, ownerZip))
	}

	// the host hides one photo from the moderator page: guests lose it, the owner keeps it
	run(t, phone, chromedp.Navigate("http://localhost:"+port+"/mod/"+mod), chromedp.WaitVisible(`.mod-photos`, chromedp.ByQuery))
	until(t, phone, "the photos kept are shown with pictures", `document.querySelectorAll('.mod-photo').length === 3 && [...document.querySelectorAll('.mod-photo img')].every(i => i.complete && i.naturalWidth > 0)`, 30*time.Second)
	run(t, phone, chromedp.Click(`.mod-photo:nth-child(2) button[value=hide]`, chromedp.ByQuery))
	until(t, phone, "it is marked hidden", `document.querySelectorAll('.mod-photo.is-hidden').length === 1`, 20*time.Second)
	if c, b := guestGet(t, port, "/drop/"+token+"/gallery"); c != 200 || strings.Count(b, `"id":`) != 2 {
		t.Errorf("guests see %d photos after one is hidden: %s", strings.Count(b, `"id":`), b)
	}
	if n := len(inbox()); n != 3 {
		t.Errorf("hiding takes nothing out of the library: %d", n)
	}

	// the album of loved photos: the hearted one (unless it is the hidden one, which is never loved)
	if code, b := postBody(t, port, "/dropbox/loved", owner, url.Values{"service": {boxID}}); code != 204 {
		t.Fatalf("loved album: %d %s", code, b)
	}
	var albums []struct {
		AlbumName  string `json:"albumName"`
		AssetCount int    `json:"assetCount"`
	}
	call(t, "GET", "/albums", keyAuth(alice), nil, &albums)
	loved := -1
	for _, al := range albums {
		if al.AlbumName == name+" loved by guests" {
			loved = al.AssetCount
		}
	}
	if loved != 1 {
		t.Errorf("the loved album holds %d photos, want the one that was hearted", loved)
	}

	// the host's own look: a title, a logo, and a colour that does not read on a light page
	logo := image.NewRGBA(image.Rect(0, 0, 120, 60))
	for x := 0; x < 120; x++ {
		for y := 0; y < 60; y++ {
			logo.Set(x, y, color.RGBA{200, 30, 90, 255})
		}
	}
	var logoPNG bytes.Buffer
	png.Encode(&logoPNG, logo)
	if code := formPost(t, port, "/dropbox/look", owner, map[string]string{"service": boxID, "accent": "#ffe066", "title": "Anna & Marco's wedding"}, map[string][]byte{"logo": logoPNG.Bytes()}); code != 204 {
		t.Fatalf("title and logo: %d", code)
	}
	lp := browse(t, port, "visitor=1", look{w: 1280, h: 900})
	run(t, lp, chromedp.Navigate("http://localhost:"+port+"/drop/"+token), chromedp.WaitVisible(`form.drop-form`, chromedp.ByQuery))
	until(t, lp, "the logo is shown", `(() => { const i = document.querySelector('img.drop-logo'); return !!i && i.complete && i.naturalWidth === 120; })()`, 20*time.Second)
	if h := js[string](t, lp, `document.querySelector('h1').textContent`); h != "Anna & Marco's wedding" {
		t.Errorf("the title guests see: %q", h)
	}
	if js[bool](t, lp, fmt.Sprintf("document.body.textContent.includes(%q)", name)) {
		t.Error("the box's internal name must not show to guests")
	}
	// a link in the pale yellow would be unreadable: the ink is darker, and the button's text is dark
	ink := js[float64](t, lp, `(() => {
	  const c = getComputedStyle(document.querySelector('a, .gal-save') || document.body).color.match(/\d+/g).map(Number);
	  const l = v => { v /= 255; return v <= .03928 ? v / 12.92 : Math.pow((v + .055) / 1.055, 2.4); };
	  const lum = 0.2126 * l(c[0]) + 0.7152 * l(c[1]) + 0.0722 * l(c[2]);
	  const bg = getComputedStyle(document.body).backgroundColor.match(/\d+/g).map(Number);
	  const lb = 0.2126 * l(bg[0]) + 0.7152 * l(bg[1]) + 0.0722 * l(bg[2]);
	  return (Math.max(lum, lb) + .05) / (Math.min(lum, lb) + .05);
	})()`)
	if ink < 4.5 {
		t.Errorf("text in the host's colour reads at %.2f to 1 on the page, want 4.5", ink)
	}
	if on := js[string](t, lp, `getComputedStyle(document.documentElement).getPropertyValue('--on-accent').trim()`); on != "#111111" {
		t.Errorf("text on a pale yellow is dark: %q", on)
	}
}
