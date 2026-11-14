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
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// roomShots takes the pictures of a client room: the photographer's row, the client's page on a phone with two photos chosen, a
// photo larger with a note, and the review page. It makes the room itself: Alice uploads six sample photos, makes an album of them
// and a room of the album through the app, and a client chooses and writes a note, all by the same requests the pages make.
//
//	go run . room <outdir> <photo folder>      (FIRST moves on to other sample photos, as for `send`)
func roomShots(outdir, photos string) {
	os.MkdirAll(outdir, 0o755)
	first := 2060
	fmt.Sscan(env("FIRST", "2060"), &first)
	token := alice()
	var ids []string
	for i := 0; i < 6; i++ {
		p := filepath.Join(photos, fmt.Sprintf("IMG_%04d.jpg", first+i))
		b, err := os.ReadFile(p)
		if err != nil {
			log.Fatalf("sample photo %s: %v (run `go run . gen <dir> 40` first)", p, err)
		}
		ids = append(ids, upload(token, filepath.Base(p), b, time.Date(2026, 6, 14, 15, i*7, 0, 0, time.UTC)))
	}
	for _, id := range ids {
		waitProcessed(token, id)
	}
	album := apiJSON(token, "POST", "/api/albums", map[string]any{"albumName": "Rossi wedding", "assetIds": ids})["id"].(string)

	cookies := loginCookies("alice@test.local")
	owner := ""
	for _, c := range cookies {
		owner += c.Name + "=" + c.Value + "; "
	}
	owner = strings.TrimSuffix(owner, "; ")
	post := func(path string, form url.Values) {
		req, _ := http.NewRequest("POST", strings.TrimSuffix(appURL, "/")+path, strings.NewReader(form.Encode()))
		req.Header.Set("Cookie", owner)
		req.Header.Set("X-Requested-With", "immich-handoff")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode >= 300 {
			log.Fatalf("%s: %d %s", path, res.StatusCode, b)
		}
	}
	post("/rooms", url.Values{"name": {"Rossi wedding"}, "title": {"Chiara and Luca, 14 June"}, "album": {album}, "mark": {"text"}, "marktext": {"Chiara Bianchi Photography"},
		"pickmode": {"shared"}, "picklimit": {"3"}, "notes": {"replies"}, "rounds": {"one"}, "ends": {"90"}, "releasedays": {"30"}})
	home := getText(owner, strings.TrimSuffix(appURL, "/")+"/")
	link := regexp.MustCompile(`data-link="room/([0-9a-f]{64})"`).FindStringSubmatch(home)
	roomID := regexp.MustCompile(`(?:&#34;|")room(?:&#34;|"):(?:&#34;|")([0-9a-f-]{36})`).FindStringSubmatch(home)
	if link == nil || roomID == nil {
		log.Fatal("the room is not on Alice's page")
	}
	roomToken := link[1]

	// the client: a browser with its own cookies
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	clientDo := func(method, path, body string) string {
		req, _ := http.NewRequest(method, strings.TrimSuffix(appURL, "/")+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		res, err := client.Do(req)
		if err != nil {
			log.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	clientDo("POST", "/room/"+roomToken+"/name", "name=Chiara")
	page := getText("", strings.TrimSuffix(appURL, "/")+"/room/"+roomToken)
	_ = page
	var photoIDs []string
	req, _ := http.NewRequest("GET", strings.TrimSuffix(appURL, "/")+"/room/"+roomToken, nil)
	if res, err := client.Do(req); err == nil {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		seen := map[string]bool{}
		for _, m := range regexp.MustCompile(`room/`+roomToken+`/pick/([0-9a-f]{16})`).FindAllStringSubmatch(string(b), -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				photoIDs = append(photoIDs, m[1])
			}
		}
	}
	if len(photoIDs) < 4 {
		log.Fatalf("the room shows %d photos", len(photoIDs))
	}
	clientDo("POST", "/room/"+roomToken+"/pick/"+photoIDs[1], "")
	clientDo("POST", "/room/"+roomToken+"/pick/"+photoIDs[3], "")
	clientDo("POST", "/room/"+roomToken+"/note/"+photoIDs[1], "text="+url.QueryEscape("Lovely. Could you brighten this one a little?"))

	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("no-sandbox", true), chromedp.Flag("hide-scrollbars", true), chromedp.Flag("disable-gpu", true))
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelA()
	var buf []byte
	save := func(name string) {
		if err := os.WriteFile(filepath.Join(outdir, name+".png"), buf, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%-28s %6d KB\n", name+".png", len(buf)/1024)
	}
	session := func(width, height int, mobile bool, who []*network.CookieParam, jarCookies bool) (context.Context, context.CancelFunc) {
		ctx, cancel := chromedp.NewContext(actx)
		ctx, cancelT := context.WithTimeout(ctx, 2*time.Minute)
		tasks := chromedp.Tasks{emulation.SetDeviceMetricsOverride(int64(width), int64(height), 1.5, mobile), emulation.SetTouchEmulationEnabled(mobile), inEnglish(),
			chromedp.ActionFunc(func(ctx context.Context) error {
				return emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: "light"}}).Do(ctx)
			})}
		for _, c := range who {
			c := c
			tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
				return network.SetCookie(c.Name, c.Value).WithDomain(c.Domain).WithPath(c.Path).WithHTTPOnly(c.HTTPOnly).Do(ctx)
			}))
		}
		if jarCookies {
			u, _ := url.Parse(appURL)
			for _, c := range jar.Cookies(u) {
				c := c
				tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
					return network.SetCookie(c.Name, c.Value).WithDomain("localhost").WithPath("/").Do(ctx)
				}))
			}
		}
		if err := chromedp.Run(ctx, tasks); err != nil {
			log.Fatal(err)
		}
		return ctx, func() { cancelT(); cancel() }
	}
	must := func(what string, ctx context.Context, a ...chromedp.Action) {
		if err := chromedp.Run(ctx, a...); err != nil {
			log.Fatalf("%s: %v", what, err)
		}
	}
	loaded := `Array.from(document.querySelectorAll('.room-tile img')).every(i => i.complete && i.naturalWidth > 0)`

	// the client's page on a phone, two photos chosen, and one photo larger with its note
	ctx, done := session(390, 844, true, nil, true)
	must("client page", ctx, chromedp.Navigate(appURL+"room/"+roomToken), chromedp.WaitVisible(`.room-grid`, chromedp.ByQuery), chromedp.Poll(loaded, nil, chromedp.WithPollingTimeout(60*time.Second)), chromedp.Sleep(800*time.Millisecond), chromedp.CaptureScreenshot(&buf))
	save("room-client")
	must("large", ctx, chromedp.Click(`.room-grid li:nth-child(2) a.room-tile`, chromedp.ByQuery), chromedp.Sleep(1200*time.Millisecond), chromedp.CaptureScreenshot(&buf))
	save("room-large")
	done()

	// the client sends the choice; the photographer answers the note
	clientDo("POST", "/room/"+roomToken+"/done", "")
	post("/rooms/reply", url.Values{"room": {roomID[1]}, "who": {""}, "photo": {photoIDs[1]}, "text": {"Of course, I will lift the shadows."}})

	// the photographer's row and the review page
	ctx, done = session(1280, 900, false, cookies, false)
	must("row", ctx, chromedp.Navigate(appURL+"#services"), chromedp.WaitVisible(`#v-services`, chromedp.ByQuery), chromedp.Sleep(1500*time.Millisecond),
		chromedp.Evaluate(`(function(){ const d = [...document.querySelectorAll('#v-services details.svc')].find(x => x.textContent.includes('room of')); if (d) d.open = true })()`, nil), chromedp.Sleep(400*time.Millisecond),
		screenshotElement(`//details[contains(@class,'svc')][contains(.,'room of')]`, 16, &buf))
	save("room-row")
	must("review", ctx, chromedp.Navigate(appURL+"rooms/"+roomID[1]+"/review"), chromedp.WaitVisible(`.room-grid`, chromedp.ByQuery), chromedp.Sleep(6*time.Second), chromedp.CaptureScreenshot(&buf))
	save("room-review")
	done()
}

