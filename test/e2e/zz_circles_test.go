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

// TestCircles: alice makes a circle with bob on a real Immich. An album she makes afterwards is offered, nothing is shared before
// her answer, Share adds bob to it with her own key (he then sees it among the albums shared with him), an album she made before is
// left alone until she offers the existing ones, and taking bob out of the circle takes him off what the app shared.
func TestCircles(t *testing.T) {
	alice, bob, _, _ := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 97)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir(), "RULES_CHECK_MINUTES=0.1", "RULES_SETTLE_MINUTES=0.02")
	cookie := loginCookie(t, "alice@e2e.test")
	form := func(path string, v url.Values) {
		t.Helper()
		if code, body := postBody(t, port, path, cookie, v); code != 204 {
			t.Fatalf("%s: HTTP %d %.300s", path, code, body)
		}
	}
	stamp := fmt.Sprint(time.Now().UnixNano())
	seenByBob := func(name string) bool {
		var list []struct {
			Name string `json:"albumName"`
		}
		call(t, "GET", "/albums?shared=true", keyAuth(bob), nil, &list)
		for _, a := range list {
			if a.Name == name {
				return true
			}
		}
		return false
	}
	newAlbum := func(name string) string {
		var al struct {
			ID string `json:"id"`
		}
		mustOK(t, call(t, "POST", "/albums", keyAuth(alice), map[string]any{"albumName": name}, &al), "album "+name)
		return al.ID
	}
	before := "Before the circle " + stamp
	newAlbum(before)

	if p := page(t, port, cookie); !strings.Contains(p, "Make a circle") {
		t.Fatalf("the Circles card is offered: %.300s", p)
	}
	if code, _ := postBody(t, port, "/circles", cookie, url.Values{"name": {"Nobody"}, "emails": {"nobody@e2e.test"}}); code != 409 {
		t.Errorf("an email nobody has is refused: %d", code)
	}
	form("/circles", url.Values{"name": {"Friends"}, "emails": {"bob@e2e.test"}})
	if p := page(t, port, cookie); !strings.Contains(p, "Friends") || !strings.Contains(p, "bob@e2e.test") {
		t.Fatalf("the circle is on the page: %.500s", p)
	}

	trip := "Circle trip " + stamp
	newAlbum(trip)
	m := regexp.MustCompile(`id="circle-([0-9a-f-]{36})"`).FindStringSubmatch(page(t, port, cookie))
	if m == nil {
		t.Fatal("the circle's card has an id")
	}
	circle := m[1]
	eventually(t, "the album is offered", 120*time.Second, func() bool {
		p := page(t, port, cookie)
		return strings.Contains(p, "Share them") && strings.Contains(p, "Circle trip")
	})
	if seenByBob(trip) {
		t.Fatal("the album was shared before the answer")
	}
	form("/circles/answer", url.Values{"circle": {circle}, "do": {"send"}})
	if !seenByBob(trip) {
		t.Fatal("bob does not see the album shared with him")
	}
	if seenByBob(before) {
		t.Fatal("an album from before the circle was shared")
	}

	// the existing albums are offered on request, and only offered
	form("/circles/catchup", url.Values{"circle": {circle}})
	if seenByBob(before) {
		t.Fatal("the catch-up shared an album without asking")
	}
	form("/circles/answer", url.Values{"circle": {circle}, "do": {"decline"}})

	// taking bob out takes him off what the app shared
	form("/circles/member/remove", url.Values{"circle": {circle}, "member": {bob.id}})
	if seenByBob(trip) {
		t.Fatal("bob is still on the album after he was taken out of the circle")
	}
	form("/circles/delete", url.Values{"circle": {circle}})
}
