//go:build e2e

package e2e

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestHandOverButton presses the button the script adds to Immich's own pages, in a real Chrome against a real Immich
// behind the proxy docs/find-handoff.md describes: the photo viewer, the list of services, the message with Undo.
func TestHandOverButton(t *testing.T) {
	w := theWorld(t)
	address := w.inFrontOfImmich(t)
	// bob's own sending is paused, so a photo he hands over stays tagged until he undoes it, not taken by the next pass
	if res := post(t, w.port, "/pause", w.sender, url.Values{"service": {"live"}, "paused": {"1"}}); res.StatusCode != 204 {
		t.Fatalf("bob pauses: %d", res.StatusCode)
	}
	id, _ := upload(t, w.bob, "button-1.jpg", jpegBytes(9601))
	hasTag := func() bool {
		a, _ := getAsset(t, w.bob, id)
		for _, tg := range a.Tags {
			if tg.Name == "live-browser" {
				return true
			}
		}
		return false
	}

	ctx := browse(t, w.port, w.sender, look{})
	run(t, ctx, chromedp.Navigate(address+"/photos/"+id))
	until(t, ctx, "the button in the viewer's toolbar", `document.getElementById('handoff-viewer-act') !== null`, 30*time.Second)

	t.Run("it sits in the viewer's toolbar, before the three dots, with words and the app's icon", func(t *testing.T) {
		got := js[string](t, ctx, `(() => {
			const b = document.getElementById('handoff-viewer-act');
			const bar = document.querySelector('[data-testid=asset-viewer-navbar-actions]');
			const more = bar.querySelector('button[aria-haspopup][id^=context-menu-button]').parentElement.parentElement;
			return [b.parentElement === bar, b.nextElementSibling === more.closest('[data-testid=asset-viewer-navbar-actions] > *'), b.textContent.trim(), b.getBoundingClientRect().height, b.querySelector('img').naturalWidth > 0].join('|');
		})()`)
		if got != "true|true|Hand over...|40|true" {
			t.Errorf("in the toolbar before the three dots, words, 40 px high, icon: %q", got)
		}
	})

	t.Run("it offers the service bob sends to, says his sending is paused, and hands the photo over with Undo", func(t *testing.T) {
		run(t, ctx, chromedp.Click(`#handoff-viewer-act`, chromedp.ByQuery))
		until(t, ctx, "the list of services", `document.querySelectorAll('#handoff-menu [role=menuitem]').length === 1`, 10*time.Second)
		if got := js[string](t, ctx, `document.getElementById('handoff-menu').textContent`); !strings.Contains(got, "live") || !strings.Contains(got, "You paused sending to this service.") {
			t.Errorf("the menu: %q", got)
		}
		run(t, ctx, chromedp.Click(`#handoff-menu [role=menuitem]`, chromedp.ByQuery))
		until(t, ctx, "the message", `(document.getElementById('handoff-toast') || {textContent: ''}).textContent.includes('1 photo handed over to live')`, 15*time.Second)
		if !hasTag() {
			t.Error("the photo did not get the service's tag")
		}
		run(t, ctx, chromedp.Click(`#handoff-toast button`, chromedp.ByQuery)) // Undo
		until(t, ctx, "the undo to be said", `document.getElementById('handoff-toast').textContent.includes('Taken off again')`, 15*time.Second)
		eventually(t, "the tag to be off the photo", 10*time.Second, func() bool { return !hasTag() })
		if !js[bool](t, ctx, `document.querySelector('#handoff-toast a').href.endsWith('/handoff/')`) {
			t.Error("the message links to the app on the same address")
		}
	})

	t.Run("on a phone only the icon shows, and a person who sends nowhere is told so", func(t *testing.T) {
		phone := browse(t, w.port, w.grandma, look{w: 390, h: 800, touch: true})
		hers, _ := upload(t, w.gran, "button-gran.jpg", jpegBytes(9602))
		run(t, phone, chromedp.Navigate(address+"/photos/"+hers))
		until(t, phone, "the button", `document.getElementById('handoff-viewer-act') !== null`, 30*time.Second)
		if !js[bool](t, phone, `getComputedStyle(document.querySelector('#handoff-viewer-act span')).display === 'none'`) {
			t.Error("the words are shown on a phone")
		}
		run(t, phone, chromedp.Click(`#handoff-viewer-act`, chromedp.ByQuery))
		until(t, phone, "the list", `document.getElementById('handoff-menu') !== null && document.getElementById('handoff-menu').textContent.includes('You do not send to any service yet.')`, 10*time.Second)
	})

	t.Run("a person the app does not know gets no button", func(t *testing.T) {
		ctx := browse(t, w.port, w.owner, look{})
		run(t, ctx, chromedp.Navigate(address+"/photos"))
		until(t, ctx, "the header link", `document.getElementById('handoff-link') !== null`, 20*time.Second)
		if js[bool](t, ctx, `document.getElementById('handoff-viewer-act') !== null`) {
			t.Error("a button where there is no viewer")
		}
	})
}