func alice() string {
	return loginToken("alice@test.local")
}

func loginToken(email string) string {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	res, err := http.Post(immichURL+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		AccessToken string `json:"accessToken"`
	}
	json.NewDecoder(res.Body).Decode(&out)
	if out.AccessToken == "" {
		log.Fatalf("login as %s failed", email)
	}
	return out.AccessToken
}

func apiJSON(token, method, path string, body any) map[string]any {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, immichURL+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		log.Fatalf("%s %s: %d %s", method, path, res.StatusCode, data)
	}
	json.Unmarshal(data, &out)
	return out
}

func upload(token, name string, data []byte, at time.Time) string {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("deviceAssetId", name+fmt.Sprint(at.UnixNano()))
	mw.WriteField("deviceId", "screenshots")
	mw.WriteField("fileCreatedAt", at.Format(time.RFC3339))
	mw.WriteField("fileModifiedAt", at.Format(time.RFC3339))
	fw, _ := mw.CreateFormFile("assetData", name)
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest("POST", immichURL+"/api/assets", &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()
	var out struct{ ID string }
	b, _ := io.ReadAll(res.Body)
	json.Unmarshal(b, &out)
	if out.ID == "" {
		log.Fatalf("upload %s: %d %s", name, res.StatusCode, b)
	}
	return out.ID
}

func waitProcessed(token, id string) {
	for i := 0; i < 120; i++ {
		req, _ := http.NewRequest("GET", immichURL+"/api/assets/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		if res, err := http.DefaultClient.Do(req); err == nil {
			var a struct {
				HasMetadata bool           `json:"hasMetadata"`
				ExifInfo    map[string]any `json:"exifInfo"`
				Thumbhash   *string        `json:"thumbhash"`
			}
			json.NewDecoder(res.Body).Decode(&a)
			res.Body.Close()
			if a.HasMetadata && a.ExifInfo != nil && a.Thumbhash != nil {
				return
			}
		}
		time.Sleep(time.Second)
	}
	log.Fatalf("Immich did not finish %s", id)
}

func getText(cookie, u string) string {
	req, _ := http.NewRequest("GET", u, nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return string(b)
}
