//go:build e2e

// This file is named to sort last: go test runs the files in name order, and these tests leave services, workflows and
// photos behind in the shared Immich that the older tests (which count what is there) must not see.
package e2e

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// guestUpload sends files to a drop box's link the way a browser form does, with no cookie at all.
func guestUpload(t *testing.T, port, token string, files map[string][]byte) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, data := range files {
		fw, _ := mw.CreateFormFile("files", name)
		fw.Write(data)
	}
	mw.Close()
	res, err := http.Post("http://localhost:"+port+"/drop/"+token+"/upload", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func guestGet(t *testing.T, port, path string) (int, string) {
	t.Helper()
	res, err := http.Get("http://localhost:" + port + path)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// TestDropBox: a person makes a drop box, a guest with no account sends photos through its link and they arrive in the
// person's library under the box's tag; the key can only store and tag; a page that is not a photo is turned away; a
// paused, and then a revoked, link takes nothing and looks like a link that never was.
func TestDropBox(t *testing.T) {
	alice, _, _, _ := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 50)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	owner := loginCookie(t, "alice@e2e.test")

	code, body := postBody(t, port, "/dropbox", owner, url.Values{"name": {"Wedding"}, "message": {"Send us your photos"}, "opendays": {"7"}, "filemb": {"50"}})
	if code != 204 {
		t.Fatalf("make a drop box: %d %s", code, body)
	}
	perms := keyPermissions(t, alice, "handoff: Wedding (drop box)")
	if len(perms) != 5 || !has(perms, "asset.upload") || has(perms, "asset.read") || has(perms, "asset.delete") || has(perms, "album.read") {
		t.Errorf("the drop box key may only store and tag photos, got %v", perms)
	}
	m := regexp.MustCompile(`data-link="drop/([0-9a-f]+)"`).FindStringSubmatch(page(t, port, owner))
	if m == nil {
		t.Fatal("the owner's page must show the link")
	}
	token := m[1]

	// a guest needs no login, and sees the name and message but nothing of the library
	if code, p := guestGet(t, port, "/drop/"+token); code != 200 || !strings.Contains(p, "Wedding") || !strings.Contains(p, "Send us your photos") {
		t.Fatalf("guest page: %d %.200s", code, p)
	}
	inbox := func() []string { return searchTag(t, alice, "inbox/Wedding", false) }

	code, p := guestUpload(t, port, token, map[string][]byte{"one.jpg": jpegBytes(7001), "two.jpg": jpegBytes(7002), "evil.jpg": []byte("<script>alert(1)</script>")})
	if code != 200 || !strings.Contains(p, "Received") || !strings.Contains(p, "Not a photo or video this box takes") {
		t.Fatalf("guest upload: %d %s", code, p)
	}
	eventually(t, "both photos are in the owner's library under the box's tag", 60*time.Second, func() bool { return len(inbox()) == 2 })

	// the same photo again is not stored twice
	guestUpload(t, port, token, map[string][]byte{"one-again.jpg": jpegBytes(7001)})
	time.Sleep(3 * time.Second)
	if n := len(inbox()); n != 2 {
		t.Errorf("a duplicate must not add a photo: %d in the inbox", n)
	}

	// the owner sees what happened, with a mark for the guest and no address
	if op := page(t, port, owner); !strings.Contains(op, "2 files") || !strings.Contains(op, "was not taken") || strings.Contains(op, "127.0.0.1") {
		t.Errorf("the owner's log: %.400s", op)
	}

	// pause: the link looks like any link that does not exist
	id := regexp.MustCompile(`(?:&#34;|")service(?:&#34;|"):(?:&#34;|")([0-9a-f-]{36})`).FindStringSubmatch(page(t, port, owner))
	if id == nil {
		t.Fatal("no drop box id on the page")
	}
	boxID := id[1]
	if code, _ := postBody(t, port, "/dropbox/pause", owner, url.Values{"service": {boxID}, "paused": {"1"}}); code != 204 {
		t.Fatalf("pause: %d", code)
	}
	pausedCode, pausedPage := guestGet(t, port, "/drop/"+token)
	unknownCode, unknownPage := guestGet(t, port, "/drop/"+strings.Repeat("0", 32))
	if pausedCode != 404 || pausedCode != unknownCode || pausedPage != unknownPage {
		t.Errorf("a paused link must look like an unknown one: %d vs %d", pausedCode, unknownCode)
	}
	if code, _ := guestUpload(t, port, token, map[string][]byte{"late.jpg": jpegBytes(7003)}); code != 404 {
		t.Errorf("upload to a paused box: %d", code)
	}
	postBody(t, port, "/dropbox/pause", owner, url.Values{"service": {boxID}, "paused": {"0"}})
	if code, _ := guestUpload(t, port, token, map[string][]byte{"three.jpg": jpegBytes(7004)}); code != 200 {
		t.Errorf("upload after resuming: %d", code)
	}
	eventually(t, "the photo sent after resuming arrives", 60*time.Second, func() bool { return len(inbox()) == 3 })

	// revoke: the key goes from the owner's account, the link stops, the photos stay
	if code, b := postBody(t, port, "/dropbox/revoke", owner, url.Values{"service": {boxID}}); code != 204 {
		t.Fatalf("revoke: %d %s", code, b)
	}
	if hasName(keyNames(t, alice), "handoff: Wedding (drop box)") {
		t.Error("revoking must delete the key from the owner's account")
	}
	if code, _ := guestGet(t, port, "/drop/"+token); code != 404 {
		t.Errorf("a revoked link: %d", code)
	}
	if n := len(inbox()); n != 3 {
		t.Errorf("revoking must leave the received photos in the library: %d", n)
	}
}

// TestDropBoxTools: a box that files into an album and a tag of its own and holds files for approval; a co-host who may
// only look, then one who manages; extending; all with a real Immich.
func TestDropBoxTools(t *testing.T) {
	alice, _, _, _ := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 51)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	owner, helper := loginCookie(t, "alice@e2e.test"), loginCookie(t, "bob@e2e.test")

	closes := time.Now().Add(72 * time.Hour).Format("2006-01-02T15:04")
	code, body := postBody(t, port, "/dropbox", owner, url.Values{"name": {"Gala"}, "album": {"Gala photos"}, "tag": {"gala"}, "approve": {"1"}, "closes": {closes}})
	if code != 204 {
		t.Fatalf("make a drop box: %d %s", code, body)
	}
	perms := keyPermissions(t, alice, "handoff: Gala (drop box)")
	if !has(perms, "album.create") || !has(perms, "albumAsset.create") || has(perms, "asset.read") || has(perms, "asset.delete") {
		t.Errorf("a box with an album: %v", perms)
	}
	op := page(t, port, owner)
	token := regexp.MustCompile(`data-link="drop/([0-9a-f]+)"`).FindStringSubmatch(op)[1]
	boxID := regexp.MustCompile(`(?:&#34;|")service(?:&#34;|"):(?:&#34;|")([0-9a-f-]{36})`).FindStringSubmatch(op)[1]
	inbox := func() []string { return searchTag(t, alice, "inbox/Gala", false) }

	// two files wait; nothing is in the library
	code, p := guestUpload(t, port, token, map[string][]byte{"keep.jpg": jpegBytes(7101), "drop.jpg": jpegBytes(7102)})
	if code != 200 || strings.Count(p, "waiting for approval") != 2 {
		t.Fatalf("guest upload: %d %s", code, p)
	}
	time.Sleep(2 * time.Second)
	if n := len(inbox()); n != 0 {
		t.Fatalf("nothing may be stored before approval: %d", n)
	}
	op = page(t, port, owner)
	ids := regexp.MustCompile(`src="dropbox/pending/([0-9a-f-]{36})" alt="([a-z]+\.jpg)"`).FindAllStringSubmatch(op, -1)
	if len(ids) != 2 || !strings.Contains(op, "Waiting for approval") {
		t.Fatalf("the owner must see both files waiting: %.300s", op)
	}
	idOf := map[string]string{}
	for _, m := range ids {
		idOf[m[2]] = m[1]
	}
	req, _ := http.NewRequest("GET", "http://localhost:"+port+"/dropbox/pending/"+idOf["keep.jpg"], nil)
	req.Header.Set("Cookie", owner)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "image/jpeg") {
		t.Errorf("the owner opens a waiting file: %v %v", err, res)
	}

	// bob is made a co-host who only looks: he sees the box and that files wait, but cannot decide
	if code, b := postBody(t, port, "/dropbox/host", owner, url.Values{"service": {boxID}, "email": {"bob@e2e.test"}, "role": {"view"}}); code != 204 {
		t.Fatalf("add a co-host: %d %s", code, b)
	}
	if code, _ := postBody(t, port, "/dropbox/host", owner, url.Values{"service": {boxID}, "email": {"nobody@e2e.test"}, "role": {"view"}}); code != 409 {
		t.Errorf("an email nobody has: %d, want 409", code)
	}
	bp := page(t, port, helper)
	if !strings.Contains(bp, "Gala") || !strings.Contains(bp, "You can see this drop box") || strings.Contains(bp, "dropbox/pending/") || strings.Contains(bp, `data-link="drop/`) {
		t.Errorf("what a viewer sees: %.400s", bp)
	}
	if code, _ := postBody(t, port, "/dropbox/approve", helper, url.Values{"service": {boxID}, "all": {"1"}}); code != 409 {
		t.Errorf("a viewer approving: %d, want 409", code)
	}
	// ...then to a co-host who manages: he approves one and rejects the other
	postBody(t, port, "/dropbox/host", owner, url.Values{"service": {boxID}, "email": {"bob@e2e.test"}, "role": {"manage"}})
	if code, b := postBody(t, port, "/dropbox/approve", helper, url.Values{"service": {boxID}, "item": {idOf["keep.jpg"]}}); code != 204 {
		t.Fatalf("approve: %d %s", code, b)
	}
	if code, b := postBody(t, port, "/dropbox/reject", helper, url.Values{"service": {boxID}, "item": {idOf["drop.jpg"]}}); code != 204 {
		t.Fatalf("reject: %d %s", code, b)
	}
	eventually(t, "the approved photo is in the library, and only it", 60*time.Second, func() bool { return len(inbox()) == 1 })
	id := inbox()[0]
	// the box's tag, the extra tag and the album are put on one after another, so wait for all of them
	var tags string
	eventually(t, "the photo carries both tags", 60*time.Second, func() bool {
		tags = tagNames(getFull(t, alice, id))
		return strings.Contains(tags, "Gala") && strings.Contains(tags, "gala")
	})
	var albums []struct {
		AlbumName  string `json:"albumName"`
		AssetCount int    `json:"assetCount"`
	}
	eventually(t, "the album holds the photo", 60*time.Second, func() bool {
		albums = nil
		call(t, "GET", "/albums", keyAuth(alice), nil, &albums)
		for _, al := range albums {
			if al.AlbumName == "Gala photos" && al.AssetCount == 1 {
				return true
			}
		}
		return false
	})
	if strings.Contains(page(t, port, owner), "dropbox/pending/") {
		t.Error("the queue must be empty")
	}

	// the manager may pause and extend, not end it; a co-host may stop helping
	if code, _ := postBody(t, port, "/dropbox/pause", helper, url.Values{"service": {boxID}, "paused": {"1"}}); code != 204 {
		t.Errorf("a manager pausing: %d", code)
	}
	if code, _ := guestGet(t, port, "/drop/"+token); code != 404 {
		t.Errorf("a paused box: %d", code)
	}
	postBody(t, port, "/dropbox/pause", helper, url.Values{"service": {boxID}, "paused": {"0"}})
	if code, _ := postBody(t, port, "/dropbox/extend", helper, url.Values{"service": {boxID}, "days": {"7"}}); code != 204 {
		t.Errorf("a manager extending: %d", code)
	}
	if code, _ := postBody(t, port, "/dropbox/revoke", helper, url.Values{"service": {boxID}}); code != 409 {
		t.Errorf("a manager revoking: %d, want 409", code)
	}
	if code, _ := postBody(t, port, "/dropbox/host/remove", helper, url.Values{"service": {boxID}}); code != 204 {
		t.Errorf("a co-host stopping: %d", code)
	}
	if strings.Contains(page(t, port, helper), "Gala") {
		t.Error("after stopping, the box is gone from the co-host's page")
	}
	if !strings.Contains(page(t, port, owner), "The link stops working") {
		t.Error("the owner's page must say when the link stops working")
	}
}

