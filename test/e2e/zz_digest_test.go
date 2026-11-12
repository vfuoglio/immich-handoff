//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/url"
	"strings"
	"testing"
	"time"
)

// texturedJPEG is a scene full of detail, different for each seed: what the digest's rules keep (the plain gradients of
// jpegBytes would all be left out as blurred).
func texturedJPEG(seed uint32) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	state := seed*2654435761 + 1
	for y := 0; y < 300; y += 3 {
		for x := 0; x < 400; x += 3 {
			state = state*1664525 + 1013904223
			v := 40 + uint8((state>>16)%181)
			for dy := 0; dy < 3; dy++ {
				for dx := 0; dx < 3; dx++ {
					img.Set(x+dx, y+dy, color.RGBA{v, v, v, 255})
				}
			}
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, nil)
	return b.Bytes()
}

// TestWeeklyDigest: the owner turns the weekly digest on, photos are handed over, and a digest made now puts the best of
// them in a new album of the owner's with a shared link that ends; turning it off ends the link.
func TestWeeklyDigest(t *testing.T) {
	alice, bob, _, _ := bootstrap(t) // alice owns the service, bob sends
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 62)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	owner, sender := loginCookie(t, "alice@e2e.test"), loginCookie(t, "bob@e2e.test")
	must := func(code int, want int, what string) {
		t.Helper()
		if code != want {
			t.Fatalf("%s: HTTP %d, want %d", what, code, want)
		}
	}
	code, _ := postBody(t, port, "/services", owner, url.Values{"name": {"digestive"}, "tag": {"digestive-tag"}, "policy": {"open"}})
	must(code, 204, "create")
	code, _ = postBody(t, port, "/join", sender, url.Values{"service": {"digestive"}, "mode": {"copy"}})
	must(code, 204, "join")
	eventually(t, "bob's workflow exists", 90*time.Second, func() bool {
		var wfs []struct{ Name string }
		call(t, "GET", "/workflows", keyAuth(bob), nil, &wfs)
		for _, w := range wfs {
			if w.Name == "immich-handoff: digestive" {
				return true
			}
		}
		return false
	})
	code, body := postBody(t, port, "/services/digest", owner, url.Values{"service": {"digestive"}, "do": {"on"}})
	must(code, 204, "turn the digest on: "+body)
	if !has(keyPermissions(t, alice, "handoff: digestive (receiving)"), "sharedLink.create") {
		t.Fatal("turning the digest on must give the receiver key what it needs to make a shared link")
	}

	for i, seed := range []uint32{11, 4242} {
		id, _ := upload(t, bob, fmt.Sprintf("digest-%d.jpg", i), texturedJPEG(seed))
		tagAsset(t, bob, "digestive-tag", id)
	}
	eventually(t, "both photos arrive", 90*time.Second, func() bool { return len(searchTag(t, alice, "inbox/digestive", false)) == 2 })

	code, body = postBody(t, port, "/services/digest", owner, url.Values{"service": {"digestive"}, "do": {"now"}})
	must(code, 204, "make the digest now: "+body)
	eventually(t, "the digest is on the page", 120*time.Second, func() bool {
		return strings.Contains(page(t, port, owner), "2 photos of the week")
	})

	var albums []struct{ AlbumName string }
	call(t, "GET", "/albums", keyAuth(alice), nil, &albums)
	album := ""
	for _, a := range albums {
		if strings.HasPrefix(a.AlbumName, "digestive week of ") {
			album = a.AlbumName
		}
	}
	if album == "" {
		t.Fatalf("no album for the digest: %v", albums)
	}
	var links []struct {
		Key       string
		ExpiresAt string
		Type      string
	}
	call(t, "GET", "/shared-links", keyAuth(alice), nil, &links)
	found := false
	for _, l := range links {
		if l.Type == "ALBUM" && l.ExpiresAt != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no shared link that ends: %v", links)
	}
	if got := searchTag(t, alice, "inbox/digestive", true); len(got) != 2 {
		t.Errorf("a digest changes nothing: %d photos in the inbox", len(got))
	}

	code, body = postBody(t, port, "/services/digest", owner, url.Values{"service": {"digestive"}, "do": {"off"}})
	must(code, 204, "turn it off: "+body)
	call(t, "GET", "/shared-links", keyAuth(alice), nil, &links)
	for _, l := range links {
		if l.Type == "ALBUM" {
			t.Errorf("turning the digest off must end its link: %v", links)
		}
	}
	if has(keyPermissions(t, alice, "handoff: digestive (receiving)"), "sharedLink.create") {
		t.Error("turning the digest off must take its permissions off the key")
	}
}
