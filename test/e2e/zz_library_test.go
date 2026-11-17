//go:build e2e

// Named to sort last, like the other zz_ tests: it leaves photos, an album and copies in the shared Immich.
package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// appGet reads a page of the app as a person signed in to Immich would.
func appGet(t *testing.T, port, path, cookie string, out any) int {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://localhost:"+port+path, nil)
	req.Header.Set("Cookie", cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if out != nil && res.StatusCode == 200 {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("GET %s: bad JSON %q", path, b)
		}
	}
	return res.StatusCode
}

// TestAddToMyLibrary: what the person sees but does not own (an album shared with them, a partner's photo) is copied
// into their own library, with the description and the rating, a live photo's clip, to a tag, to an album or as it is,
// a repeat is harmless, Undo takes the copies back out, and the button in Immich's own viewer does it all.
func TestAddToMyLibrary(t *testing.T) {
	w := theWorld(t)
	alice, bob := w.alice, w.bob
	seed := uint32(time.Now().UnixNano())
	up := func(u user, name string, n uint32) string {
		id, _ := upload(t, u, fmt.Sprintf("%s-%d.jpg", name, seed), noiseJPEG(seed+n, 40, 220))
		waitProcessed(t, u, id)
		return id
	}
	inAlbum, partner, own, forButton := up(alice, "lib-album", 1), up(alice, "lib-partner", 2), up(bob, "lib-own", 3), up(alice, "lib-button", 4)
	mustOK(t, call(t, "PUT", "/assets/"+inAlbum, keyAuth(alice), map[string]any{"description": "on the terrace", "rating": 4}, nil), "describe the photo")

	// a live photo of alice's: the hidden clip, then the still that names it
	var still, clip string
	if v := makeVideo(t, 1); v != nil {
		clip = uploadFile(t, alice, fmt.Sprintf("lib-clip-%d.mp4", seed), v, "video/mp4", map[string]string{"visibility": "hidden"})
		still = up(alice, "lib-live", 5)
		mustOK(t, call(t, "PUT", "/assets/"+still, keyAuth(alice), map[string]any{"livePhotoVideoId": clip}, nil), "pair the clip with the still")
	} else {
		t.Log("no ffmpeg: the live photo part is skipped")
	}
	members := []string{inAlbum, forButton}
	if still != "" {
		members = append(members, still)
	}
	var album struct {
		ID string `json:"id"`
	}
	mustOK(t, call(t, "POST", "/albums", keyAuth(alice), map[string]any{"albumName": fmt.Sprintf("Shared %d", seed), "assetIds": members,
		"albumUsers": []map[string]string{{"userId": bob.id, "role": "viewer"}}}, &album), "alice's album, shared with bob")
	call(t, "POST", "/partners/"+bob.id, keyAuth(alice), nil, nil) // alice shares her library with bob (400 when an earlier test did)

	type owned struct{ Mine, Others []string }
	has := func(list []string, id string) bool {
		for _, x := range list {
			if x == id {
				return true
			}
		}
		return false
	}
	ownerOf := func(id string) string {
		var a struct {
			OwnerID string `json:"ownerId"`
		}
		call(t, "GET", "/assets/"+id, keyAuth(bob), nil, &a)
		return a.OwnerID
	}
	add := func(form url.Values) (int, struct {
		Added, Already, Skipped int
		Copies                  []string
		Message                 string
	}) {
		code, body := postBody(t, w.port, "/immich/add", w.sender, form)
		var out struct {
			Added, Already, Skipped int
			Copies                  []string
			Message                 string
		}
		if code == 200 {
			if err := json.Unmarshal([]byte(body), &out); err != nil {
				t.Fatalf("add: bad JSON %q", body)
			}
		} else {
			t.Logf("add: %d %s", code, body)
		}
		return code, out
	}

	t.Run("the app tells whose photos are whose", func(t *testing.T) {
		var got owned
		q := url.Values{"asset": {inAlbum, partner, own}}
		if code := appGet(t, w.port, "/immich/owned?"+q.Encode(), w.sender, &got); code != 200 {
			t.Fatalf("owned: %d", code)
		}
		if !has(got.Mine, own) || len(got.Mine) != 1 || !has(got.Others, inAlbum) || !has(got.Others, partner) {
			t.Errorf("mine %v others %v: the album photo and the partner's are others', the upload is bob's", got.Mine, got.Others)
		}
	})

	var copyOfAlbumPhoto string
	t.Run("a photo from a shared album and a partner's photo become bob's own, tagged with who shared them", func(t *testing.T) {
		code, res := add(url.Values{"dest": {"tag"}, "asset": {inAlbum, partner}})
		if code != 200 || res.Added != 2 || len(res.Copies) != 2 {
			t.Fatalf("add: %d %+v", code, res)
		}
		copyOfAlbumPhoto = res.Copies[0]
		for _, id := range res.Copies {
			if ownerOf(id) != bob.id {
				t.Errorf("copy %s is not bob's", id)
			}
			waitProcessed(t, bob, id)
			eventually(t, "the copy is tagged with who shared it", 30*time.Second, func() bool { return strings.Contains(tagNames(getFull(t, bob, id)), "alice") })
		}
		eventually(t, "the description and rating came along", 30*time.Second, func() bool {
			desc, rating, _, _ := getFull(t, bob, res.Copies[0]).exif()
			return desc == "on the terrace" && rating == "4"
		})
		if ownerOf(inAlbum) == bob.id {
			t.Error("the original must stay alice's")
		}
	})

	t.Run("pressing again changes nothing, and a photo that is bob's own is left alone", func(t *testing.T) {
		if code, res := add(url.Values{"dest": {"tag"}, "asset": {inAlbum}}); code != 200 || res.Added != 0 || res.Already != 1 || len(res.Copies) != 0 {
			t.Errorf("a repeat: %d %+v", code, res)
		}
		if code, res := add(url.Values{"dest": {"plain"}, "asset": {own}}); code != 200 || res.Added != 0 || res.Skipped != 1 {
			t.Errorf("an own photo: %d %+v", code, res)
		}
		if code, _ := add(url.Values{"dest": {"plain"}, "asset": {"00000000-0000-4000-8000-000000000000"}}); code != 409 {
			t.Errorf("a photo that cannot be seen: %d, want 409", code)
		}
	})

	t.Run("the copies can go to an album, made when it is missing", func(t *testing.T) {
		name := fmt.Sprintf("From Alice %d", seed)
		code, res := add(url.Values{"dest": {"album"}, "album": {name}, "asset": {forButton}})
		if code != 200 || res.Added != 1 {
			t.Fatalf("add to an album: %d %+v", code, res)
		}
		var albums []struct {
			ID         string `json:"id"`
			AlbumName  string `json:"albumName"`
			AssetCount int    `json:"assetCount"`
		}
		eventually(t, "bob has the album with the copy", 20*time.Second, func() bool {
			call(t, "GET", "/albums?isOwned=true", keyAuth(bob), nil, &albums)
			for _, a := range albums {
				if a.AlbumName == name && a.AssetCount == 1 {
					return true
				}
			}
			return false
		})
		// undo this one: it is in the album it was sent to, so it may go
		code, body := postBody(t, w.port, "/immich/add-undo", w.sender, url.Values{"asset": {res.Copies[0]}, "album": {name}})
		if code != 200 {
			t.Fatalf("undo: %d %s", code, body)
		}
		eventually(t, "the copy is in the trash", 20*time.Second, func() bool { return getFull(t, bob, res.Copies[0]).IsTrashed })
	})

	if still != "" {
		t.Run("a live photo comes with its clip, linked to bob's own copy of it, and Undo takes both", func(t *testing.T) {
			code, res := add(url.Values{"dest": {"plain"}, "asset": {still}})
			if code != 200 || res.Added != 1 {
				t.Fatalf("add: %d %+v", code, res)
			}
			cp := getFull(t, bob, res.Copies[0])
			if cp.LivePhotoVideoID == nil || *cp.LivePhotoVideoID == clip || ownerOf(*cp.LivePhotoVideoID) != bob.id {
				t.Fatalf("the copy of the still must point at bob's own clip: %v (alice's is %s)", cp.LivePhotoVideoID, clip)
			}
			ownClip := *cp.LivePhotoVideoID
			if code, body := postBody(t, w.port, "/immich/add-undo", w.sender, url.Values{"asset": {res.Copies[0]}}); code != 200 {
				t.Fatalf("undo: %d %s", code, body)
			}
			eventually(t, "the still and its clip are in the trash", 20*time.Second, func() bool {
				return getFull(t, bob, res.Copies[0]).IsTrashed && getFull(t, bob, ownClip).IsTrashed
			})
			if getFull(t, alice, still).IsTrashed || getFull(t, alice, clip).IsTrashed {
				t.Error("alice's originals must never be touched")
			}
		})
	}

	t.Run("Undo refuses what is not bob's and what he has filed in an album of his own", func(t *testing.T) {
		if code, _ := postBody(t, w.port, "/immich/add-undo", w.sender, url.Values{"asset": {inAlbum}}); code != 409 {
			t.Errorf("alice's photo: %d, want 409", code)
		}
		var mine struct {
			ID string `json:"id"`
		}
		mustOK(t, call(t, "POST", "/albums", keyAuth(bob), map[string]any{"albumName": fmt.Sprintf("Kept %d", seed), "assetIds": []string{copyOfAlbumPhoto}}, &mine), "bob files the copy in an album of his own")
		if code, _ := postBody(t, w.port, "/immich/add-undo", w.sender, url.Values{"asset": {copyOfAlbumPhoto}}); code != 409 {
			t.Errorf("a copy bob has filed himself: %d, want 409", code)
		}
		if getFull(t, bob, copyOfAlbumPhoto).IsTrashed {
			t.Error("the filed copy must stay")
		}
	})

	t.Run("in Immich's own viewer a photo that is not bob's has Add to my library, and not Hand over", func(t *testing.T) {
		address := w.inFrontOfImmich(t)
		ctx := browse(t, w.port, w.sender, look{})
		run(t, ctx, chromedp.Navigate(address+"/photos/"+partner))
		until(t, ctx, "Add to my library in the viewer's toolbar", `document.getElementById('handoff-viewer-add') !== null`, 40*time.Second)
		if js[bool](t, ctx, `document.getElementById('handoff-viewer-act') !== null`) {
			t.Error("Hand over must not be offered for a photo that is not bob's")
		}
		// the photo was copied above, so the press answers "already": the point here is the button and the choice
		run(t, ctx, chromedp.Click(`#handoff-viewer-add`, chromedp.ByQuery))
		until(t, ctx, "the choice of where it goes", `document.querySelectorAll('#handoff-menu [role=menuitem]').length === 3`, 10*time.Second)
		run(t, ctx, chromedp.Click(`#handoff-menu [role=menuitem]:nth-of-type(2)`, chromedp.ByQuery)) // in my library, remembered
		until(t, ctx, "the message", `(document.getElementById('handoff-toast') || {textContent: ''}).textContent.includes('Already in your library')`, 20*time.Second)
		// remembered: the next press adds at once
		fresh := up(alice, "lib-fresh", 6) // not copied before: forButton's copy was undone, so it is in the trash
		run(t, ctx, chromedp.Navigate(address+"/photos/"+fresh))
		until(t, ctx, "the button again", `document.getElementById('handoff-viewer-add') !== null`, 40*time.Second)
		run(t, ctx, chromedp.Click(`#handoff-viewer-add`, chromedp.ByQuery))
		until(t, ctx, "the photo added without asking", `(document.getElementById('handoff-toast') || {textContent: ''}).textContent.includes('1 photo added to your library')`, 30*time.Second)
		if js[bool](t, ctx, `document.getElementById('handoff-menu') !== null`) {
			t.Error("once the choice is remembered the press must not ask again")
		}
	})
}
