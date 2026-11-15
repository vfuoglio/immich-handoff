//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

// toggleTile selects (or unselects) the i-th photo of the grid: its check circle is drawn only while the pointer is
// over the tile.
func toggleTile(t *testing.T, ctx context.Context, i int) {
	t.Helper()
	var at []float64
	run(t, ctx, chromedp.Evaluate(fmt.Sprintf(`(() => { const r = document.querySelectorAll('[data-thumbnail-focus-container]')[%d].getBoundingClientRect(); return [r.x + r.width / 2, r.y + r.height / 2]; })()`, i), &at))
	run(t, ctx, input.DispatchMouseEvent(input.MouseMoved, at[0], at[1]))
	until(t, ctx, "the check circle", fmt.Sprintf(`document.querySelectorAll('[data-thumbnail-focus-container]')[%d].querySelector('button[role=checkbox]') !== null`, i), 5*time.Second)
	run(t, ctx, chromedp.Evaluate(fmt.Sprintf(`document.querySelectorAll('[data-thumbnail-focus-container]')[%d].querySelector('button[role=checkbox]').click()`, i), nil))
}

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
		until(t, ctx, "the list of services", `document.querySelectorAll('#handoff-menu [role=menuitem]').length === 2`, 10*time.Second)
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
		// the service used is remembered in this browser and offered first; services needing approval can be asked for
		run(t, ctx, chromedp.Click(`#handoff-viewer-act`, chromedp.ByQuery))
		until(t, ctx, "the list again", `document.getElementById('handoff-menu') !== null`, 10*time.Second)
		got := js[string](t, ctx, `document.getElementById('handoff-menu').textContent`)
		for _, want := range []string{"Last used", "Ask to join", "club", "Forget my choice"} {
			if !strings.Contains(got, want) {
				t.Errorf("the list lacks %q: %q", want, got)
			}
		}
		run(t, ctx, chromedp.Click(`#handoff-menu .foot button`, chromedp.ByQuery)) // Forget my choice
		run(t, ctx, chromedp.Click(`#handoff-viewer-act`, chromedp.ByQuery))
		until(t, ctx, "the list once more", `document.getElementById('handoff-menu') !== null`, 10*time.Second)
		if js[bool](t, ctx, `document.getElementById('handoff-menu').textContent.includes('Last used')`) {
			t.Error("a forgotten choice is still shown")
		}
		run(t, ctx, chromedp.Click(`#handoff-menu [role=menuitem]:not(:first-of-type)`, chromedp.ByQuery)) // ask to join club
		until(t, ctx, "the request", `document.getElementById('handoff-toast') !== null && document.getElementById('handoff-toast').textContent.includes('You asked to join club')`, 10*time.Second)
	})

	t.Run("with photos selected the bar has the button and the three-dot menu has a line, and both hand the selection over", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			upload(t, w.bob, fmt.Sprintf("button-sel-%d.jpg", i), jpegBytes(9610+i))
		}
		grid := browse(t, w.port, w.sender, look{})
		run(t, grid, chromedp.Navigate(address+"/photos"))
		until(t, grid, "photos", `document.querySelectorAll('[data-thumbnail-focus-container][data-asset]').length >= 3`, 60*time.Second)
		if js[bool](t, grid, `document.getElementById('handoff-bar-act') !== null`) {
			t.Error("a button in the bar with nothing selected")
		}
		toggleTile(t, grid, 0)
		until(t, grid, "the button in the bar", `document.getElementById('handoff-bar-act') !== null`, 10*time.Second)
		toggleTile(t, grid, 1)
		until(t, grid, "two selected", `[...document.querySelectorAll('#control-bar p')].some(p => p.textContent.trim() === '2')`, 5*time.Second)
		ids := js[[]string](t, grid, `[...document.querySelectorAll('[data-thumbnail-focus-container][data-selected=true]')].map(t => t.dataset.asset)`)
		if len(ids) != 2 {
			t.Fatalf("selected: %v", ids)
		}
		if !js[bool](t, grid, `(() => { const b = document.getElementById('handoff-bar-act'); const more = document.querySelector('#control-bar button[id^=context-menu-button]'); return b.getBoundingClientRect().right <= more.getBoundingClientRect().left + 1 && b.closest('#control-bar') !== null; })()`) {
			t.Error("the button is before the three dots, in the bar")
		}
		run(t, grid, chromedp.Click(`#handoff-bar-act`, chromedp.ByQuery))
		until(t, grid, "the list", `document.querySelectorAll('#handoff-menu [role=menuitem]').length === 2`, 10*time.Second)
		run(t, grid, chromedp.Click(`#handoff-menu [role=menuitem]`, chromedp.ByQuery))
		until(t, grid, "the message", `(document.getElementById('handoff-toast') || {textContent: ''}).textContent.includes('2 photos handed over to live')`, 15*time.Second)
		for _, id := range ids {
			a, _ := getAsset(t, w.bob, id)
			if len(a.Tags) != 1 || a.Tags[0].Name != "live-browser" {
				t.Errorf("%s: tags %+v", id, a.Tags)
			}
		}
		run(t, grid, chromedp.Click(`#handoff-toast button`, chromedp.ByQuery)) // Undo
		until(t, grid, "the undo", `document.getElementById('handoff-toast').textContent.includes('Taken off again')`, 15*time.Second)
		eventually(t, "both tags off", 10*time.Second, func() bool {
			for _, id := range ids {
				if a, _ := getAsset(t, w.bob, id); len(a.Tags) != 0 {
					return false
				}
			}
			return true
		})
		// the line in Immich's own menu does the same
		run(t, grid, chromedp.Click(`#control-bar button[id^=context-menu-button]`, chromedp.ByQuery))
		until(t, grid, "the line in the menu", `document.querySelector('#control-bar li[data-handoff-item]') !== null`, 5*time.Second)
		if got := js[string](t, grid, `document.querySelector('#control-bar li[data-handoff-item]').textContent.trim()`); got != "Hand over..." {
			t.Errorf("the line says %q", got)
		}
		run(t, grid, chromedp.Click(`#control-bar li[data-handoff-item]`, chromedp.ByQuery))
		until(t, grid, "the list", `document.querySelectorAll('#handoff-menu [role=menuitem]').length === 2`, 10*time.Second)
		// and the viewer's three dots have it too
		run(t, grid, chromedp.Navigate(address+"/photos/"+ids[0]))
		until(t, grid, "the viewer", `document.getElementById('handoff-viewer-act') !== null`, 30*time.Second)
		run(t, grid, chromedp.Click(`#immich-asset-viewer button[id^=context-menu-button]`, chromedp.ByQuery))
		until(t, grid, "the line in the viewer's menu", `document.querySelector('#immich-asset-viewer li[data-handoff-item]') !== null`, 5*time.Second)
	})

	t.Run("on a phone the selection bar still fits the button, and the menu has its line", func(t *testing.T) {
		phone := browse(t, w.port, w.sender, look{w: 390, h: 800, touch: true})
		run(t, phone, chromedp.Navigate(address+"/photos"))
		until(t, phone, "photos", `document.querySelectorAll('[data-thumbnail-focus-container][data-asset]').length >= 3`, 60*time.Second)
		var at []float64
		run(t, phone, chromedp.Evaluate(`(() => { const r = document.querySelector('[data-thumbnail-focus-container]').getBoundingClientRect(); return [r.x + r.width / 2, r.y + r.height / 2]; })()`, &at))
		run(t, phone, input.DispatchTouchEvent(input.TouchStart, []*input.TouchPoint{{X: at[0], Y: at[1]}}))
		time.Sleep(1200 * time.Millisecond) // a long press selects
		run(t, phone, input.DispatchTouchEvent(input.TouchEnd, nil))
		until(t, phone, "the menu line", `document.querySelector('#control-bar li[data-handoff-item]') !== null`, 10*time.Second)
		time.Sleep(500 * time.Millisecond)
		until(t, phone, "the button, with room for it", `document.getElementById('handoff-bar-act') !== null`, 5*time.Second)
		if !js[bool](t, phone, `getComputedStyle(document.querySelector('#handoff-bar-act span')).display === 'none'`) {
			t.Error("the words are shown on a phone")
		}
		if !js[bool](t, phone, `(() => { const n = document.querySelector('#control-bar nav'); return n.scrollWidth <= n.clientWidth; })()`) {
			t.Error("the bar overflows")
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
		until(t, phone, "the list", `document.getElementById('handoff-menu') !== null && document.getElementById('handoff-menu').textContent.includes('You do not send to any service yet.') && document.getElementById('handoff-menu').textContent.includes('Waiting for approval')`, 10*time.Second)
	})

	t.Run("when the app does not answer for the button, the viewer has none and the header link stays", func(t *testing.T) {
		quiet := w.front(t, true)
		ctx := browse(t, w.port, w.sender, look{})
		run(t, ctx, chromedp.Navigate(quiet+"/photos/"+id))
		until(t, ctx, "the header link", `document.getElementById('handoff-link') !== null`, 30*time.Second)
		until(t, ctx, "the viewer", `document.querySelector('[data-testid=asset-viewer-navbar-actions] button') !== null`, 30*time.Second)
		time.Sleep(2 * time.Second)
		if js[bool](t, ctx, `document.getElementById('handoff-viewer-act') !== null`) {
			t.Error("a button that could not work")
		}
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
