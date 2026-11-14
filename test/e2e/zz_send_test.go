//go:build e2e

// Named to sort last, like the other zz_ tests: it leaves photos in the shared Immich.
package e2e

import (
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// sendFixture starts the app with a service that bob sends to, and has bob join it.
func sendFixture(t *testing.T, offset int) (port string, bobCookie string, bob user) {
	t.Helper()
	_, bob, family, _ := bootstrap(t)
	bin := buildApp(t)
	port = fmt.Sprint(atoi(appPort) + offset)
	dir := t.TempDir()
	routes := filepath.Join(dir, "routes.yaml")
	if err := os.WriteFile(routes, []byte(`
accounts:
  family: { key: ${E2E_FAMILY} }
services:
  - name: phone-open
    tag: send-me
    receivers: [family]
    allow_join: true
    mode: move
`), 0o600); err != nil {
		t.Fatal(err)
	}
	launchApp(t, bin, port, "ROUTES_FILE="+routes, "E2E_FAMILY="+family.appKey, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+dir)
	bobCookie = loginCookie(t, "bob@e2e.test")
	if res := post(t, port, "/join", bobCookie, url.Values{"service": {"phone-open"}, "mode": {"move"}}); res.StatusCode != 204 {
		t.Fatalf("bob joins: HTTP %d", res.StatusCode)
	}
	return port, bobCookie, bob
}

// phoneFile writes a photo (a real JPEG, with more bytes after it when pad is above zero, so it goes in several pieces).
func phoneFile(t *testing.T, name string, seed, pad int) string {
	t.Helper()
	data := jpegBytes(seed)
	if pad > 0 {
		junk := make([]byte, pad)
		rand.New(rand.NewSource(int64(seed))).Read(junk)
		data = append(data, junk...)
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// byName finds a person's photos by the name they were stored under.
func byName(t *testing.T, u user, name string) []string {
	t.Helper()
	var res struct {
		Assets struct{ Items []struct{ ID string } } `json:"assets"`
	}
	call(t, "POST", "/search/metadata", keyAuth(u), map[string]any{"originalFileName": name, "withDeleted": true}, &res)
	var ids []string
	for _, i := range res.Assets.Items {
		ids = append(ids, i.ID)
	}
	return ids
}

// TestSend: a phone-sized browser sends photos from the page. A person who sends to a service picks it, the photos land
// whole in their own Immich, are tagged and handed over; a person who picks "my library only" has them kept, untagged.
func TestSend(t *testing.T) {
	port, cookie, bob := sendFixture(t, 70)
	stamp := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	seed := int(time.Now().UnixNano() % 50000)
	phone := browse(t, port, cookie, look{w: 390, h: 800, touch: true})
	t.Cleanup(func() {
		if t.Failed() {
			var list, flash string
			chromedp.Run(phone, chromedp.Evaluate(`document.getElementById('send-list')?.innerText ?? ''`, &list), chromedp.Evaluate(`document.getElementById('flash')?.textContent ?? ''`, &flash))
			t.Logf("the page shows: %q (%q)", list, flash)
		}
	})
	row := func(name string) string {
		return fmt.Sprintf(`[...document.querySelectorAll('.send-item')].find(i => i.querySelector('.name').textContent === %q)`, name)
	}
	progress := func(name string) string {
		return `Number(` + row(name) + `?.querySelector('.send-bar')?.getAttribute('aria-valuenow') ?? 0)`
	}
	isDone := func(name string) string { return `(` + row(name) + `?.classList.contains('done') ?? false)` }
	open := func() {
		run(t, phone, chromedp.Navigate("http://localhost:"+port+"/send"), chromedp.WaitVisible(`#send-files`, chromedp.ByQuery))
		if js[bool](t, phone, `document.documentElement.scrollWidth > innerWidth + 1`) {
			t.Error("the page must not scroll sideways on a phone")
		}
	}
	choose := func(service string, files ...string) {
		run(t, phone, chromedp.SetUploadFiles(`#send-files`, files, chromedp.ByQuery))
		until(t, phone, "the button counts the files", fmt.Sprintf(`document.getElementById('send-go').disabled === false && /%d/.test(document.getElementById('send-go').textContent)`, len(files)), 10*time.Second)
		run(t, phone, chromedp.Evaluate(fmt.Sprintf(`[...document.querySelectorAll('input[name=service]')].find(r => r.value === %q).click()`, service), nil), chromedp.Click(`#send-go`, chromedp.ByQuery))
		// the first send in a browser says that locations go with the photos, and asks (the dialog rises into place, so the
		// button is pressed again until it has closed)
		for i := 0; i < 30; i++ {
			if js[bool](t, phone, `document.getElementById('confirm').open`) {
				if !strings.Contains(js[string](t, phone, `document.getElementById('confirm-text').textContent`), "place where each was taken") {
					t.Error("the question is about locations")
				}
				time.Sleep(400 * time.Millisecond)
				run(t, phone, chromedp.Click(`#confirm-ok`, chromedp.ByQuery))
			} else if i > 5 {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	t.Run("photos sent to a service land whole in the sender's Immich, tagged, and are handed over", func(t *testing.T) {
		open()
		a, b := fmt.Sprintf("a-%s.jpg", stamp), fmt.Sprintf("b-%s.jpg", stamp)
		// the second is larger than a piece, so it goes in several
		choose("phone-open", phoneFile(t, a, seed, 0), phoneFile(t, b, seed+1, 9<<20))
		until(t, phone, "both are sent", isDone(a)+` && `+isDone(b), 120*time.Second)
		if js[bool](t, phone, `[...document.querySelectorAll('.send-item .state')].some(s => /could not|failed/i.test(s.textContent))`) {
			t.Error("nothing failed")
		}
		var ids []string
		eventually(t, "both are in bob's Immich", 30*time.Second, func() bool {
			ids = append(byName(t, bob, a), byName(t, bob, b)...)
			return len(ids) == 2
		})
		for _, id := range ids {
			eventually(t, "tagged and handed over", 120*time.Second, func() bool {
				x, code := getAsset(t, bob, id)
				if code != 200 {
					return false
				}
				tagged := false
				for _, tg := range x.Tags {
					tagged = tagged || tg.Name == "send-me"
				}
				return tagged && x.IsTrashed
			})
		}
	})

	t.Run("my library only keeps them, untagged, and the choice is remembered", func(t *testing.T) {
		open()
		name := fmt.Sprintf("c-%s.jpg", stamp)
		choose("", phoneFile(t, name, seed+2, 0))
		until(t, phone, "it is sent", isDone(name), 60*time.Second)
		var id string
		eventually(t, "it is in bob's Immich", 30*time.Second, func() bool {
			ids := byName(t, bob, name)
			if len(ids) == 1 {
				id = ids[0]
			}
			return id != ""
		})
		x, _ := getAsset(t, bob, id)
		if len(x.Tags) != 0 || x.IsTrashed {
			t.Errorf("a library-only photo has no tag and stays: %+v", x)
		}
		open()
		if !js[bool](t, phone, `document.querySelector('input[name=service]:checked').value === ""`) {
			t.Error("the choice made last is selected next time")
		}
		if !strings.Contains(js[string](t, phone, `document.querySelector('fieldset.targets').textContent`), "phone-open") {
			t.Error("the other service is still listed")
		}
	})

	// the network: slow, so a file is caught half way, then gone, then back
	connection := func(offline bool, upload float64) {
		run(t, phone, network.EmulateNetworkConditions(offline, 0, -1, upload))
	}
	checksumOf := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha1.Sum(b)
		return base64.StdEncoding.EncodeToString(sum[:])
	}
	arrived := func(t *testing.T, name, path string) {
		t.Helper()
		var id string
		eventually(t, name+" is in bob's Immich", 60*time.Second, func() bool {
			ids := byName(t, bob, name)
			if len(ids) == 1 {
				id = ids[0]
			}
			return id != ""
		})
		var a struct{ Checksum string }
		call(t, "GET", "/assets/"+id, keyAuth(bob), nil, &a)
		if a.Checksum != checksumOf(path) {
			t.Errorf("%s did not arrive whole: its checksum is %s", name, a.Checksum)
		}
	}

	t.Run("a connection that is cut waits, and the file carries on by itself and arrives whole", func(t *testing.T) {
		open()
		name := fmt.Sprintf("d-%s.jpg", stamp)
		path := phoneFile(t, name, seed+3, 12<<20)
		connection(false, 2_000_000) // about 2 MB a second: the file takes several seconds
		choose("", path)
		until(t, phone, "it is under way", progress(name)+` > 5 && `+progress(name)+` < 100`, 30*time.Second)
		connection(true, -1)
		until(t, phone, "it says it waits for the network", `/network/i.test(`+row(name)+`?.querySelector('.state')?.textContent ?? '')`, 30*time.Second)
		connection(false, -1)
		until(t, phone, "it carries on and is sent", isDone(name), 120*time.Second)
		arrived(t, name, path)
	})

	t.Run("a tab closed in the middle is carried on when the page is opened again", func(t *testing.T) {
		open()
		name := fmt.Sprintf("e-%s.jpg", stamp)
		path := phoneFile(t, name, seed+4, 12<<20)
		connection(false, 2_000_000)
		choose("", path)
		until(t, phone, "it is under way", progress(name)+` > 5 && `+progress(name)+` < 100`, 30*time.Second)
		connection(true, -1)
		run(t, phone, chromedp.Navigate("about:blank"))
		connection(false, -1)
		open()
		until(t, phone, "the file is back in the list and is sent", isDone(name), 120*time.Second)
		arrived(t, name, path)
	})

	t.Run("files chosen by one person are not sent as another", func(t *testing.T) {
		start := func(owner string) int {
			req, _ := http.NewRequest("POST", "http://localhost:"+port+"/send/start", strings.NewReader(`{"name":"x.jpg","size":10,"owner":"`+owner+`"}`))
			req.Header.Set("Cookie", cookie)
			req.Header.Set("X-Requested-With", "immich-handoff")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			return res.StatusCode
		}
		if c := start("00000000-0000-0000-0000-000000000001"); c != 403 {
			t.Errorf("a file chosen by someone else: %d", c)
		}
		if c := start(bob.id); c != 201 {
			t.Errorf("bob's own: %d", c)
		}
	})
}
