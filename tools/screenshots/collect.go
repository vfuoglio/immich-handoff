package main

import (
	"bytes"
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
)

// collectShots takes the pictures of the drop box guides: the form that makes a box, the guest page and its gallery, a box that waits for
// approval with the moderator page, the wall and the printable card. Alice makes two boxes through the same requests the page makes (a
// birthday with a gallery, and a school trip she approves), guests send sample photos and heart some, and the pictures are taken.
//
//	go run . collect <outdir> <photo folder>      (needs the sample photos from `go run . gen <dir> 80`)
func collectShots(outdir, photos string) {
	os.MkdirAll(outdir, 0o755)
	base := strings.TrimSuffix(appURL, "/")
	owner := ""
	for _, c := range loginCookies("alice@test.local") {
		owner += c.Name + "=" + c.Value + "; "
	}
	owner = strings.TrimSuffix(owner, "; ")
	post := func(path string, form url.Values) {
		req, _ := http.NewRequest("POST", base+path, strings.NewReader(form.Encode()))
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
	// the boxes on Alice's page: token and id of each, told apart by what was there before
	boxes := func() map[string]string {
		home := getText(owner, base+"/")
		out := map[string]string{}
		for _, m := range regexp.MustCompile(`data-link="drop/([0-9a-f]{64})"`).FindAllStringSubmatch(home, -1) {
			out[m[1]] = ""
		}
		ids := regexp.MustCompile(`(?:&#34;|")service(?:&#34;|"):(?:&#34;|")([0-9a-f-]{36})`).FindAllStringSubmatch(home, -1)
		_ = ids
		return out
	}
	idOf := func(token string) string { // the row that holds this link also holds the box's id
		home := getText(owner, base+"/")
		i := strings.Index(home, `data-link="drop/`+token+`"`)
		if i < 0 {
			log.Fatal("the box is not on Alice's page")
		}
		rest := home[i:]
		m := regexp.MustCompile(`(?:&#34;|")service(?:&#34;|"):(?:&#34;|")([0-9a-f-]{36})`).FindStringSubmatch(rest)
		if m == nil {
			log.Fatal("no id for the box")
		}
		return m[1]
	}
	newToken := func(before map[string]string) string {
		for i := 0; i < 20; i++ {
			for t := range boxes() {
				if _, old := before[t]; !old {
					return t
				}
			}
			time.Sleep(time.Second)
		}
		log.Fatal("the new box did not appear")
		return ""
	}

	png := func(n int) []byte {
		b, err := os.ReadFile(filepath.Join(photos, fmt.Sprintf("IMG_%04d.jpg", n)))
		if err != nil {
			log.Fatalf("sample photo %d: %v (run `go run . gen <dir> 80` first)", n, err)
		}
		return b
	}
	send := func(c *http.Client, token, guest string, n int) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("guest", guest)
		fw, _ := mw.CreateFormFile("files", fmt.Sprintf("IMG_%04d.jpg", n))
		fw.Write(png(n))
		mw.Close()
		req, _ := http.NewRequest("POST", base+"/drop/"+token+"/upload", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Accept", "application/json")
		res, err := c.Do(req)
		if err != nil {
			log.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.Contains(string(b), `"ok":true`) {
			log.Fatalf("a guest's upload: %d %s", res.StatusCode, b)
		}
	}
	guest := func() *http.Client { jar, _ := cookiejar.New(nil); return &http.Client{Jar: jar} }

	// the birthday: open, with a gallery guests may heart and download from, showing names
	before := boxes()
	post("/dropbox", url.Values{"name": {"Mia's birthday"}, "message": {"Thank you for coming! Add your photos here."}, "days": {"30"},
		"gallery": {"1"}, "gallerydownload": {"1"}, "gallerynames": {"1"}})
	birthday := newToken(before)
	birthdayID := idOf(birthday)
	ann, marco := guest(), guest()
	for i, n := range []int{2062, 2068, 2074, 2080, 2086, 2092} {
		if i%2 == 0 {
			send(ann, birthday, "Ann", n)
		} else {
			send(marco, birthday, "Marco", n)
		}
	}
	time.Sleep(8 * time.Second) // Immich takes the photos in
	feed := getText("", base+"/drop/"+birthday+"/gallery")
	for i, m := range regexp.MustCompile(`"id":(\d+)`).FindAllStringSubmatch(feed, -1) {
		if i%3 != 2 {
			c := ann
			if i%2 == 0 {
				c = marco
			}
			req, _ := http.NewRequest("POST", base+"/drop/"+birthday+"/heart/"+m[1], nil)
			if res, err := c.Do(req); err == nil {
				res.Body.Close()
			}
		}
	}
	post("/dropbox/wall", url.Values{"service": {birthdayID}, "renew": {"0"}})
	var wall string
	for i := 0; i < 20 && wall == ""; i++ {
		if m := regexp.MustCompile(`data-link="wall/([0-9a-f]+)"`).FindStringSubmatch(getText(owner, base+"/")); m != nil {
			wall = m[1]
		}
		time.Sleep(time.Second)
	}
	if wall == "" {
		log.Fatal("the wall did not start")
	}

	// the school trip: Alice approves every file, so they wait
	before = boxes()
	post("/dropbox", url.Values{"name": {"School trip"}, "message": {"Send your photos from the trip."}, "days": {"30"}, "approve": {"1"}, "photos": {"1"}})
	trip := newToken(before)
	tripID := idOf(trip)
	lena := guest()
	for _, n := range []int{2098, 2104, 2110, 2116} {
		send(lena, trip, "Lena", n)
	}
	post("/dropbox/moderator", url.Values{"service": {tripID}, "on": {"1"}})
	var mod string
	for i := 0; i < 20 && mod == ""; i++ {
		if m := regexp.MustCompile(`data-link="mod/([0-9a-f]+)"`).FindStringSubmatch(getText(owner, base+"/")); m != nil {
			mod = m[1]
		}
		time.Sleep(time.Second)
	}

	// the rows are an accordion (one open at a time): open the first that matches
	row := `{ const d = [...document.querySelectorAll('#seg-dropboxes details.svc')].find(x => x.textContent.includes('%s')); if (d) d.open = true }`
	tab := storyHideBar + `document.querySelector('nav.tabs a[data-view="services"]').click(); const s = document.querySelector('.seg [data-seg="dropboxes"]'); if (s) s.click();`
	take(shot{name: "dropbox-form", who: "alice@test.local", url: appURL + "#services", width: 1280, height: 900, wait: `document.readyState==='complete'`, pause: 3 * time.Second, after: time.Second,
		js:    tab + `document.querySelectorAll('#seg-dropboxes details.svc').forEach(d => d.open = false); const o = [...document.querySelectorAll('#seg-dropboxes details.offer')].find(d => d.textContent.includes('Make a drop box')); o.open = true;`,
		xpath: `//*[@id='seg-dropboxes']//details[contains(@class,'offer')][1]`, pad: 16}, outdir)
	take(shot{name: "dropbox-row", who: "alice@test.local", url: appURL + "#services", width: 1280, height: 900, wait: `document.readyState==='complete'`, pause: 3 * time.Second, after: time.Second,
		js: tab + fmt.Sprintf(row, "Mia"), xpath: `//*[@id='seg-dropboxes']//details[contains(@class,'svc')][contains(.,"Mia")]`, pad: 16}, outdir)
	take(shot{name: "dropbox-approve", who: "alice@test.local", url: appURL + "#services", width: 1280, height: 900, wait: `document.readyState==='complete'`, pause: 3 * time.Second, after: 2 * time.Second,
		js: tab + fmt.Sprintf(row, "School") + approvalJS, xpath: `//*[@id='shot']`, pad: 16}, outdir)
	take(shot{name: "dropbox-guest", url: base + "/drop/" + birthday, width: 1280, height: 900, fold: true, pause: 3 * time.Second}, outdir)
	take(shot{name: "dropbox-gallery", url: base + "/drop/" + birthday, width: 1280, height: 900, wait: `document.querySelectorAll('.gallery img').length > 3`, pause: 3 * time.Second, after: 2 * time.Second,
		js: `document.querySelector('.gallery').scrollIntoView({block: 'start'})`, xpath: `//section[contains(@class,'gallery')]`, pad: 16}, outdir)
	take(shot{name: "dropbox-moderator", url: base + "/mod/" + mod, width: 1280, height: 900, fold: true, pause: 3 * time.Second}, outdir)
	take(shot{name: "dropbox-wall", url: base + "/wall/" + wall + "?mode=grid&bg=black", width: 1280, height: 720, fold: true, pause: 7 * time.Second}, outdir)
	take(shot{name: "dropbox-card", who: "alice@test.local", url: base + "/dropbox/card/" + birthdayID, width: 900, height: 900, fold: true, pause: 2 * time.Second}, outdir)
}

// the thumbnails of what waits for approval, with their buttons, wrapped to be taken as one picture
const approvalJS = `;{ const h = [...document.querySelectorAll('#seg-dropboxes details.svc[open] *')].find(e => e.children.length <= 1 && e.textContent.trim().startsWith('Waiting for approval'));
	if (h) { const box = document.createElement('div'); box.id = 'shot'; box.style.padding = '8px'; h.parentNode.insertBefore(box, h); let n = h; for (let i = 0; i < 3 && n; i++) { const next = n.nextElementSibling; box.appendChild(n); n = next; } } }`

// collectFix retakes the two pictures of `collect` that depend on timing or on a long row, for boxes that exist.
func collectFix(outdir string) {
	base := strings.TrimSuffix(appURL, "/")
	owner := ""
	for _, c := range loginCookies("alice@test.local") {
		owner += c.Name + "=" + c.Value + "; "
	}
	owner = strings.TrimSuffix(owner, "; ")
	home := getText(owner, base+"/")
	wall := regexp.MustCompile(`data-link="wall/([0-9a-f]+)"`).FindStringSubmatch(home)
	if wall == nil {
		log.Fatal("no wall is running")
	}
	row := `{ const d = [...document.querySelectorAll('#seg-dropboxes details.svc')].find(x => x.textContent.includes('School')); if (d) d.open = true }`
	tab := storyHideBar + `document.querySelector('nav.tabs a[data-view="services"]').click(); const s = document.querySelector('.seg [data-seg="dropboxes"]'); if (s) s.click();`
	take(shot{name: "dropbox-approve", who: "alice@test.local", url: appURL + "#services", width: 1280, height: 900, wait: `document.readyState==='complete'`, pause: 3 * time.Second, after: 2 * time.Second,
		js: tab + row + approvalJS, xpath: `//*[@id='shot']`, pad: 16}, outdir)
	take(shot{name: "dropbox-wall", url: base + "/wall/" + wall[1] + "?mode=grid&bg=black", width: 1280, height: 720, fold: true, pause: 7 * time.Second}, outdir)
}
