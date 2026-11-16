package main

import (
	"bytes"
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
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// Scene 7 of the film: inside Immich, the button in the photo viewer, the list, the choice, the message.
func sceneInside(dir string) {
	c := newClip(dir, 1920, 1080, 1, false, false, "bob")
	c.open("/photos")
	c.until("the photos", `document.querySelectorAll('[data-thumbnail-focus-container][data-asset]').length >= 6`, 60)
	c.wait(800)
	c.startFilming()
	c.wait(1200)
	// open a photo with a click on its tile
	c.clickOn(`[data-thumbnail-focus-container] img`, 900)
	c.until("the viewer", `document.getElementById('handoff-viewer-act') !== null`, 30)
	c.wait(1500)
	c.clickOn(`#handoff-viewer-act`, 1100)
	c.until("the list", `document.getElementById('handoff-menu') !== null`, 10)
	c.wait(1400)
	c.clickOn(`#handoff-menu [role=menuitem]`, 800)
	c.until("the message", `document.getElementById('handoff-toast') && /handed over/.test(document.getElementById('handoff-toast').textContent)`, 20)
	c.wait(2500)
	c.stop("c07-inside")
}

// Scene 3: the host makes a drop box and opens its QR code. Short and unhurried: the form is filled where it is (Enter sends it, so the
// page does not scroll to the far button), and the QR code stays on screen at the end.
func sceneHost(dir string) {
	c := newClip(dir, 1920, 1080, 1, false, false, "alice")
	c.open("/handoff/#services")
	c.until("the services", `document.querySelector('#v-services') !== null && document.querySelectorAll('.skeleton').length === 0`, 40)
	c.wait(800)
	c.startFilming()
	c.wait(700)
	c.clickOn(c.mark(`[...document.querySelectorAll('summary')].find(s => s.textContent.trim() === 'Make a drop box')`), 800)
	c.wait(500)
	c.typeInto(`#drop-name`, "Mia's birthday", 45)
	c.wait(250)
	c.typeInto(`#drop-message`, "Send us your photos!", 22)
	c.wait(400)
	c.must("send the form", chromedp.KeyEvent(kb.Enter))
	c.until("the box", `document.querySelector('button.show-qr[data-for^="drop-"]') !== null`, 30)
	c.wait(900)
	for try_ := 0; try_ < 3 && !c.has(`[id^="qr-drop-"]:not([hidden])`); try_++ { // press it again if the first press came before the page was ready
		c.clickOn(`button.show-qr[data-for^="drop-"]`, 800)
		for i := 0; i < 30 && !c.has(`[id^="qr-drop-"]:not([hidden])`); i++ {
			c.wait(200)
		}
	}
	c.until("the code", `document.querySelector('[id^="qr-drop-"]:not([hidden])') !== null`, 10)
	c.wait(2600)
	c.stop("c03-host")
}

// ---- shared by the event scenes ----

// photoPath is the demo photo whose file name starts with the number.
func photoPath(n int) string {
	dir, _ := filepath.Abs(env("DEMO_PHOTOS", "../../demo/photos"))
	m, _ := filepath.Glob(filepath.Join(dir, fmt.Sprintf("%02d-*.jpg", n)))
	if len(m) == 0 {
		log.Fatalf("no demo photo %d in %s", n, dir)
	}
	return m[0]
}

// dropToken is the guest link's token of the host's drop box (made by the host scene).
func dropToken() string {
	_, page := appRequest("alice", "GET", "/", nil)
	m := regexp.MustCompile(`drop/([0-9a-f]{32,})`).FindStringSubmatch(page)
	if m == nil {
		log.Fatal("Alice has no drop box: film the host scene first")
	}
	return m[1]
}

// guestUpload sends photos to the drop box as a guest would, in one request.
func guestUpload(token string, nums ...int) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, n := range nums {
		p := photoPath(n)
		fw, _ := mw.CreateFormFile("files", filepath.Base(p))
		b, _ := os.ReadFile(p)
		fw.Write(b)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", demoBase()+"/handoff/drop/"+token+"/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Requested-With", "immich-handoff")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode >= 300 {
		log.Fatalf("the guest's upload: HTTP %d", res.StatusCode)
	}
}

