//go:build e2e

// The two screens of sharing photos with another person, both ways (docs/design/pairing-design.md), in a real browser against a real
// Immich: the starter offers it from the Overview and gets a link and a QR code; the other person opens the page, sees what saying
// yes means, chooses who goes back and says yes; either can pause and resume it; the starter stops it through the real confirm
// dialog and the other person is left able to clean up. This Immich runs without machine learning, so the named people are made by
// hand (POST /faces), as in the rest of the suite.
package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestPairingPages(t *testing.T) {
	alice, bob, _, _ := bootstrap(t)
	person := func(u user, name string, seed int) {
		t.Helper()
		asset, _ := upload(t, u, name+".jpg", jpegBytes(seed))
		var p struct {
			ID string `json:"id"`
		}
		mustOK(t, call(t, "POST", "/people", keyAuth(u), map[string]string{"name": name}, &p), "person")
		mustOK(t, call(t, "POST", "/faces", keyAuth(u), map[string]any{"assetId": asset, "personId": p.ID, "imageWidth": 64, "imageHeight": 64, "x": 10, "y": 10, "width": 30, "height": 30}, nil), "face")
	}
	person(alice, "Nora-pair", 71001)
	person(bob, "Leo-pair", 71002)

	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 71)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	owner, other := loginCookie(t, "alice@e2e.test"), loginCookie(t, "bob@e2e.test")
	base := "http://localhost:" + port + "/"
	shots := os.Getenv("PAIRING_SHOTS") // a folder to keep pictures of the two screens in, to look at them

	text := func(ctx context.Context, sel string) string {
		return js[string](t, ctx, `(document.querySelector('`+sel+`') || {innerText: ''}).innerText`)
	}
	click := func(ctx context.Context, sel, label string) { // the button or link with this text inside sel
		t.Helper()
		until(t, ctx, "the "+label+" button", `[...document.querySelectorAll('`+sel+` button, `+sel+` a')].some(b => b.textContent.trim() === '`+label+`')`, 10*time.Second)
		run(t, ctx, chromedp.Evaluate(`[...document.querySelectorAll('`+sel+` button, `+sel+` a')].find(b => b.textContent.trim() === '`+label+`').click()`, nil))
	}
	shot := func(ctx context.Context, sel, name string) {
		if shots == "" {
			return
		}
		var buf []byte
		run(t, ctx, chromedp.ScrollIntoView(sel, chromedp.ByQuery))
		time.Sleep(400 * time.Millisecond)
		run(t, ctx, chromedp.Screenshot(sel, &buf, chromedp.ByQuery))
		os.WriteFile(filepath.Join(shots, name), buf, 0o644)
	}
	home := func(cookie string) context.Context {
		ctx := browse(t, port, cookie, look{})
		run(t, ctx, chromedp.Navigate(base+"#settings"), chromedp.WaitVisible("header.bar", chromedp.ByQuery))
		until(t, ctx, "the Settings view", `document.querySelector('#v-settings') !== null`, 10*time.Second)
		// Settings shows one category at a time, and the sharing card is in Sharing
		run(t, ctx, chromedp.Click(`.settings-nav [data-cat="sharing"]`, chromedp.ByQuery))
		return ctx
	}

	// the starter offers it from the Settings view
	a := home(owner)
	until(t, a, "the card", `document.querySelector('.pairingscard') !== null`, 10*time.Second)
	run(t, a, chromedp.Click(".pairingscard details.offer summary", chromedp.ByQuery))
	until(t, a, "the form", `document.getElementById('pair-email') !== null && document.querySelectorAll('.pairingscard input[name=person]').length > 0`, 10*time.Second)
	if got := text(a, ".pairingscard .offer-form"); !strings.Contains(got, "Nora-pair") || strings.Contains(got, "Leo-pair") {
		t.Errorf("the form offers the starter's own people only: %q", got)
	}
	run(t, a, chromedp.SendKeys("#pair-email", "bob@e2e.test", chromedp.ByQuery))
	run(t, a, chromedp.Evaluate(`[...document.querySelectorAll('.pairingscard input[name=person]')].find(i => i.closest('label').textContent.includes('Nora-pair')).click()`, nil))
	run(t, a, chromedp.Click(".pairingscard .offer-form button[type=submit]", chromedp.ByQuery))
	until(t, a, "the offer on the card", `document.querySelector('.pairing') !== null`, 15*time.Second)
	card := text(a, ".pairing")
	for _, want := range []string{"You and bob", "Waiting for a yes", "You send photos of Nora-pair.", "Cancel the offer"} {
		if !strings.Contains(card, want) {
			t.Errorf("the starter's card lacks %q: %q", want, card)
		}
	}
	until(t, a, "the link", `document.querySelector('.pairing input.invite').value !== ''`, 5*time.Second)
	link := js[string](t, a, `document.querySelector('.pairing input.invite').value`)
	if !strings.HasPrefix(link, base+"pair/") {
		t.Fatalf("the link to give: %q", link)
	}
	run(t, a, chromedp.Click(".pairing .show-qr", chromedp.ByQuery))
	until(t, a, "the QR code", `document.querySelector('.pairing .qr img, .pairing .qr svg') !== null && !document.querySelector('.pairing .qr').hidden`, 10*time.Second)
	if poolName := regexp.MustCompile(`pair-[0-9a-f]{8}`).FindString(text(a, "body")); poolName != "" {
		t.Errorf("the pool's generated name is shown to the starter: %s", poolName)
	}
	shot(a, ".pairingscard", "starter-offer.png")

	// the other person opens the page
	b := browse(t, port, other, look{})
	run(t, b, chromedp.Navigate(link), chromedp.WaitVisible("h1", chromedp.ByQuery))
	if got := text(b, "h1"); got != "alice would like to share photos with you" {
		t.Errorf("the page's title: %q", got)
	}
	page := text(b, "body")
	for _, want := range []string{"photos of Nora-pair into your library", "Only photos that arrive after you say yes", "nothing is ever deleted from anyone's library", "Leo-pair", "No thanks"} {
		if !strings.Contains(page, want) {
			t.Errorf("the consent page lacks %q", want)
		}
	}
	if strings.Contains(page, "Nora-pair") && strings.Contains(text(b, "fieldset"), "Nora-pair") {
		t.Error("the other person is offered the starter's people to send back: only their own belong there")
	}
	shot(b, ".invite-card", "other-consent.png")
	run(t, b, chromedp.Evaluate(`[...document.querySelectorAll('input[name=person]')].find(i => i.closest('label').textContent.includes('Leo-pair')).click()`, nil))
	click(b, "form", "Yes, share with alice")
	until(t, b, "the page to lead home with the card", `!location.pathname.includes('/pair/') && document.querySelector('.pairing') !== null`, 20*time.Second)
	for _, want := range []string{"You and alice", "Sharing", "You send photos of Leo-pair.", "alice sends photos of Nora-pair.", "Pause sharing", "Stop sharing"} {
		if got := text(b, ".pairing"); !strings.Contains(got, want) {
			t.Errorf("the other person's card lacks %q: %q", want, got)
		}
	}

	// the starter's card is live now, and either can pause and resume
	a = home(owner)
	until(t, a, "the sharing on the starter's card", `document.querySelector('.pairing') !== null && document.querySelector('.pairing').innerText.includes('bob sends photos of Leo-pair.')`, 15*time.Second)
	shot(a, ".pairingscard", "starter-sharing.png")
	click(a, ".pairing", "Pause sharing")
	until(t, a, "the card to say it is paused", `document.querySelector('.pairing') !== null && document.querySelector('.pairing').innerText.includes('Resume sharing')`, 15*time.Second)
	click(a, ".pairing", "Resume sharing")
	until(t, a, "the card to say it is on again", `document.querySelector('.pairing') !== null && document.querySelector('.pairing').innerText.includes('Pause sharing')`, 15*time.Second)

	// nothing of it can be changed by hand: the pool is not on the Services tab
	if poolName := regexp.MustCompile(`pair-[0-9a-f]{8}`).FindString(js[string](t, a, `document.documentElement.innerText`)); poolName != "" {
		t.Errorf("the pool's generated name is on the page: %s", poolName)
	}

	// stopping goes through the confirm dialog, and the other person can clean up
	click(a, ".pairing", "Stop sharing")
	until(t, a, "the confirm dialog", `document.getElementById('confirm').open`, 5*time.Second)
	if got := js[string](t, a, `document.getElementById('confirm-text').textContent`); !strings.Contains(got, "Stop sharing photos with bob?") || !strings.Contains(got, "nothing is deleted") {
		t.Errorf("the dialog says what stopping means: %q", got)
	}
	run(t, a, chromedp.Click(`#confirm-ok`, chromedp.ByQuery))
	until(t, a, "the sharing to be gone", `document.querySelector('.pairing') === null`, 15*time.Second)

	// the notice that it was removed is a thing that needs the person, so it is on the Overview
	b = browse(t, port, other, look{})
	run(t, b, chromedp.Navigate(base), chromedp.WaitVisible("header.bar", chromedp.ByQuery))
	until(t, b, "the removed sharing", `document.body.innerText.includes('Removed by its owner')`, 15*time.Second)
	if got := text(b, "body"); !strings.Contains(got, "Photo sharing") || regexp.MustCompile(`pair-[0-9a-f]{8}`).MatchString(got) {
		t.Errorf("the other person is told in words, not by the pool's name: %q", got)
	}
	shot(b, "body", "other-removed.png")
	click(b, "body", "Clean up")
	until(t, b, "the clean up to finish", `!document.body.innerText.includes('Removed by its owner')`, 15*time.Second)
}
