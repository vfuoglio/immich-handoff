package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// shareShots takes the pictures of the sharing guides that the plain demo has nothing to show for: the offer form with Event pool ticked, the
// Circles form, the card that follows an album shared with Bob, a service that hands photos to a destination, and the button that invites
// someone on another server. It needs the stack made with `SHARE=1 ./seed.sh reset` (bridges on, a folder destination named nas) and the
// app set up as usual (`./seed.sh setup`: Bob offers Holiday 2026).
//
//	go run . share <outdir> <photo folder>
func shareShots(outdir, photos string) {
	os.MkdirAll(outdir, 0o755)
	base := strings.TrimSuffix(appURL, "/")
	cookie := func(email string) string {
		var s []string
		for _, c := range loginCookies(email) {
			s = append(s, c.Name+"="+c.Value)
		}
		return strings.Join(s, "; ")
	}
	bob := cookie("bob@test.local")
	post := func(who, path string, form url.Values) {
		req, _ := http.NewRequest("POST", base+path, strings.NewReader(form.Encode()))
		req.Header.Set("Cookie", who)
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

	// Alice makes an album of four bright photos and shares it with Bob, who can then follow it
	token := alice()
	var ids []string
	for i, n := range []int{2062, 2068, 2074, 2080} {
		p := filepath.Join(photos, fmt.Sprintf("IMG_%04d.jpg", n))
		b, err := os.ReadFile(p)
		if err != nil {
			log.Fatalf("sample photo %s: %v (run `go run . gen <dir> 80` first)", p, err)
		}
		ids = append(ids, upload(token, filepath.Base(p), b, time.Date(2026, 5, 20, 10+i, 0, 0, 0, time.UTC)))
	}
	for _, id := range ids {
		waitProcessed(token, id)
	}
	album := apiJSON(token, "POST", "/api/albums", map[string]any{"albumName": "Lisbon, spring 2026", "assetIds": ids})["id"].(string)
	var bobID string
	req, _ := http.NewRequest("GET", immichURL+"/api/users", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if res, err := http.DefaultClient.Do(req); err == nil {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if m := regexp.MustCompile(`"id":"([0-9a-f-]{36})"[^{}]*"email":"bob@test.local"|"email":"bob@test.local"[^{}]*"id":"([0-9a-f-]{36})"`).FindStringSubmatch(string(b)); m != nil {
			bobID = m[1] + m[2]
		}
	}
	if bobID == "" {
		log.Fatal("Bob is not among the users")
	}
	apiJSON(token, "PUT", "/api/albums/"+album+"/users", map[string]any{"albumUsers": []map[string]any{{"userId": bobID, "role": "viewer"}}})

	// Bob's Holiday 2026 hands photos to the folder destination, and has an invitation for someone on another server
	home := getText(bob, base+"/")
	ref := regexp.MustCompile(`name="ref">\s*<option value="([^"]+)"`).FindStringSubmatch(home)
	if ref == nil {
		log.Fatal("the service offers no destination (was the stack made with SHARE=1 ./seed.sh reset?)")
	}
	post(bob, "/services/destinations", url.Values{"service": {"Holiday 2026"}, "ref": {ref[1]}, "template": {""}})
	post(bob, "/services/bridge-invite", url.Values{"service": {"Holiday 2026"}})

	hide := storyHideBar
	svc := hide + `document.querySelector('nav.tabs a[data-view="services"]').click(); document.querySelectorAll('#v-services details.svc').forEach(d => { if (d.textContent.includes('Holiday 2026')) d.open = true });`
	take(shot{name: "trip-pool-form", who: "bob@test.local", url: appURL + "#services", width: 1280, height: 900, pause: 2 * time.Second, after: time.Second,
		js: hide + `document.querySelector('nav.tabs a[data-view="services"]').click(); const o = document.querySelector('#v-services details.offer'); o.open = true;
			o.querySelector('input[name="name"]').value = 'Trip to Lisbon'; o.querySelector('input[name="pool"]').checked = true;`,
		xpath: `//section[@id='v-services']//details[contains(@class,'offer')][1]`, pad: 16}, outdir)
	take(shot{name: "circles-form", who: "alice@test.local", url: appURL, width: 1280, height: 900, pause: 3 * time.Second, after: 3 * time.Second,
		js: hide + `document.querySelector('nav.tabs a[data-view="settings"]').click(); const c = document.querySelector('.settings-nav [data-cat="sharing"]'); if (c) c.click();
			document.querySelector('.circlescard details.offer').open = true;`,
		xpath: `//div[contains(@class,'circlescard')]`, pad: 16}, outdir)
	take(shot{name: "follow-album-card", who: "bob@test.local", url: appURL, width: 1280, height: 900, pause: 3 * time.Second, after: 3 * time.Second,
		js: followCardJS, xpath: `//*[@id='shot']`, pad: 16}, outdir)
	take(shot{name: "destinations-card", who: "bob@test.local", url: appURL + "#services", width: 1280, height: 900, pause: 2 * time.Second, after: time.Second, js: svc,
		xpath: `//div[contains(@class,'dests')]`, pad: 16}, outdir)
	take(shot{name: "bridge-card", who: "bob@test.local", url: appURL + "#services", width: 1280, height: 900, pause: 2 * time.Second, after: time.Second, js: svc,
		xpath: `//label[contains(text(),'A receiver on another Immich server')]/..`, pad: 16}, outdir)
}

// the card that chooses where copies go is followed, in the page, by the albums that are followed and the form that follows one: wrapped
// together to be taken as one picture
const followCardJS = storyHideBar + `document.querySelector('nav.tabs a[data-view="settings"]').click();
	const c = document.querySelector('.settings-nav [data-cat="photos"]'); if (c) c.click();
	const h = [...document.querySelectorAll('#v-settings h2')].find(e => e.textContent.includes('Add to my library'));
	const box = document.createElement('div'); box.id = 'shot'; h.parentNode.insertBefore(box, h);
	let n = h; while (n && !(n !== h && n.tagName === 'H2')) { const next = n.nextElementSibling; box.appendChild(n); n = next; }
	box.querySelectorAll('details.offer').forEach(d => { if (d.textContent.includes('Follow an album shared with me')) d.open = true });`
