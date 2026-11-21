//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestDestinations: photos handed over to a folder (docs/contributing/design/destinations-design.md), against a real Immich and the real app. A service
// with only a destination moves a tagged photo into the folder with its record and moves the original; a second photo with the same name
// is suffixed; a destination that does not answer holds the original (still tagged) while the other destination has the photo, and
// removing it lets the move go on; taking the hand-over back deletes the files and restores the original; and a service with an Immich
// receiver and a destination does both. The WebDAV, S3 and SFTP kinds have their own tests against in-test servers (internal/dest).
func TestDestinations(t *testing.T) {
	alice, bob, _, _ := bootstrap(t) // alice owns the services, bob sends
	nas, broken := t.TempDir(), filepath.Join(t.TempDir(), "a-file-not-a-folder")
	os.WriteFile(broken, []byte("x"), 0o644)
	dir := t.TempDir()
	file := filepath.Join(dir, "destinations.yaml")
	os.WriteFile(file, []byte(fmt.Sprintf("destinations:\n  nas:\n    kind: folder\n    root: %s\n  attic:\n    kind: folder\n    root: %s\n", nas, broken)), 0o600)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 73)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+dir, "DESTINATIONS_FILE="+file)
	owner, sender := loginCookie(t, "alice@e2e.test"), loginCookie(t, "bob@e2e.test")
	form := func(path, cookie string, v url.Values) string {
		t.Helper()
		code, body := postBody(t, port, path, cookie, v)
		if code != 204 {
			t.Fatalf("%s: HTTP %d %.300s", path, code, body)
		}
		return body
	}
	workflow := func(name string) {
		eventually(t, "bob's workflow "+name, 90*time.Second, func() bool {
			var wfs []struct{ Name string }
			call(t, "GET", "/workflows", keyAuth(bob), nil, &wfs)
			for _, w := range wfs {
				if w.Name == "immich-handoff: "+name {
					return true
				}
			}
			return false
		})
	}
	files := func(root, contains string) []string { // the files under a folder whose name contains this
		var out []string
		filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err == nil && !e.IsDir() && strings.Contains(e.Name(), contains) {
				rel, _ := filepath.Rel(root, p)
				out = append(out, filepath.ToSlash(rel))
			}
			return nil
		})
		return out
	}
	trashed := func(id string) bool { a, c := getAsset(t, bob, id); return c == 200 && a.IsTrashed }

	// a service with only a destination
	form("/services", owner, url.Values{"name": {"stash"}, "tag": {"to-stash"}, "policy": {"open"}, "destonly": {"1"}})
	form("/join", sender, url.Values{"service": {"stash"}, "mode": {"move"}})
	workflow("stash")
	form("/services/destinations", owner, url.Values{"service": {"stash"}, "ref": {"admin:nas"}})
	if p := page(t, port, owner); !strings.Contains(p, "This service has only destinations") || !strings.Contains(p, "Working") {
		t.Errorf("the owner's card shows the destination working")
	}

	a1, _ := upload(t, bob, "IMG_a.jpg", jpegBytes(73001))
	tagAsset(t, bob, "to-stash", a1)
	eventually(t, "the first photo is in the folder and its original moved", 120*time.Second, func() bool {
		return len(files(nas, "IMG_a")) == 2 && trashed(a1)
	})
	got := files(nas, "IMG_a")
	var photo, record string
	for _, f := range got {
		if strings.HasSuffix(f, ".handoff.json") {
			record = f
		} else {
			photo = f
		}
	}
	if !strings.HasPrefix(photo, "stash/bob/") || !strings.HasSuffix(photo, "/IMG_a.jpg") {
		t.Errorf("by service, sender and date: %q", photo)
	}
	if b, _ := os.ReadFile(filepath.Join(nas, photo)); string(b) != string(jpegBytes(73001)) {
		t.Error("the file in the folder is the original, byte for byte")
	}
	rec, _ := os.ReadFile(filepath.Join(nas, record))
	for _, want := range []string{`"sender": "bob"`, `"service": "stash"`, `"original_name": "IMG_a.jpg"`, `"verified": "size and checksum"`} {
		if !strings.Contains(string(rec), want) {
			t.Errorf("the record lacks %q:\n%s", want, rec)
		}
	}

	// another photo with the same name is not the same file: it is suffixed
	a2, _ := upload(t, bob, "IMG_a.jpg", jpegBytes(73002))
	tagAsset(t, bob, "to-stash", a2)
	eventually(t, "the second photo with that name is suffixed", 120*time.Second, func() bool {
		return len(files(nas, "IMG_a (2).jpg")) >= 1 && trashed(a2)
	})
	if b, _ := os.ReadFile(filepath.Join(nas, filepath.Dir(photo), "IMG_a (2).jpg")); string(b) != string(jpegBytes(73002)) {
		t.Error("the first file was not overwritten and the second is intact")
	}
	if b, _ := os.ReadFile(filepath.Join(nas, photo)); string(b) != string(jpegBytes(73001)) {
		t.Error("the first file is untouched")
	}

	// a destination that does not answer holds the original, while the other has the photo
	form("/services/destinations", owner, url.Values{"service": {"stash"}, "ref": {"admin:attic"}})
	c3, _ := upload(t, bob, "IMG_c.jpg", jpegBytes(73003))
	tagAsset(t, bob, "to-stash", c3)
	eventually(t, "the third photo is in the working destination", 120*time.Second, func() bool { return len(files(nas, "IMG_c.jpg")) >= 1 })
	time.Sleep(25 * time.Second) // a few more passes
	if trashed(c3) {
		t.Fatal("the original was moved although a destination does not hold the photo")
	}
	still := false
	for _, id := range searchTag(t, bob, "to-stash", false) {
		still = still || id == c3
	}
	if !still {
		t.Error("the photo is still tagged, waiting for the destination")
	}
	if n := len(files(nas, "IMG_c")); n != 2 { // the photo and its record, and not a second copy at every pass
		t.Errorf("the working destination holds the photo once: %v", files(nas, "IMG_c"))
	}
	p := page(t, port, owner)
	if !strings.Contains(p, "Not answering") || !strings.Contains(p, "is not a folder") {
		t.Errorf("the owner's card says the destination is not answering and why")
	}
	// removing it lets the move go on
	m := regexp.MustCompile(`(?s)attic</b>.*?destinations/remove" hx-vals="([^"]*)"`).FindStringSubmatch(p)
	if m == nil {
		t.Fatal("no remove button for the attic")
	}
	var vals struct{ Service, ID string }
	if err := json.Unmarshal([]byte(html.UnescapeString(m[1])), &vals); err != nil {
		t.Fatal(err)
	}
	form("/services/destinations/remove", owner, url.Values{"service": {"stash"}, "id": {vals.ID}})
	eventually(t, "the original moves once nothing is waited for", 120*time.Second, func() bool { return trashed(c3) })

	// taking the hand-over back deletes the files and restores the original
	form("/services/takeback", sender, url.Values{"service": {"stash"}})
	eventually(t, "the files are deleted and the original is back", 60*time.Second, func() bool {
		return len(files(nas, "IMG_c.jpg")) == 0 && !trashed(c3)
	})
	if len(files(nas, "IMG_a.jpg")) == 0 {
		t.Error("what an earlier hand-over put there stays")
	}

	// a service with an Immich receiver and a destination does both
	form("/services", owner, url.Values{"name": {"both"}, "tag": {"to-both"}, "policy": {"open"}})
	form("/join", sender, url.Values{"service": {"both"}, "mode": {"move"}})
	workflow("both")
	form("/services/destinations", owner, url.Values{"service": {"both"}, "ref": {"admin:nas"}, "template": {"copies/{yyyy}/{name}"}})
	before := len(searchTag(t, alice, "inbox/both", false))
	d4, _ := upload(t, bob, "IMG_d.jpg", jpegBytes(73004))
	tagAsset(t, bob, "to-both", d4)
	eventually(t, "the photo reached the owner and the folder, and the original moved", 120*time.Second, func() bool {
		return len(searchTag(t, alice, "inbox/both", false)) == before+1 && len(files(nas, "IMG_d.jpg")) == 2 && trashed(d4)
	})
	if got := files(nas, "IMG_d.jpg"); !strings.HasPrefix(got[0], "copies/") {
		t.Errorf("the template of this destination: %v", got)
	}
}