// bigJPEG is a photo wider than any size a drop box shrinks to.
func bigJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), uint8((x + y) % 256), 255})
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 90})
	return b.Bytes()
}

// TestDropBoxGuests: the short link, a printable card, a new long link, templates and copies, and a guest taking back
// what they sent while it is in the inbox, all with a real Immich and the permissions the box's key really has.
func TestDropBoxGuests(t *testing.T) {
	alice, _, _, _ := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 52)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	owner := loginCookie(t, "alice@e2e.test")

	code, body := postBody(t, port, "/dropbox", owner, url.Values{"name": {"Undo"}, "guestdelete": {"1"}, "opendays": {"5"}})
	if code != 204 {
		t.Fatalf("make a drop box: %d %s", code, body)
	}
	perms := keyPermissions(t, alice, "handoff: Undo (drop box)")
	if !has(perms, "asset.read") || !has(perms, "asset.delete") || !has(perms, "asset.upload") || has(perms, "album.read") {
		t.Errorf("a box that lets guests take back what they sent: %v", perms)
	}
	op := page(t, port, owner)
	long := regexp.MustCompile(`data-link="drop/([0-9a-f]+)"`).FindStringSubmatch(op)[1]
	boxID := regexp.MustCompile(`(?:&#34;|")service(?:&#34;|"):(?:&#34;|")([0-9a-f-]{36})`).FindStringSubmatch(op)[1]
	if code, _ := postBody(t, port, "/dropbox/short", owner, url.Values{"service": {boxID}, "on": {"1"}}); code != 204 {
		t.Fatalf("short link: %d", code)
	}
	short := regexp.MustCompile(`data-link="d/([a-z0-9]{10})"`).FindStringSubmatch(page(t, port, owner))[1]
	inbox := func() []string { return searchTag(t, alice, "inbox/Undo", false) }

	// a guest uses the short link, with a browser that keeps its cookies
	jar, _ := cookiejar.New(nil)
	guest := &http.Client{Jar: jar}
	sendAs := func(path string, name string, data []byte) string {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("files", name)
		fw.Write(data)
		mw.Close()
		req, _ := http.NewRequest("POST", "http://localhost:"+port+path, &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Accept", "application/json")
		res, err := guest.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	for i, n := range []string{"one.jpg", "two.jpg"} {
		if r := sendAs("/d/"+short+"/upload", n, jpegBytes(7200+i)); !strings.Contains(r, `"ok":true`) {
			t.Fatalf("upload %s: %s", n, r)
		}
	}
	eventually(t, "both photos arrive", 60*time.Second, func() bool { return len(inbox()) == 2 })
	mine := func() string {
		res, err := guest.Get("http://localhost:" + port + "/d/" + short + "/mine")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	ids := regexp.MustCompile(`name="id" value="([0-9a-f-]{36})"`).FindAllStringSubmatch(mine(), -1)
	if len(ids) != 2 {
		t.Fatalf("the guest must see both files: %v", ids)
	}
	if stranger, _ := http.Get("http://localhost:" + port + "/d/" + short + "/mine"); stranger != nil {
		b, _ := io.ReadAll(stranger.Body)
		stranger.Body.Close()
		if strings.Contains(string(b), "one.jpg") {
			t.Error("a browser that did not send them must not see them")
		}
	}
	takeBack := func(id string) string {
		noFollow := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		res, err := noFollow.PostForm("http://localhost:"+port+"/d/"+short+"/mine/delete", url.Values{"id": {id}})
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.Header.Get("Location")
	}
	// taking one back puts it in the owner's trash
	before := inbox()
	if loc := takeBack(ids[0][1]); !strings.Contains(loc, "deleted") {
		t.Fatalf("taking back: %q", loc)
	}
	eventually(t, "the photo is in the owner's trash", 30*time.Second, func() bool { return len(inbox()) == 1 })
	gone := ""
	for _, id := range before {
		if _, c := getAsset(t, alice, id); c == 200 {
			if a, _ := getAsset(t, alice, id); a.IsTrashed {
				gone = id
			}
		}
	}
	if gone == "" {
		t.Error("the photo the guest took back must be in the trash, not deleted for good")
	}
	// once the owner takes the inbox tag off, the other photo is theirs
	var tags []struct{ ID, Name string }
	call(t, "GET", "/tags", keyAuth(alice), nil, &tags)
	for _, tg := range tags {
		if tg.Name == "Undo" {
			call(t, "DELETE", "/tags/"+tg.ID+"/assets", keyAuth(alice), map[string]any{"ids": inbox()}, nil)
		}
	}
	if loc := takeBack(ids[1][1]); !strings.Contains(loc, "kept") {
		t.Errorf("a photo the owner has filed: %q", loc)
	}
	for _, id := range before {
		if id != gone {
			if a, _ := getAsset(t, alice, id); a.IsTrashed {
				t.Error("a filed photo must stay out of the trash")
			}
		}
	}

	// the card, and ending each way in on its own
	if code, p := guestGetAs(t, owner, port, "/dropbox/card/"+boxID); code != 200 || !strings.Contains(p, `data-card-link="drop/`+long+`"`) {
		t.Errorf("card: %d", code)
	}
	if code, _ := postBody(t, port, "/dropbox/renew", owner, url.Values{"service": {boxID}}); code != 204 {
		t.Fatal("renew")
	}
	if code, _ := guestGet(t, port, "/drop/"+long); code != 404 {
		t.Errorf("the old long link: %d", code)
	}
	if code, _ := guestGet(t, port, "/d/"+short); code != 200 {
		t.Errorf("the short link must keep working: %d", code)
	}
	postBody(t, port, "/dropbox/short", owner, url.Values{"service": {boxID}, "on": {"0"}})
	if code, _ := guestGet(t, port, "/d/"+short); code != 404 {
		t.Errorf("the removed short link: %d", code)
	}

	// templates and copies make new boxes with real keys
	if code, b := postBody(t, port, "/dropbox/template", owner, url.Values{"service": {boxID}, "name": {"My template"}}); code != 204 {
		t.Fatalf("save a template: %d %s", code, b)
	}
	tid := regexp.MustCompile(`value="([0-9a-f-]{36})"\s*/?>\s*<b>My template</b>`).FindStringSubmatch(page(t, port, owner))
	if tid == nil {
		t.Fatal("the template must be listed")
	}
	if code, b := postBody(t, port, "/dropbox/fromtemplate", owner, url.Values{"service": {tid[1]}, "name": {"From template"}}); code != 204 {
		t.Fatalf("from a template: %d %s", code, b)
	}
	if code, b := postBody(t, port, "/dropbox/fromtemplate", owner, url.Values{"service": {"builtin:wedding"}, "name": {"Built in"}}); code != 204 {
		t.Fatalf("from a built-in template: %d %s", code, b)
	}
	if code, b := postBody(t, port, "/dropbox/duplicate", owner, url.Values{"service": {boxID}, "name": {"A copy"}}); code != 204 {
		t.Fatalf("copy: %d %s", code, b)
	}
	if !has(keyPermissions(t, alice, "handoff: From template (drop box)"), "asset.delete") {
		t.Error("a box made from a template that lets guests take back must have the key for it")
	}
	if p := keyPermissions(t, alice, "handoff: Built in (drop box)"); len(p) != 5 {
		t.Errorf("a plain template makes a plain key: %v", p)
	}
	keyPermissions(t, alice, "handoff: A copy (drop box)")
}

func guestGetAs(t *testing.T, cookie, port, path string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://localhost:"+port+path, nil)
	req.Header.Set("Cookie", cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// TestDropBoxGuestPage drives the guest page in a real browser, on a phone: it takes several photos, shows a progress
// bar, shrinks the big one when the owner asked for it, remembers what was sent so that it is not sent twice, and
// shows the box's own colour.
func TestDropBoxGuestPage(t *testing.T) {
	alice, _, _, _ := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 53)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	owner := loginCookie(t, "alice@e2e.test")
	if code, b := postBody(t, port, "/dropbox", owner, url.Values{"name": {"Phone party"}, "message": {"Send your photos"}, "opendays": {"3"}}); code != 204 {
		t.Fatalf("make a drop box: %d %s", code, b)
	}
	op := page(t, port, owner)
	token := regexp.MustCompile(`data-link="drop/([0-9a-f]+)"`).FindStringSubmatch(op)[1]
	boxID := regexp.MustCompile(`(?:&#34;|")service(?:&#34;|"):(?:&#34;|")([0-9a-f-]{36})`).FindStringSubmatch(op)[1]
	if code, b := postBody(t, port, "/dropbox/look", owner, url.Values{"service": {boxID}, "accent": {"#c2185b"}, "maxdim": {"1600"}}); code != 204 {
		t.Fatalf("look: %d %s", code, b)
	}

	dir := t.TempDir()
	small, big := filepath.Join(dir, "small.jpg"), filepath.Join(dir, "big.jpg")
	os.WriteFile(small, jpegBytes(7301), 0o644)
	bigBytes := bigJPEG(3200, 2000)
	os.WriteFile(big, bigBytes, 0o644)

	ctx := browse(t, port, "visitor=1", look{w: 390, h: 800, touch: true})
	guestURL := "http://localhost:" + port + "/drop/" + token
	run(t, ctx, chromedp.Navigate(guestURL), chromedp.WaitVisible(`form.drop-form`, chromedp.ByQuery))
	// the box's colour is applied, and the page can be installed under the box's name
	until(t, ctx, "the box's colour", `getComputedStyle(document.documentElement).getPropertyValue('--accent').trim() === '#c2185b'`, 10*time.Second)
	if m := js[string](t, ctx, `document.querySelector('link[rel=manifest]').getAttribute('href')`); m != "drop/"+token+"/manifest.webmanifest" {
		t.Errorf("manifest link: %q", m)
	}
	if !js[bool](t, ctx, `document.querySelector('input[name=camera]').getAttribute('capture') === 'environment' && getComputedStyle(document.querySelector('.camera-only')).display !== 'none'`) {
		t.Error("a phone must be offered the camera")
	}
	run(t, ctx, chromedp.SetUploadFiles(`#drop-files`, []string{small, big}, chromedp.ByQuery))
	until(t, ctx, "both files are listed", `document.querySelectorAll('.drop-list li').length === 2`, 10*time.Second)
	run(t, ctx, chromedp.Click(`form.drop-form button[type=submit]`, chromedp.ByQuery))
	until(t, ctx, "both files are received", `[...document.querySelectorAll('.drop-list li')].every(li => li.textContent.includes('Received'))`, 60*time.Second)

	inbox := func() []string { return searchTag(t, alice, "inbox/Phone party", false) }
	eventually(t, "both photos are in the owner's library", 60*time.Second, func() bool { return len(inbox()) == 2 })
	// the big one was shrunk in the browser: it is narrower than what was chosen, and the small one is untouched
	narrow := false
	eventually(t, "Immich reads their sizes", 60*time.Second, func() bool {
		widths := map[int]bool{}
		for _, id := range inbox() {
			if w := getFull(t, alice, id).Width; w != nil {
				widths[*w] = true
			}
		}
		narrow = widths[1600] && widths[64]
		return len(widths) == 2
	})
	if !narrow {
		t.Error("the big photo must be shrunk to 1600 pixels and the small one left alone")
	}

	// reloading and choosing the same files again does not send them again
	run(t, ctx, chromedp.Navigate(guestURL), chromedp.WaitVisible(`form.drop-form`, chromedp.ByQuery),
		chromedp.SetUploadFiles(`#drop-files`, []string{small}, chromedp.ByQuery))
	until(t, ctx, "the file is known as sent", `document.querySelector('.drop-list li')?.textContent.includes('Sent already')`, 10*time.Second)
	run(t, ctx, chromedp.Click(`form.drop-form button[type=submit]`, chromedp.ByQuery))
	time.Sleep(2 * time.Second)
	if n := len(inbox()); n != 2 {
		t.Errorf("a file sent before must not be sent again: %d photos", n)
	}
	_ = bigBytes
}

// TestDropBoxExtras: a shot list that tags photos by prompt, a guest book with a voice note, and sharing the result back
// with guests through a real Immich shared link that anyone can open without an account.
func TestDropBoxExtras(t *testing.T) {
	alice, _, _, _ := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 54)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	owner := loginCookie(t, "alice@e2e.test")

	code, body := postBody(t, port, "/dropbox", owner, url.Values{"name": {"Hunt"}, "opendays": {"5"}, "prompts": {"The cake\r\nThe oldest guest"}, "required": {"1"}, "guestbook": {"1"}})
	if code != 204 {
		t.Fatalf("make a drop box: %d %s", code, body)
	}
	op := page(t, port, owner)
	token := regexp.MustCompile(`data-link="drop/([0-9a-f]+)"`).FindStringSubmatch(op)[1]
	boxID := regexp.MustCompile(`(?:&#34;|")service(?:&#34;|"):(?:&#34;|")([0-9a-f-]{36})`).FindStringSubmatch(op)[1]
	if g, p := guestGet(t, port, "/drop/"+token); g != 200 || !strings.Contains(p, "The oldest guest") || !strings.Contains(p, "Leave a message") {
		t.Fatalf("guest page: %d", g)
	}
	send := func(prompt string, name string, data []byte) string {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		if prompt != "" {
			mw.WriteField("prompt", prompt)
		}
		fw, _ := mw.CreateFormFile("files", name)
		fw.Write(data)
		mw.Close()
		req, _ := http.NewRequest("POST", "http://localhost:"+port+"/drop/"+token+"/upload", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Accept", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	inbox := func() []string { return searchTag(t, alice, "inbox/Hunt", false) }
	if r := send("", "none.jpg", jpegBytes(7401)); !strings.Contains(r, "Choose what the photo is for") {
		t.Errorf("a photo with no prompt: %s", r)
	}
	if r := send("p1", "cake.jpg", jpegBytes(7402)); !strings.Contains(r, `"ok":true`) {
		t.Fatalf("a photo of the cake: %s", r)
	}
	send("p1", "cake2.jpg", jpegBytes(7403))
	send("p2", "guest.jpg", jpegBytes(7404))
	eventually(t, "three photos arrive", 60*time.Second, func() bool { return len(inbox()) == 3 })
	// each prompt is a tag of its own, under the box's tag, and Immich lists the photos by it // the wait is the check: Immich search is eventually consistent, so a second search right after can differ
	eventually(t, "the prompt tags are on", 60*time.Second, func() bool {
		return len(searchTag(t, alice, "inbox/Hunt/The cake", false)) == 2 && len(searchTag(t, alice, "inbox/Hunt/The oldest guest", false)) == 1
	})
	if p := page(t, port, owner); !strings.Contains(p, "2 photos") || !strings.Contains(p, "The cake") {
		t.Error("the owner sees how many photos came for each prompt")
	}

	// the guest book: words and a voice note, read and played by the owner only
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("name", "Eve")
	mw.WriteField("text", "Congratulations!")
	fw, _ := mw.CreateFormFile("voice", "voice")
	fw.Write(append([]byte{0x1A, 0x45, 0xDF, 0xA3}, make([]byte, 400)...))
	mw.Close()
	req, _ := http.NewRequest("POST", "http://localhost:"+port+"/drop/"+token+"/message", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 200 {
		t.Fatalf("leave a message: %v %v", err, res)
	}
	op = page(t, port, owner)
	voice := regexp.MustCompile(`src="dropbox/voice/([0-9a-f-]{36})"`).FindStringSubmatch(op)
	if voice == nil || !strings.Contains(op, "Congratulations!") {
		t.Fatalf("the owner reads the guest book: %.300s", op)
	}
	if c, _ := guestGetAs(t, owner, port, "/dropbox/voice/"+voice[1]); c != 200 {
		t.Errorf("the owner plays it: %d", c)
	}
	if c, _ := guestGet(t, port, "/dropbox/voice/"+voice[1]); c != 401 {
		t.Errorf("a stranger plays it: %d", c)
	}

	// sharing back: an album with the photos and a link anyone can open
	if code, b := postBody(t, port, "/dropbox/share", owner, url.Values{"service": {boxID}, "days": {"7"}, "message": {"Thank you all!"}}); code != 204 {
		t.Fatalf("share: %d %s", code, b)
	}
	op = page(t, port, owner)
	key := regexp.MustCompile(`data-share-key="([^"]+)"`).FindStringSubmatch(op)
	if key == nil || !strings.Contains(op, "Stop sharing") {
		t.Fatalf("the owner sees the link: %.200s", op)
	}
	var shared struct {
		AllowDownload bool `json:"allowDownload"`
		AllowUpload   bool `json:"allowUpload"`
		Description   string
		Assets        []struct{ ID string } `json:"assets"`
		Album         *struct {
			AlbumName  string `json:"albumName"`
			AssetCount int    `json:"assetCount"`
		} `json:"album"`
	}
	if c := call(t, "GET", "/shared-links/me?key="+key[1], nil, nil, &shared); c != 200 {
		t.Fatalf("a stranger opens the shared link: %d", c)
	}
	if !shared.AllowDownload || shared.AllowUpload || shared.Description != "Thank you all!" || shared.Album == nil || shared.Album.AlbumName != "Hunt" {
		t.Errorf("the shared link: %+v", shared)
	}
	if shared.Album.AssetCount != 3 {
		t.Errorf("the album holds %d photos, want 3", shared.Album.AssetCount)
	}
	// the box's own link, once closed, leads to the photos
	postBody(t, port, "/dropbox/pause", owner, url.Values{"service": {boxID}, "paused": {"1"}})
	if c, p := guestGet(t, port, "/drop/"+token); c != 200 || !strings.Contains(p, "Thank you all!") || !strings.Contains(p, `data-share-key="`+key[1]+`"`) || strings.Contains(p, `type="file"`) {
		t.Errorf("the closed box's page: %d", c)
	}
	// stopping ends the link and leaves the photos
	if code, b := postBody(t, port, "/dropbox/unshare", owner, url.Values{"service": {boxID}}); code != 204 {
		t.Fatalf("unshare: %d %s", code, b)
	}
	if c := call(t, "GET", "/shared-links/me?key="+key[1], nil, nil, nil); c < 400 {
		t.Errorf("a stopped link still opens: %d", c)
	}
	if n := len(inbox()); n != 3 {
		t.Errorf("stopping must leave the photos: %d", n)
	}
	if c, _ := guestGet(t, port, "/drop/"+token); c != 404 {
		t.Errorf("after stopping, the closed box answers like any closed one: %d", c)
	}
}

// noiseJPEG is a picture full of detail between two grey levels: a sharp photo, dark or bright, of a scene chosen by its seed.
func noiseJPEG(seed uint32, lo, hi uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	state := seed*2654435761 + 1
	for y := 0; y < 300; y += 3 {
		for x := 0; x < 400; x += 3 {
			state = state*1664525 + 1013904223
			v := lo + uint8((state>>16)%uint32(hi-lo+1))
			for dy := 0; dy < 3; dy++ {
				for dx := 0; dx < 3; dx++ {
					img.Set(x+dx, y+dy, color.RGBA{v, v, v, 255})
				}
			}
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 92})
	return b.Bytes()
}

// blurredJPEG is a picture with no detail: a soft gradient, as a blurred shot is.
func blurredJPEG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 400; x++ {
			v := uint8(80 + x*80/400)
			img.Set(x, y, color.RGBA{v, v, v, 255})
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 92})
	return b.Bytes()
}

// TestDropBoxWallBestOfAndHandingOn: a wall that a screen with no sign-in watches, a best-of that leaves out what is too
// dark, blurred or repeated and says why, and the best-of handed on through a real service to another account's library,
// all with a real Immich.
func TestDropBoxWallBestOfAndHandingOn(t *testing.T) {
	alice, _, family, _ := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 55)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	owner, receiver := loginCookie(t, "alice@e2e.test"), loginCookie(t, "family@e2e.test")

	// a service the owner sends to, which is where the best-of will be handed on
	if code, b := postBody(t, port, "/services", receiver, url.Values{"name": {"frame"}, "tag": {"frame-tag"}, "policy": {"open"}}); code != 204 {
		t.Fatalf("make a service: %d %s", code, b)
	}
	if code, b := postBody(t, port, "/join", owner, url.Values{"service": {"frame"}, "mode": {"copy"}}); code != 204 {
		t.Fatalf("join: %d %s", code, b)
	}
	code, body := postBody(t, port, "/dropbox", owner, url.Values{"name": {"Wow"}, "opendays": {"5"}})
	if code != 204 {
		t.Fatalf("make a drop box: %d %s", code, body)
	}
	op := page(t, port, owner)
	token := regexp.MustCompile(`data-link="drop/([0-9a-f]+)"`).FindStringSubmatch(op)[1]
	boxID := regexp.MustCompile(`(?:&#34;|")service(?:&#34;|"):(?:&#34;|")([0-9a-f-]{36})`).FindStringSubmatch(op)[1]
	inbox := func() []string { return searchTag(t, alice, "inbox/Wow", false) }

	// the wall: nothing at first; started by the owner, which gives the key one more permission
	if perms := keyPermissions(t, alice, "handoff: Wow (drop box)"); has(perms, "asset.view") {
		t.Fatal("a box with no wall must not be able to view photos")
	}
	if code, b := postBody(t, port, "/dropbox/wall", owner, url.Values{"service": {boxID}}); code != 204 {
		t.Fatalf("start the wall: %d %s", code, b)
	}
	if !has(keyPermissions(t, alice, "handoff: Wow (drop box)"), "asset.view") {
		t.Error("the wall needs the key to view pictures")
	}
	wall := regexp.MustCompile(`data-link="wall/([0-9a-f]+)"`).FindStringSubmatch(page(t, port, owner))[1]
	if c, p := guestGet(t, port, "/wall/"+wall); c != 200 || !strings.Contains(p, `id="wall"`) {
		t.Fatalf("the wall page: %d", c)
	}
	feed := func(after int64) (ids []int64) {
		c, b := guestGet(t, port, fmt.Sprintf("/wall/%s/feed?after=%d", wall, after))
		if c != 200 {
			t.Fatalf("feed: %d", c)
		}
		for _, m := range regexp.MustCompile(`"id":(\d+)`).FindAllStringSubmatch(b, -1) {
			var id int64
			fmt.Sscan(m[1], &id)
			ids = append(ids, id)
		}
		return
	}
	if n := len(feed(0)); n != 0 {
		t.Fatalf("an empty box has nothing on its wall: %d", n)
	}
	photos := map[string][]byte{
		"sharp.jpg": noiseJPEG(1, 40, 220), "sharp-again.jpg": noiseJPEG(1, 46, 226), "scene.jpg": noiseJPEG(9, 40, 220),
		"blur.jpg": blurredJPEG(), "dark.jpg": noiseJPEG(2, 0, 30), "bright.jpg": noiseJPEG(3, 235, 255),
	}
	for name, data := range photos {
		if c, p := guestUpload(t, port, token, map[string][]byte{name: data}); c != 200 || !strings.Contains(p, "Received") {
			t.Fatalf("upload %s: %d %s", name, c, p)
		}
	}
	eventually(t, "all six are in the library", 90*time.Second, func() bool { return len(inbox()) == 6 })
	ids := feed(0)
	if len(ids) != 6 {
		t.Fatalf("the wall's feed: %v", ids)
	}
	if newer := feed(ids[3]); len(newer) != 2 {
		t.Errorf("what is newer than the fourth: %v", newer)
	}
	// a picture, once Immich has made its thumbnail, with no sign-in; a photo that is not on the wall gives nothing
	for _, id := range ids {
		eventually(t, "Immich has made the picture", 90*time.Second, func() bool {
			res, err := http.Get(fmt.Sprintf("http://localhost:%s/wall/%s/photo/%d", port, wall, id))
			if err != nil {
				return false
			}
			defer res.Body.Close()
			b, _ := io.ReadAll(res.Body)
			return res.StatusCode == 200 && strings.HasPrefix(res.Header.Get("Content-Type"), "image/") && len(b) > 500
		})
	}
	if c, _ := guestGet(t, port, "/wall/"+wall+"/photo/999999"); c != 404 {
		t.Errorf("a photo that is not on the wall: %d", c)
	}

	// the best-of: it runs in the background and the owner's page says how it went
	if code, b := postBody(t, port, "/dropbox/highlights", owner, url.Values{"service": {boxID}, "strictness": {"normal"}}); code != 204 {
		t.Fatalf("make a best-of: %d %s", code, b)
	}
	eventually(t, "the best-of is done", 180*time.Second, func() bool {
		return strings.Contains(page(t, port, owner), "photos kept in the album Wow best of")
	})
	op = page(t, port, owner)
	for _, want := range []string{"2 of 6 photos kept", "Why 4 photos were left out", "blur.jpg", "blurry", "dark.jpg", "too dark", "bright.jpg", "too bright", "the same moment as a better photo"} {
		if !strings.Contains(op, want) {
			t.Errorf("the best-of's report lacks %q", want)
		}
	}
	var albums []struct {
		AlbumName  string `json:"albumName"`
		AssetCount int    `json:"assetCount"`
	}
	call(t, "GET", "/albums", keyAuth(alice), nil, &albums)
	best := -1
	for _, al := range albums {
		if al.AlbumName == "Wow best of" {
			best = al.AssetCount
		}
	}
	if best != 2 {
		t.Errorf("the best-of album holds %d photos, want 2", best)
	}
	if n := len(inbox()); n != 6 {
		t.Errorf("a best-of must delete nothing: %d photos in the inbox", n)
	}

	// handing the best-of on: the photos get the service's tag and the hand-over that exists carries them to the other account
	if code, b := postBody(t, port, "/dropbox/onward", owner, url.Values{"service": {boxID}, "target": {"frame"}}); code != 204 {
		t.Fatalf("choose a service: %d %s", code, b)
	}
	if !strings.Contains(page(t, port, owner), `option value="frame" selected`) {
		t.Error("the choice must be shown")
	}
	eventually(t, "the owner's workflow for the service exists", 90*time.Second, func() bool {
		var wfs []struct{ Name string }
		call(t, "GET", "/workflows", keyAuth(alice), nil, &wfs)
		for _, w := range wfs {
			if w.Name == "immich-handoff: frame" {
				return true
			}
		}
		return false
	})
	if code, b := postBody(t, port, "/dropbox/handon", owner, url.Values{"service": {boxID}, "scope": {"best"}}); code != 204 {
		t.Fatalf("hand on: %d %s", code, b)
	}
	eventually(t, "the best-of arrives in the other account", 120*time.Second, func() bool {
		return len(searchTag(t, family, "inbox/frame", false)) == 2
	})
	if p := page(t, port, owner); !strings.Contains(p, "2 photos (the best-of) to frame") {
		t.Errorf("the box must say where its photos went: %.200s", p)
	}
	if n := len(inbox()); n != 6 {
		t.Errorf("with copy mode the owner keeps every original: %d", n)
	}

	// stopping the wall: it shows nothing, and the key loses the permission
	if code, b := postBody(t, port, "/dropbox/wall/stop", owner, url.Values{"service": {boxID}}); code != 204 {
		t.Fatalf("stop the wall: %d %s", code, b)
	}
	if c, _ := guestGet(t, port, "/wall/"+wall+"/feed?after=0"); c != 404 {
		t.Errorf("a stopped wall: %d", c)
	}
	if has(keyPermissions(t, alice, "handoff: Wow (drop box)"), "asset.view") {
		t.Error("stopping the wall must take the permission off the key")
	}
}