// Scene 4: the guest, on a phone: the link, three photos chosen, Send.
func sceneGuest(dir string) {
	token := dropToken()
	c := newClip(dir, 390, 844, 2, true, false, "")
	c.open("/handoff/drop/" + token)
	c.until("the form", `document.getElementById('drop-files') !== null`, 30)
	c.wait(1200)
	c.startFilming()
	c.wait(1500)
	c.glide(200, 640, 600)
	c.must("choose photos", chromedp.SetUploadFiles(`#drop-files`, []string{photoPath(21), photoPath(24), photoPath(19)}, chromedp.ByQuery))
	c.until("the list", `document.querySelectorAll('.drop-list li').length >= 3`, 20)
	c.wait(2000)
	c.clickOn(`.drop-send`, 900)
	c.until("the result", `(document.querySelector('.drop-result') || {}).hidden === false`, 60)
	c.wait(3000)
	c.stop("c04-guest")
}

// Scene 5: the photos arrive: the host's Overview while guests send (a lane for the drop box, the drop flying in, the total
// going up with a +N over it), then Immich's timeline with them in it.
func sceneArrive(dir string) {
	token := dropToken()
	c := newClip(dir, 1920, 1080, 1, false, true, "alice")
	c.open("/handoff/")
	c.until("the box's stage", `document.querySelector('[data-stage][data-kind="drop"]') !== null`, 40)
	c.eval(`document.querySelector('[data-stage][data-kind="drop"]').scrollIntoView({block: 'center'})`)
	c.wait(1800)
	c.startFilming()
	c.wait(2500)
	go guestUpload(token, 23, 38, 39) // while the camera runs, a guest sends three photos
	c.wait(5500)
	go guestUpload(token, 40, 41)
	c.wait(5000)
	// then Immich's own timeline
	c.open("/photos")
	c.until("the timeline", `document.querySelectorAll('[data-thumbnail-focus-container] img').length >= 6 && [...document.querySelectorAll('[data-thumbnail-focus-container] img')].every(i => i.complete && i.naturalWidth > 0)`, 60)
	c.wait(3000)
	c.stop("c05-arrive")
}

// wallLink starts the host's wall (once) and says where it is.
func wallLink() string {
	_, page := appRequest("alice", "GET", "/", nil)
	if m := regexp.MustCompile(`wall/([0-9a-f]{20,})`).FindStringSubmatch(page); m != nil {
		return m[1]
	}
	id := regexp.MustCompile(`(?s)hx-post="dropbox/wall"[^>]*?service(?:"|&#34;):(?:"|&#34;)([0-9A-Za-z-]+)`).FindStringSubmatch(page)
	if id == nil {
		log.Fatal("the wall's button is not on Alice's page: film the host scene first")
	}
	if code, body := appRequest("alice", "POST", "/dropbox/wall", url.Values{"service": {id[1]}}); code >= 300 {
		log.Fatalf("start the wall: %d %s", code, body)
	}
	_, page = appRequest("alice", "GET", "/", nil)
	m := regexp.MustCompile(`wall/([0-9a-f]{20,})`).FindStringSubmatch(page)
	if m == nil {
		log.Fatal("the wall has no link")
	}
	return m[1]
}

// Scene 6: the wall on a screen while guests send photos.
func sceneWall(dir string) {
	token := dropToken()
	wall := wallLink()
	guestUpload(token, 5, 14) // a few are there already, so the wall starts with something
	c := newClip(dir, 1920, 1080, 1, false, true, "")
	c.open("/handoff/wall/" + wall)
	c.until("the wall", `document.body !== null && document.querySelectorAll('img').length >= 1`, 60)
	c.wait(4000)
	c.startFilming()
	c.wait(3000)
	go func() { // photos arrive while the camera runs
		guestUpload(token, 15)
		time.Sleep(9 * time.Second)
		guestUpload(token, 22)
	}()
	c.wait(24000)
	c.stop("c06-wall")
}

// immichUpload puts a demo photo in a person's own library, as their phone would.
func immichUpload(who string, n int) {
	body, _ := json.Marshal(map[string]string{"email": who + "@demo.test", "password": "Demo-pass-1"})
	res, err := http.Post(demoBase()+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Fatal(err)
	}
	var login struct {
		AccessToken string `json:"accessToken"`
	}
	json.NewDecoder(res.Body).Decode(&login)
	res.Body.Close()
	p := photoPath(n)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	mw.WriteField("deviceAssetId", filepath.Base(p)+"-"+now)
	mw.WriteField("deviceId", "film")
	mw.WriteField("fileCreatedAt", now)
	mw.WriteField("fileModifiedAt", now)
	fw, _ := mw.CreateFormFile("assetData", filepath.Base(p))
	b, _ := os.ReadFile(p)
	fw.Write(b)
	mw.Close()
	req, _ := http.NewRequest("POST", demoBase()+"/api/assets", &buf)
	req.Header.Set("Authorization", "Bearer "+login.AccessToken)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	io.Copy(io.Discard, r2.Body)
	r2.Body.Close()
	if r2.StatusCode >= 300 {
		log.Fatalf("upload for %s: HTTP %d", who, r2.StatusCode)
	}
}

