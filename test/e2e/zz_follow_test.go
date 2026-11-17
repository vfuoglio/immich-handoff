//go:build e2e

// Named to sort last, like the other zz_ tests: it leaves an album, photos and copies in the shared Immich.
package e2e

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestFollowSharedAlbum: a person follows an album that was shared with them, on a real Immich. What is in it then is not
// offered, what is added later is (with the reason), Send copies it into their library, Always three times lets the rule
// copy by itself, and stopping the album deletes the key the app made.
func TestFollowSharedAlbum(t *testing.T) {
	alice, bob, _, _ := bootstrap(t) // alice shares the album, bob follows it
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 85)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir(), "RULES_CHECK_MINUTES=0.1", "RULES_SETTLE_MINUTES=0.02")
	cookie := loginCookie(t, "bob@e2e.test")
	form := func(path string, v url.Values) {
		t.Helper()
		if code, body := postBody(t, port, path, cookie, v); code != 204 {
			t.Fatalf("%s: HTTP %d %.300s", path, code, body)
		}
	}
	seed := uint32(time.Now().UnixNano())
	n := uint32(0)
	photo := func(label string) (id, name string) {
		n++
		name = fmt.Sprintf("follow-%s-%d.jpg", label, seed)
		id, _ = upload(t, alice, name, noiseJPEG(seed+n, 40, 220))
		waitProcessed(t, alice, id)
		return id, name
	}
	first, _ := photo("first")
	albumName := fmt.Sprintf("Followed %d", seed)
	var album struct {
		ID string `json:"id"`
	}
	mustOK(t, call(t, "POST", "/albums", keyAuth(alice), map[string]any{"albumName": albumName, "assetIds": []string{first},
		"albumUsers": []map[string]string{{"userId": bob.id, "role": "viewer"}}}, &album), "alice's album, shared with bob")
	addToAlbum := func(id string) {
		mustOK(t, call(t, "PUT", "/albums/"+album.ID+"/assets", keyAuth(alice), map[string]any{"ids": []string{id}}, nil), "alice adds a photo to the album")
	}
	bobHas := func(name string) bool {
		var res struct {
			Assets struct {
				Items []struct {
					OwnerID string `json:"ownerId"`
				} `json:"items"`
			} `json:"assets"`
		}
		call(t, "POST", "/search/metadata", keyAuth(bob), map[string]any{"originalFileName": name}, &res)
		for _, a := range res.Assets.Items {
			if a.OwnerID == bob.id {
				return true
			}
		}
		return false
	}
	ruleID := func(re string) string {
		m := regexp.MustCompile(re).FindStringSubmatch(page(t, port, cookie))
		if m == nil {
			return ""
		}
		return m[1]
	}

	// the album is listed for bob, and following it makes his key
	var listed string
	eventually(t, "the form lists the album", 20*time.Second, func() bool {
		var code int
		code, listed = getBody(t, port, "/fragments/follow-albums", cookie)
		return code == 200 && strings.Contains(listed, album.ID)
	})
	form("/rules/follow", url.Values{"album_id": {album.ID}, "history": {"new"}, "dest": {"tag"}})
	if !hasName(keyNames(t, bob), "handoff: library") {
		t.Fatalf("the key the app made for following albums is in bob's Immich: %v", keyNames(t, bob))
	}

	// what was in the album when it was followed is not offered; what is added later is
	second, secondName := photo("second")
	addToAlbum(second)
	var rule string
	eventually(t, "the new photo is offered", 90*time.Second, func() bool {
		rule = ruleID(`rules/review/([0-9a-f-]{36})`)
		return rule != "" && strings.Contains(page(t, port, cookie), "1 new photo in "+albumName)
	})
	review := page(t, port, cookie)
	if strings.Contains(review, "follow-first-") {
		t.Error("what was in the album at the start must not be offered")
	}
	if bobHas(secondName) {
		t.Error("nothing is copied before the person says yes")
	}
	form("/rules/answer", url.Values{"rule": {rule}, "do": {"send"}})
	eventually(t, "the copy is in bob's library", 30*time.Second, func() bool { return bobHas(secondName) })

	// three Always, then it goes on by itself
	for i := 0; i < 3; i++ {
		id, name := photo(fmt.Sprintf("always-%d", i))
		addToAlbum(id)
		eventually(t, "the photo is offered", 90*time.Second, func() bool {
			return strings.Contains(page(t, port, cookie), "1 new photo in "+albumName)
		})
		form("/rules/answer", url.Values{"rule": {rule}, "do": {"always"}})
		eventually(t, "the copy is in bob's library", 30*time.Second, func() bool { return bobHas(name) })
	}
	form("/rules/auto", url.Values{"rule": {rule}, "on": {"1"}})
	last, lastName := photo("by-itself")
	addToAlbum(last)
	eventually(t, "the photo is copied without asking", 90*time.Second, func() bool { return bobHas(lastName) })
	// the copy is in Immich a moment before the rule writes down that it sent it
	eventually(t, "nothing waits when the rule copies by itself", 30*time.Second, func() bool {
		return !strings.Contains(page(t, port, cookie), "1 new photo in "+albumName)
	})

	// stopping the album deletes the key
	form("/rules/delete", url.Values{"rule": {rule}})
	eventually(t, "the key is gone", 20*time.Second, func() bool { return !hasName(keyNames(t, bob), "handoff: library") })
}

// getBody reads a page of the app as a person signed in to Immich would, and returns the answer as text.
func getBody(t *testing.T, port, path, cookie string) (int, string) {
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
