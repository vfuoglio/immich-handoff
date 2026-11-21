//go:build e2e

package e2e

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestAlbumTree: the album tree page on a real Immich. Albums named with slashes are shown as folders, a cover comes through the
// person's own session by album id, an album shared with another person is in that person's Shared with me, and the move renames
// an own album with the person's session (no key) and is refused for an album that is somebody else's.
func TestAlbumTree(t *testing.T) {
	alice, bob, _, _ := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 98)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	ac, bc := loginCookie(t, "alice@e2e.test"), loginCookie(t, "bob@e2e.test")
	stamp := fmt.Sprint(time.Now().UnixNano())
	newAlbum := func(name string) string {
		var al struct {
			ID string `json:"id"`
		}
		mustOK(t, call(t, "POST", "/albums", keyAuth(alice), map[string]any{"albumName": name}, &al), "album "+name)
		return al.ID
	}
	rome := newAlbum("Trips-" + stamp + " / 2024 / Rome")
	newAlbum("Trips-" + stamp + "/2024/Oslo")
	asset, _ := upload(t, alice, "tree-cover.jpg", jpegBytes(60001))
	mustOK(t, call(t, "PUT", "/albums/"+rome+"/assets", keyAuth(alice), map[string]any{"ids": []string{asset}}, nil), "a photo in the album")
	shared := newAlbum("Family-" + stamp + " / Beach")
	mustOK(t, call(t, "PUT", "/albums/"+shared+"/users", keyAuth(alice), map[string]any{"albumUsers": []map[string]string{{"userId": bob.id, "role": "viewer"}}}, nil), "share with bob")

	if code, _ := getBody(t, port, "/albums", ""); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	code, body := getBody(t, port, "/albums", ac)
	if code != 200 || !strings.Contains(body, "Trips-"+stamp) || !strings.Contains(body, "Rome") || !strings.Contains(body, "Oslo") || strings.Contains(body, "Trips-"+stamp+"/2024/Oslo") {
		t.Fatalf("alice's tree: %d %.600s", code, body)
	}
	if !strings.Contains(body, "albums/"+rome+"/cover") {
		t.Errorf("an album with a photo has a cover")
	}
	eventually(t, "the cover comes through the person's own session (Immich makes thumbnails a moment after an upload)", 90*time.Second, func() bool {
		code, _ := getBody(t, port, "/albums/"+rome+"/cover", ac)
		return code == 200
	})
	if code, _ := getBody(t, port, "/albums/"+rome+"/cover", bc); code != 404 {
		t.Errorf("bob has no right to alice's cover: %d", code)
	}
	if _, body := getBody(t, port, "/albums", bc); !strings.Contains(body, "Shared with me") || !strings.Contains(body, "Beach") || !strings.Contains(body, "shared by") {
		t.Errorf("bob sees the shared album apart: %.600s", body)
	}

	// the move: with alice's own session, no key
	if code, b := postBody(t, port, "/albums/move", bc, url.Values{"album": {shared}, "folder": {"Mine"}}); code != 409 {
		t.Errorf("bob cannot rename an album that is alice's: %d %s", code, b)
	}
	if code, b := postBody(t, port, "/albums/move", ac, url.Values{"album": {rome}, "folder": {"Journeys-" + stamp + " / 2025"}}); code != 204 {
		t.Fatalf("the move: %d %s", code, b)
	}
	var al struct {
		Name string `json:"albumName"`
	}
	mustOK(t, call(t, "GET", "/albums/"+rome, keyAuth(alice), nil, &al), "the album")
	if al.Name != "Journeys-"+stamp+" / 2025 / Rome" {
		t.Errorf("Immich holds the new name: %q", al.Name)
	}
	var again struct {
		Name string `json:"albumName"`
	}
	mustOK(t, call(t, "GET", "/albums/"+shared, keyAuth(alice), nil, &again), "the shared album")
	if !strings.HasPrefix(again.Name, "Family-") {
		t.Errorf("the shared album was not touched: %q", again.Name)
	}
}