// ruleprep makes the rule and puts the photos it will find in Bob's library. It is done at the very start of a capture, so that Immich has
// had time to read the photos (and the rule to find them) by the time the scene is filmed.
func ruleprep(string) {
	today := time.Now().UTC().Format("2006-01-02")
	if code, body := appRequest("bob", "POST", "/rules", url.Values{"service": {"Family album"}, "after": {today}, "before": {today}}); code >= 300 {
		log.Fatalf("make the rule: %d %s", code, body)
	}
	time.Sleep(2 * time.Second)
	for _, n := range []int{12, 8, 31} { // a cabin in the snow, a lake, a city at night: photos that arrive after the rule
		immichUpload("bob", n)
	}
}

// Scene 8: a rule suggests photos on the Overview and the person sends them (ruleprep made the rule).
func sceneRule(dir string) {
	c := newClip(dir, 1920, 1080, 1, false, false, "bob")
	c.open("/handoff/")
	c.until("the suggestion", `document.querySelector('.card.need .thumbs img') !== null`, 300)
	c.wait(1500)
	c.eval(`document.querySelector('.card.need').scrollIntoView({block: 'center'})`)
	c.wait(1200)
	c.startFilming()
	c.wait(2500)
	c.clickOn(c.mark(`[...document.querySelectorAll('.card.need button')].find(b => b.textContent.trim().startsWith('Send'))`), 1100)
	c.wait(3500)
	c.stop("c08-rule")
}

// Scene 9: a client room: the host makes it from an album, a client opens it on a phone and chooses favourites.
func sceneClient(dir string) {
	// the album of Alice's trip is the album "Holiday"
	body, _ := json.Marshal(map[string]string{"email": "alice@demo.test", "password": "Demo-pass-1"})
	res, err := http.Post(demoBase()+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Fatal(err)
	}
	var login struct {
		AccessToken string `json:"accessToken"`
	}
	json.NewDecoder(res.Body).Decode(&login)
	res.Body.Close()
	req, _ := http.NewRequest("GET", demoBase()+"/api/albums", nil)
	req.Header.Set("Authorization", "Bearer "+login.AccessToken)
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	var albums []struct{ ID, AlbumName string }
	json.NewDecoder(r2.Body).Decode(&albums)
	r2.Body.Close()
	album := ""
	for _, a := range albums {
		if a.AlbumName == "Holiday" {
			album = a.ID
		}
	}
	if album == "" {
		log.Fatal("Alice has no album Holiday")
	}
	if code, out := appRequest("alice", "POST", "/rooms", url.Values{"name": {"Innsbruck trip"}, "title": {"Mia and Tom, a week in Innsbruck"}, "album": {album}, "mark": {"text"},
		"marktext": {"Alice Demo Photography"}, "pickmode": {"shared"}, "picklimit": {"3"}, "notes": {"replies"}, "rounds": {"one"}, "ends": {"90"}, "releasedays": {"30"}}); code >= 300 {
		log.Fatalf("make the room: %d %s", code, out)
	}
	_, page := appRequest("alice", "GET", "/", nil)
	m := regexp.MustCompile(`data-link="room/([0-9a-f]{64})"`).FindStringSubmatch(page)
	if m == nil {
		log.Fatal("the room's link is not on Alice's page")
	}
	c := newClip(dir, 390, 844, 2, true, false, "")
	c.open("/handoff/room/" + m[1])
	c.until("the room", `document.querySelector('[data-room]') !== null || document.getElementById('room-name') !== null`, 40)
	c.wait(800)
	if c.has(`#room-name`) {
		c.typeInto(`#room-name`, "Mia", 1)
		c.eval(`document.querySelector('#room-name').closest('form').requestSubmit()`)
		c.until("the photos", `document.querySelector('a.room-tile') !== null`, 40)
	}
	c.until("the pictures", `[...document.querySelectorAll('a.room-tile img')].every(i => i.complete && i.naturalWidth > 0) && document.querySelectorAll('a.room-tile').length >= 4`, 60)
	c.wait(1500)
	c.startFilming()
	c.wait(1800)
	for _, i := range []int{1, 3, 4} {
		c.clickOn(c.mark(fmt.Sprintf(`document.querySelectorAll('li.room-item')[%d].querySelector('button.room-heart')`, i)), 900)
		c.wait(1100)
	}
	c.wait(2500)
	c.stop("c09-client")
}

// has says whether the page has an element.
func (c *clip) has(sel string) bool {
	var ok bool
	chromedp.Run(c.ctx, chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q) !== null`, sel), &ok))
	return ok
}
