package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// wallScene makes what the wall's animation needs: as Bob, a drop box and a wall on it, and a few photos sent through the
// box's link the way a guest sends them. It waits until the wall lists them, and returns the wall's address and the
// steps that send one more photo while the page is filmed (a new arrival) and try the styles.
func wallScene() (string, []step) {
	cookies := ""
	for _, c := range loginCookies("bob@test.local") {
		cookies += c.Name + "=" + c.Value + "; "
	}
	call := func(method, path string, body io.Reader, ctype string) (int, string) {
		req, err := http.NewRequest(method, appURL+path, body)
		if err != nil {
			log.Fatal(err)
		}
		req.Header.Set("Cookie", cookies)
		req.Header.Set("X-Requested-With", "immich-handoff")
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	form := func(path string, v url.Values) (int, string) {
		return call("POST", path, strings.NewReader(v.Encode()), "application/x-www-form-urlencoded")
	}
	if code, body := form("dropbox", url.Values{"name": {"Summer wedding"}, "opendays": {"30"}, "message": {"Share your photos of the day."}}); code >= 300 {
		log.Fatalf("make the drop box: %d %s", code, body)
	}
	_, page := call("GET", "", nil, "")
	id := regexp.MustCompile(`(?s)hx-post="dropbox/wall"[^>]*?service(?:"|&#34;):(?:"|&#34;)([0-9A-Za-z-]+)`).FindStringSubmatch(page)
	if id == nil {
		log.Fatal("the wall's button is not on Bob's page")
	}
	if code, body := form("dropbox/wall", url.Values{"service": {id[1]}}); code >= 300 {
		log.Fatalf("start the wall: %d %s", code, body)
	}
	_, page = call("GET", "", nil, "")
	wall := regexp.MustCompile(`wall/([0-9a-f]{20,})`).FindStringSubmatch(page)
	drop := regexp.MustCompile(`drop/([0-9a-f]{32,})`).FindStringSubmatch(page)
	if wall == nil || drop == nil {
		log.Fatalf("the links are not on Bob's page (wall %v, drop %v)", wall != nil, drop != nil)
	}
	photos, _ := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".local/share/immich-handoff-shots/photos", "*.jpg"))
	sort.Strings(photos)
	if len(photos) < 10 {
		log.Fatal("no sample photos: run `go run . gen` first")
	}
	photos = photos[len(photos)-8:] // the last ones, which nothing else used
	next := 0
	send := func() {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		f := photos[next%len(photos)]
		next++
		data, _ := os.ReadFile(f)
		fw, _ := mw.CreateFormFile("files", filepath.Base(f))
		fw.Write(data)
		mw.Close()
		req, _ := http.NewRequest("POST", appURL+"drop/"+drop[1]+"/upload", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("X-Requested-With", "immich-handoff")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("send a photo: %v", err)
			return
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
	}
	for i := 0; i < 3; i++ {
		send()
	}
	// wait until the wall lists them (Immich has to take them in and the box tags them)
	feed := appURL + "wall/" + wall[1] + "/feed"
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		res, err := http.Get(feed)
		if err == nil {
			var out struct{ Items []struct{ ID int } }
			json.NewDecoder(res.Body).Decode(&out)
			res.Body.Close()
			if len(out.Items) >= 3 {
				break
			}
		}
		time.Sleep(3 * time.Second)
	}
	wake := js(`document.dispatchEvent(new Event('mousemove'))`)
	steps := []step{
		{3, func(context.Context) { send() }}, // a photo arrives while the wall is on screen
		{13, wake}, {13.4, click(`button[data-fx="zoom"]`)},
		{19, wake}, {19.4, click(`button[data-fx="slide"]`)},
		{25, wake}, {25.4, click(`button[data-mode="grid"]`)},
		{31, wake}, {31.4, click(`button[data-mode="collage"]`)},
	}
	return fmt.Sprintf("%swall/%s", appURL, wall[1]), steps
}
