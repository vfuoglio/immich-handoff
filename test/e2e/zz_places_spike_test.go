//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"
)

// TestPlacesSpike writes down, on the real Immich, what writing and clearing a photo's place does (the facts docs/contributing/design/places-design.md
// stands on): that a place can be written, that Immich finds the city and country itself, and whether a place can be cleared again with a
// null. It fails only on what the feature cannot work without: writing a place.
func TestPlacesSpike(t *testing.T) {
	_, bob, _, _ := bootstrap(t)
	id, _ := upload(t, bob, "place-spike.jpg", texturedJPEG(77))
	read := func() (lat, lon *float64, city, country string) {
		var a struct {
			ExifInfo *struct {
				Latitude  *float64 `json:"latitude"`
				Longitude *float64 `json:"longitude"`
				City      *string  `json:"city"`
				Country   *string  `json:"country"`
			} `json:"exifInfo"`
		}
		mustOK(t, call(t, "GET", "/assets/"+id, keyAuth(bob), nil, &a), "read the photo")
		if a.ExifInfo == nil {
			return
		}
		lat, lon = a.ExifInfo.Latitude, a.ExifInfo.Longitude
		if a.ExifInfo.City != nil {
			city = *a.ExifInfo.City
		}
		if a.ExifInfo.Country != nil {
			country = *a.ExifInfo.Country
		}
		return
	}
	eventually(t, "Immich has read the photo", 60*time.Second, func() bool {
		var a struct {
			HasMetadata bool `json:"hasMetadata"`
		}
		call(t, "GET", "/assets/"+id, keyAuth(bob), nil, &a)
		return a.HasMetadata
	})
	if lat, lon, _, _ := read(); lat != nil || lon != nil {
		t.Fatalf("the sample photo has no place to begin with: %v %v", lat, lon)
	}

	// writing, in bulk (the call the app will use)
	code := call(t, "PUT", "/assets", keyAuth(bob), map[string]any{"ids": []string{id}, "latitude": 38.7223, "longitude": -9.1393}, nil)
	t.Logf("PUT /assets with a place: HTTP %d", code)
	if code >= 300 {
		t.Fatalf("a place cannot be written in bulk: %d", code)
	}
	var city, country string
	eventually(t, "the place is in the photo", 30*time.Second, func() bool {
		lat, lon, _, _ := read()
		return lat != nil && lon != nil
	})
	eventually(t, "Immich finds the city and country itself", 90*time.Second, func() bool {
		_, _, city, country = read()
		return city != "" || country != ""
	})
	lat, lon, _, _ := read()
	t.Logf("after the write: %.4f,%.4f in %q, %q", *lat, *lon, city, country)

	// clearing: a null, one photo, then in bulk
	code = call(t, "PUT", "/assets/"+id, keyAuth(bob), map[string]any{"latitude": nil, "longitude": nil}, nil)
	t.Logf("PUT /assets/{id} with null: HTTP %d", code)
	time.Sleep(3 * time.Second)
	lat, lon, city, country = read()
	t.Logf("after a null on one photo: lat=%v lon=%v city=%q country=%q", lat, lon, city, country)
	if lat != nil {
		code = call(t, "PUT", "/assets", keyAuth(bob), map[string]any{"ids": []string{id}, "latitude": nil, "longitude": nil}, nil)
		t.Logf("PUT /assets with null in bulk: HTTP %d", code)
		time.Sleep(3 * time.Second)
		lat, lon, city, country = read()
		t.Logf("after a null in bulk: lat=%v lon=%v city=%q country=%q", lat, lon, city, country)
	}
	t.Logf("%s", fmt.Sprintf("RESULT: cleared=%v (a place %s be taken back with a null)", lat == nil && lon == nil, map[bool]string{true: "can", false: "cannot"}[lat == nil && lon == nil]))

	// a photo of an external library (read-only files): is a written place kept?
	externalLibraryAt(t, bob, []string{"/external/places"}, 5)
	var ext string
	eventually(t, "Immich has read an external photo", 120*time.Second, func() bool {
		var res struct {
			Assets struct {
				Items []struct {
					ID               string `json:"id"`
					OriginalFileName string `json:"originalFileName"`
					HasMetadata      bool   `json:"hasMetadata"`
				}
			}
		}
		call(t, "POST", "/search/metadata", keyAuth(bob), map[string]any{"originalPath": "/external/places/"}, &res)
		for _, i := range res.Assets.Items {
			if i.OriginalFileName == "IMG_3001.jpg" && i.HasMetadata {
				ext = i.ID
			}
		}
		return ext != ""
	})
	id = ext
	code = call(t, "PUT", "/assets", keyAuth(bob), map[string]any{"ids": []string{id}, "latitude": 38.7223, "longitude": -9.1393}, nil)
	time.Sleep(30 * time.Second)
	lat, lon, city, country = read()
	t.Logf("a photo of an external library: PUT /assets HTTP %d, 30 s later lat=%v lon=%v city=%q country=%q", code, lat, lon, city, country)
	t.Logf("RESULT: external library place kept=%v", lat != nil)
}
