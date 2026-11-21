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

// TestSmartAlbums: a person keeps an album up to date with a rule on a real Immich (here by a tag, which needs no EXIF). The album
// is made with the person's own key, a photo that comes later is offered and goes in only on yes, a look back offers the older ones,
// and a photo that stops matching is taken out when the person asked for that. It also shows that Immich's search filters by tag and
// that a key with albumAsset.delete can take a photo out of an album.
func TestSmartAlbums(t *testing.T) {
	_, bob, _, _ := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 96)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir(), "RULES_CHECK_MINUTES=0.1", "RULES_SETTLE_MINUTES=0.02")
	cookie := loginCookie(t, "bob@e2e.test")
	form := func(path string, v url.Values) {
		t.Helper()
		if code, body := postBody(t, port, path, cookie, v); code != 204 {
			t.Fatalf("%s: HTTP %d %.300s", path, code, body)
		}
	}
	albumCount := func(name string) (int, bool) {
		var list []struct {
			Name       string `json:"albumName"`
			AssetCount int    `json:"assetCount"`
		}
		call(t, "GET", "/albums", keyAuth(bob), nil, &list)
		for _, a := range list {
			if a.Name == name {
				return a.AssetCount, true
			}
		}
		return 0, false
	}

	var tag struct {
		ID string `json:"id"`
	}
	mustOK(t, call(t, "POST", "/tags", keyAuth(bob), map[string]string{"name": fmt.Sprintf("smart-e2e-%d", time.Now().UnixNano())}, &tag), "a tag")
	older1, _ := upload(t, bob, "smart-old-1.jpg", jpegBytes(50001))
	older2, _ := upload(t, bob, "smart-old-2.jpg", jpegBytes(50002))
	mustOK(t, call(t, "PUT", "/tags/"+tag.ID+"/assets", keyAuth(bob), map[string]any{"ids": []string{older1, older2}}, nil), "tag the older photos")

	if p := page(t, port, cookie); !strings.Contains(p, "Smart albums") {
		t.Fatalf("the Smart albums card is offered: %.300s", p)
	}
	if code, _ := postBody(t, port, "/rules/album", cookie, url.Values{"newname": {"Nothing"}}); code != 409 {
		t.Errorf("a smart album with nothing to look for is refused: %d", code)
	}
	form("/rules/album", url.Values{"tag": {tag.ID}, "newname": {"Smart e2e"}, "stale": {"1"}})
	if n, ok := albumCount("Smart e2e"); !ok || n != 0 {
		t.Fatalf("the album is made empty with the person's key: %d %v", n, ok)
	}
	if p := page(t, port, cookie); !strings.Contains(p, "Smart e2e") || !strings.Contains(p, "Takes out what no longer matches") {
		t.Fatalf("the rule is on the page: %.500s", p)
	}

	// a photo that comes after the rule is offered, and goes in only on yes
	c, _ := upload(t, bob, "smart-new.jpg", jpegBytes(50003))
	mustOK(t, call(t, "PUT", "/tags/"+tag.ID+"/assets", keyAuth(bob), map[string]any{"ids": []string{c}}, nil), "tag the new photo")
	cardRe := regexp.MustCompile(`rules/photo/([0-9a-f-]{36})/([0-9a-f-]{36})`)
	var rule string
	eventually(t, "the suggestion appears", 120*time.Second, func() bool {
		m := cardRe.FindStringSubmatch(page(t, port, cookie))
		if m == nil {
			return false
		}
		rule = m[1]
		return true
	})
	if n, _ := albumCount("Smart e2e"); n != 0 {
		t.Fatalf("nothing goes in before the answer: %d", n)
	}
	form("/rules/answer", url.Values{"rule": {rule}, "do": {"send"}})
	if n, _ := albumCount("Smart e2e"); n != 1 {
		t.Fatalf("the photo went in: %d", n)
	}

	// Immich has to have read and shown the older photos before a look back can offer them (a slower machine takes longer)
	eventually(t, "Immich has processed the older photos", 120*time.Second, func() bool {
		for _, id := range []string{older1, older2} {
			var a struct {
				HasMetadata bool    `json:"hasMetadata"`
				Thumbhash   *string `json:"thumbhash"`
			}
			call(t, "GET", "/assets/"+id, keyAuth(bob), nil, &a)
			if !a.HasMetadata || a.Thumbhash == nil || *a.Thumbhash == "" {
				return false
			}
		}
		return true
	})
	// and Immich's search by tag has to find them (a tag put on a photo reaches the search a moment later on a slower machine)
	eventually(t, "a search by the tag finds the older photos", 120*time.Second, func() bool {
		var res struct {
			Assets struct {
				Items []struct {
					ID string `json:"id"`
				}
			}
		}
		call(t, "POST", "/search/metadata", keyAuth(bob), map[string]any{"tagIds": []string{tag.ID}}, &res)
		found := map[string]bool{}
		for _, i := range res.Assets.Items {
			found[i.ID] = true
		}
		return found[older1] && found[older2]
	})
	// the older photos come with a look back
	form("/rules/lookback", url.Values{"rule": {rule}})
	eventually(t, "both older photos are offered", 30*time.Second, func() bool {
		return strings.Contains(page(t, port, cookie), "2 new photos for the album Smart e2e")
	})
	form("/rules/answer", url.Values{"rule": {rule}, "do": {"send"}})
	eventually(t, "the album holds all three", 30*time.Second, func() bool {
		n, _ := albumCount("Smart e2e")
		return n == 3
	})

	// a photo that stops matching is taken out by the rule's next look
	mustOK(t, call(t, "DELETE", "/tags/"+tag.ID+"/assets", keyAuth(bob), map[string]any{"ids": []string{c}}, nil), "untag the new photo")
	eventually(t, "the photo that no longer matches is taken out", 120*time.Second, func() bool {
		n, _ := albumCount("Smart e2e")
		return n == 2
	})

	// stopping the rule leaves the album as it is
	form("/rules/delete", url.Values{"rule": {rule}})
	if n, ok := albumCount("Smart e2e"); !ok || n != 2 {
		t.Errorf("the album stays as it is: %d %v", n, ok)
	}
}
