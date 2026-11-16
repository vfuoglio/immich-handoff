//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestAlbumPickers: every form that lists the person's own albums loads them when it is opened, in a real browser
// (they are fetched lazily from inside a closed details, so a wrong trigger or URL shows only here).
func TestAlbumPickers(t *testing.T) {
	port, owner, alice, album, _ := roomFixture(t, 82)
	_, bob, _, _ := bootstrap(t) // the albums are shared with a second person, as real ones are
	mustOK(t, call(t, "POST", "/albums", keyAuth(alice), map[string]any{"albumName": "Empty one"}, nil), "empty album")
	mustOK(t, call(t, "POST", "/albums", keyAuth(alice), map[string]any{"albumName": "Shared one", "description": "d", "albumUsers": []map[string]string{{"userId": bob.id, "role": "editor"}}}, nil), "shared album")
	for _, f := range []string{"room-albums", "frame-albums", "share-albums", "share-albums?for=immich"} {
		req, _ := http.NewRequest("GET", "http://localhost:"+port+"/fragments/"+f, nil)
		req.Header.Set("Cookie", owner)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.Contains(string(b), "<select") || !strings.Contains(string(b), "Shared one") {
			t.Errorf("%s: %d %.200s", f, res.StatusCode, b)
		}
	}
	name := fmt.Sprintf("pick-%d", time.Now().UnixNano()%1_000_000)
	if code, body := postBody(t, port, "/rooms", owner, url.Values{"name": {name}, "album": {album}, "mark": {"text"}, "marktext": {"x"}, "ends": {"0"}, "pickmode": {"shared"}, "notes": {"replies"}}); code != 204 {
		t.Fatalf("make a room: %d %s", code, body)
	}
	ctx := browse(t, port, owner, look{})
	run(t, ctx, chromeNavigate("http://localhost:"+port+"/"))
	for _, c := range []struct{ what, field, sel string }{
		{"client room", "#rm-name", "#rm-album"},
		{"frame", "#fr-name", "#fr-album"},
	} {
		until(t, ctx, "the album list loads in the "+c.what+" form", fmt.Sprintf(`(() => {
			const d = [...document.querySelectorAll('details.offer')].find(x => x.querySelector('%s'));
			if (!d) return false;
			d.open = true;
			const s = document.querySelector('%s');
			return !!s && s.options.length > 0;
		})()`, c.field, c.sel), 20*time.Second)
	}
}
