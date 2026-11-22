package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// cardJS wraps one card of Settings (its heading and what follows it up to the next heading) as #shot and opens the form whose summary
// contains `open`, so that it can be taken as one picture.
func cardJS(category, heading, open string) string {
	return fmt.Sprintf(storyHideBar+`document.querySelector('nav.tabs a[data-view="settings"]').click();
	const c = document.querySelector('.settings-nav [data-cat="%s"]'); if (c) c.click();
	const h = [...document.querySelectorAll('#v-settings h2')].find(e => e.textContent.includes(%q));
	const box = document.createElement('div'); box.id = 'shot'; box.style.padding = '8px'; h.parentNode.insertBefore(box, h);
	let n = h; while (n && !(n !== h && n.tagName === 'H2')) { const next = n.nextElementSibling; box.appendChild(n); n = next; }
	if (%q) box.querySelectorAll('details.offer').forEach(d => { if (d.textContent.includes(%q)) d.open = true });`, category, heading, open, open)
}

// cardsShots takes the pictures of the guides whose feature is a card in Settings, a form of a service, the album tree and the
// administrator's page. It needs the stack made with `SHARE=1 ./seed.sh reset` (which also gives the app a push contact, so the
// notification cards are there), `./seed.sh setup` and `./seed.sh prepare` (Alice joins Holiday 2026, so she has a rules form).
//
//	go run . cards <outdir> <photo folder>
func cardsShots(outdir, photos string) {
	os.MkdirAll(outdir, 0o755)
	// albums with slashes in their names, with a few bright photos, for the album tree
	token := alice()
	var ids []string
	for i, n := range []int{2062, 2068, 2074, 2080, 2086, 2092} {
		p := filepath.Join(photos, fmt.Sprintf("IMG_%04d.jpg", n))
		b, err := os.ReadFile(p)
		if err != nil {
			log.Fatalf("sample photo %s: %v (run `go run . gen <dir> 80` first)", p, err)
		}
		ids = append(ids, upload(token, filepath.Base(p), b, time.Date(2025, 6, 1+i, 12, 0, 0, 0, time.UTC)))
	}
	for _, id := range ids {
		waitProcessed(token, id)
	}
	for i, name := range []string{"Trips / 2025 / Rome", "Trips / 2025 / Lisbon", "Trips / 2024 / Oslo", "Family / Birthdays", "Family / Christmas"} {
		apiJSON(token, "POST", "/api/albums", map[string]any{"albumName": name, "assetIds": []string{ids[i%len(ids)], ids[(i+1)%len(ids)]}})
	}

	ready := `document.readyState==='complete'`
	card := func(name, who, category, heading, open string) {
		take(shot{name: name, who: who, url: appURL, width: 1280, height: 900, wait: ready, pause: 3 * time.Second, after: 3 * time.Second,
			js: cardJS(category, heading, open), xpath: `//*[@id='shot']`, pad: 16}, outdir)
	}
	card("pets-card", "alice@test.local", "photos", "Pets", "")
	card("smart-album-form", "alice@test.local", "photos", "Smart albums", "Make a smart album")
	card("folder-albums-form", "alice@test.local", "photos", "Albums from my folders", "Make albums from a folder")
	card("stacking-form", "alice@test.local", "photos", "Stack photos that belong together", "Stack photos by a rule")
	card("places-card", "alice@test.local", "photos", "Places for photos that have none", "")
	card("memories-card", "alice@test.local", "photos", "On this day", "")
	card("cast-card", "alice@test.local", "screens", "Cast to a TV", "")
	card("notify-cards", "alice@test.local", "notifications", "Notifications on this device", "")

	tab := storyHideBar + `document.querySelector('nav.tabs a[data-view="services"]').click();`
	take(shot{name: "frames-form", who: "alice@test.local", url: appURL + "#services", width: 1280, height: 900, wait: ready, pause: 3 * time.Second, after: 2 * time.Second,
		js:    tab + `const s = document.querySelector('.seg [data-seg="frames"]'); if (s) s.click(); const o = [...document.querySelectorAll('#seg-frames details.offer')].find(d => d.textContent.includes('Make a frame')); if (o) o.open = true;`,
		xpath: `//*[@id='seg-frames']`, pad: 16}, outdir)
	take(shot{name: "rules-form", who: "alice@test.local", url: appURL + "#services", width: 1280, height: 900, wait: ready, pause: 3 * time.Second, after: 3 * time.Second,
		js: tab + `{ const d = [...document.querySelectorAll('#v-services details.svc')].find(x => x.textContent.includes('Holiday 2026')); if (d) d.open = true;
			const o = [...document.querySelectorAll('details.offer')].find(x => x.querySelector(':scope > summary')?.textContent.trim() === 'Make a rule'); if (o) o.open = true; }`,
		xpath: `//details[contains(@class,'offer')][normalize-space(./summary)='Make a rule']`, pad: 16}, outdir)
	take(shot{name: "controls-limits", who: "bob@test.local", url: appURL + "#services", width: 1280, height: 900, wait: ready, pause: 3 * time.Second, after: time.Second,
		js: tab + `{ const d = [...document.querySelectorAll('#v-services details.svc')].find(x => x.textContent.includes('Holiday 2026')); if (d) { d.open = true;
			const box = document.createElement('div'); box.id = 'shot'; box.style.padding = '8px'; const f = [...d.querySelectorAll('form.limits')].slice(0, 3); f[0].parentNode.insertBefore(box, f[0]); f.forEach(x => box.appendChild(x)); } }`,
		xpath: `//*[@id='shot']`, pad: 16}, outdir)
	take(shot{name: "album-tree", who: "alice@test.local", url: appURL + "albums", width: 1280, height: 900, fold: true, pause: 4 * time.Second,
		js: albumTreeJS, after: 2 * time.Second}, outdir)
	take(shot{name: "admin-page", who: "admin@test.local", url: appURL + "admin", width: 1280, height: 1000, fold: true, wait: ready, pause: 3 * time.Second}, outdir)
}

// the folders of the album tree open, the Move helpers and the menus of the bar closed
const albumTreeJS = `document.querySelectorAll('main details').forEach(d => { d.open = !d.querySelector(':scope > summary')?.textContent.includes('Move') })`

// cardsFix retakes the two pictures of `cards` that took the wrong form or opened too much, on the same stack.
func cardsFix(outdir string) {
	ready := `document.readyState==='complete'`
	tab := storyHideBar + `document.querySelector('nav.tabs a[data-view="services"]').click();`
	take(shot{name: "rules-form", who: "alice@test.local", url: appURL + "#services", width: 1280, height: 900, wait: ready, pause: 3 * time.Second, after: 3 * time.Second,
		js: tab + `{ const d = [...document.querySelectorAll('#v-services details.svc')].find(x => x.textContent.includes('Holiday 2026')); if (d) d.open = true;
			const o = [...document.querySelectorAll('details.offer')].find(x => x.querySelector(':scope > summary')?.textContent.trim() === 'Make a rule'); if (o) o.open = true; }`,
		xpath: `//details[contains(@class,'offer')][normalize-space(./summary)='Make a rule']`, pad: 16}, outdir)
	take(shot{name: "album-tree", who: "alice@test.local", url: appURL + "albums", width: 1280, height: 900, fold: true, pause: 4 * time.Second, js: albumTreeJS, after: 2 * time.Second}, outdir)
}
