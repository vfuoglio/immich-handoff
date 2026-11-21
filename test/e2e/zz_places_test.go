//go:build e2e

package e2e

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestPlaces: the rule that gives photos with no place the place of a photo of the same camera taken minutes from it, on a real Immich.
// Five sample photos carry a camera and a time but no GPS; the test writes a place into two of them (as a camera with GPS would have),
// and the rule offers the one between them, not the one hours away and not the one of another camera. Nothing is written before the
// answer; a yes writes the nearer neighbour's place, and Immich finds the city itself. Stopping the rule deletes its key.
func TestPlaces(t *testing.T) {
	_, bob, _, _ := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 101)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir(), "RULES_CHECK_MINUTES=0.1", "RULES_SETTLE_MINUTES=0.02")
	cookie := loginCookie(t, "bob@e2e.test")
	form := func(path string, v url.Values) (int, string) { return postBody(t, port, path, cookie, v) }
	placeKeys := func() int {
		var keys []struct{ Name string }
		call(t, "GET", "/api-keys", keyAuth(bob), nil, &keys)
		n := 0
		for _, k := range keys {
			if k.Name == "handoff: places" {
				n++
			}
		}
		return n
	}

	if p := page(t, port, cookie); !strings.Contains(p, "Offer places to my photos that have none") {
		t.Fatalf("the card is offered: %.300s", p)
	}
	if c, b := form("/rules/places", url.Values{}); c != 204 {
		t.Fatalf("make the rule: %d %s", c, b)
	}
	if placeKeys() != 1 {
		t.Fatalf("one key for places: %d", placeKeys())
	}
	// the photos reach Immich after the rule: uploaded, with the camera and the time that are in the files (Immich keeps a place written
	// into an uploaded photo; see TestPlacesSpike for a photo of an external library)
	ids := map[string]string{}
	for _, name := range []string{"IMG_3001.jpg", "IMG_3002.jpg", "IMG_3003.jpg", "IMG_3004.jpg", "DSC_4001.jpg"} {
		data, err := os.ReadFile("../external-photos/places/walk/" + name)
		if err != nil {
			t.Fatal(err)
		}
		ids[name], _ = upload(t, bob, name, data)
	}
	eventually(t, "Immich has read the five photos", 120*time.Second, func() bool {
		for _, id := range ids {
			var a struct {
				HasMetadata bool `json:"hasMetadata"`
			}
			call(t, "GET", "/assets/"+id, keyAuth(bob), nil, &a)
			if !a.HasMetadata {
				return false
			}
		}
		return true
	})
	read := func(name string) (lat *float64) {
		var a struct {
			ExifInfo *struct {
				Latitude *float64 `json:"latitude"`
			} `json:"exifInfo"`
		}
		mustOK(t, call(t, "GET", "/assets/"+ids[name], keyAuth(bob), nil, &a), "read "+name)
		if a.ExifInfo != nil {
			lat = a.ExifInfo.Latitude
		}
		return
	}
	// two of them have a place, as a camera with GPS would have given them
	mustOK(t, call(t, "PUT", "/assets", keyAuth(bob), map[string]any{"ids": []string{ids["IMG_3001.jpg"]}, "latitude": 38.7223, "longitude": -9.1393}, nil), "a place for the first")
	mustOK(t, call(t, "PUT", "/assets", keyAuth(bob), map[string]any{"ids": []string{ids["IMG_3003.jpg"]}, "latitude": 38.7230, "longitude": -9.1400}, nil), "a place for the third")

	var rule string
	eventually(t, "the photo between the two is offered", 180*time.Second, func() bool {
		p := page(t, port, cookie)
		m := regexp.MustCompile(`rules/review/([0-9a-f-]{36})`).FindStringSubmatch(p)
		if m == nil || !strings.Contains(p, "1 photo can get a place from a photo next to it") {
			return false
		}
		rule = m[1]
		return true
	})
	if read("IMG_3002.jpg") != nil {
		t.Fatal("a place was written before the answer")
	}
	_, review := getBody(t, port, "/rules/review/"+rule, cookie)
	for _, want := range []string{"Show on a map", "openstreetmap.org/?mlat=38.72230", "The photo the place comes from", "on both sides"} {
		if !strings.Contains(review, want) {
			t.Errorf("the review page lacks %q", want)
		}
	}
	if c, b := form("/rules/answer", url.Values{"rule": {rule}, "do": {"send"}}); c != 204 {
		t.Fatalf("yes: %d %s", c, b)
	}
	eventually(t, "the place is written into the photo", 60*time.Second, func() bool { return read("IMG_3002.jpg") != nil })
	if lat := read("IMG_3002.jpg"); lat == nil || *lat < 38.7222 || *lat > 38.7224 {
		t.Fatalf("the nearer neighbour's place (3 minutes from the first, 6 from the third): %v", lat)
	}
	time.Sleep(15 * time.Second) // a few looks later
	if read("IMG_3004.jpg") != nil || read("DSC_4001.jpg") != nil {
		t.Error("a photo hours from any place, and one of another camera, were left alone")
	}
	if p := page(t, port, cookie); strings.Contains(p, "Give them these places") {
		t.Error("nothing is waiting any more")
	}

	// stopping the rule deletes its key; the place written stays
	if c, b := form("/rules/delete", url.Values{"rule": {rule}}); c != 204 {
		t.Fatalf("stop: %d %s", c, b)
	}
	if placeKeys() != 0 {
		t.Errorf("the key goes with the rule: %d", placeKeys())
	}
	if read("IMG_3002.jpg") == nil {
		t.Error("the place stays")
	}
}
