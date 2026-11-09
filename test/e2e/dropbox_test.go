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
	a := getFull(t, alice, id)
	if tags := tagNames(a); !strings.Contains(tags, "Gala") || !strings.Contains(tags, "gala") {
		t.Errorf("the photo's tags: %q", tags)
	}
	var albums []struct {
		AlbumName  string `json:"albumName"`
		AssetCount int    `json:"assetCount"`
	}
	call(t, "GET", "/albums", keyAuth(alice), nil, &albums)
	found := false
	for _, al := range albums {
		if al.AlbumName == "Gala photos" {
			found = true
			if al.AssetCount != 1 {
				t.Errorf("the album holds %d photos, want 1", al.AssetCount)
			}
		}
	}
	if !found {
		t.Error("the album must be made on the first arrival")
	}
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
