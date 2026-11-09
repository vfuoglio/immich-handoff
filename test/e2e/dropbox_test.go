//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
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
