//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

// TestImmichMarkup holds Immich's own page to what the hand-over button relies on (docs/immich-button-design.md): the
// photo viewer's toolbar, the bar that shows when photos are selected, the tiles and where they keep a photo's id, and
// the menus that open from three dots. Immich offers no hooks for this, so when it is upgraded and one of these
// moves, this is the test that says so, before a button quietly disappears.
func TestImmichMarkup(t *testing.T) {
	w := theWorld(t)
	for i := 0; i < 3; i++ {
		upload(t, w.bob, fmt.Sprintf("markup-%d.jpg", i), jpegBytes(7700+i))
	}
	address := w.inFrontOfImmich(t)

	ctx := browse(t, w.port, w.sender, look{})
	run(t, ctx, chromedp.Navigate(address+"/photos"))
	until(t, ctx, "photos in the timeline", `document.querySelectorAll('[data-thumbnail-focus-container][data-asset]').length >= 3`, 60*time.Second)

	// the check circle is drawn only while the pointer is over a tile
	toggle := func(i int) {
		t.Helper()
		var at []float64
		run(t, ctx, chromedp.Evaluate(fmt.Sprintf(`(() => { const r = document.querySelectorAll('[data-thumbnail-focus-container]')[%d].getBoundingClientRect(); return [r.x + r.width / 2, r.y + r.height / 2]; })()`, i), &at))
		run(t, ctx, input.DispatchMouseEvent(input.MouseMoved, at[0], at[1]))
		until(t, ctx, "the check circle", fmt.Sprintf(`document.querySelectorAll('[data-thumbnail-focus-container]')[%d].querySelector('button[role=checkbox]') !== null`, i), 5*time.Second)
		run(t, ctx, chromedp.Evaluate(fmt.Sprintf(`document.querySelectorAll('[data-thumbnail-focus-container]')[%d].querySelector('button[role=checkbox]').click()`, i), nil))
	}

	t.Run("a tile keeps the photo's id and a check circle, and a selected one says so", func(t *testing.T) {
		got := js[string](t, ctx, `(() => {
			const tile = document.querySelector('[data-thumbnail-focus-container]');
			return [/^[0-9a-f-]{36}$/.test(tile.dataset.asset), tile.querySelector('img[src*="/api/assets/' + tile.dataset.asset + '/thumbnail"]') !== null, tile.hasAttribute('data-selected')].join(',');
		})()`)
		if got != "true,true,false" {
			t.Fatalf("id, the same id in its thumbnail, not selected: %q", got)
		}
		toggle(0)
		until(t, ctx, "the tile to say it is selected", `document.querySelector('[data-thumbnail-focus-container]').dataset.selected === 'true'`, 5*time.Second)
	})

	t.Run("the selection bar has a count, a three-dot menu and room beside it", func(t *testing.T) {
		until(t, ctx, "the bar", `document.querySelector('#control-bar nav') !== null`, 5*time.Second)
		got := js[string](t, ctx, `(() => {
			const bar = document.querySelector('#control-bar');
			const more = bar.querySelector('button[aria-haspopup][id^=context-menu-button]');
			const count = [...bar.querySelectorAll('p')].map(p => p.textContent.trim()).find(x => /^\d+$/.test(x));
			return [count, more !== null, bar.querySelector('ul[role=menu]') !== null, more && more.parentElement.parentElement !== null].join(',');
		})()`)
		if got != "1,true,true,true" {
			t.Fatalf("count, three dots, menu, a place beside it: %q", got)
		}
		// a second selected photo is counted, and the bar goes when nothing is selected
		toggle(1)
		until(t, ctx, "two selected", `[...document.querySelectorAll('#control-bar p')].some(p => p.textContent.trim() === '2')`, 5*time.Second)
		run(t, ctx, chromedp.Evaluate(`document.querySelector('#control-bar button[aria-label]').click()`, nil)) // the first button closes the selection
		until(t, ctx, "the bar to go", `document.querySelector('#control-bar') === null || document.querySelector('[data-selected=true]') === null`, 5*time.Second)
	})

	t.Run("an item added to the three-dot menu stays and is clicked", func(t *testing.T) {
		toggle(0)
		until(t, ctx, "the bar", `document.querySelector('#control-bar ul[role=menu]') !== null`, 5*time.Second)
		run(t, ctx, chromedp.Evaluate(`(() => { const ul = document.querySelector('#control-bar ul[role=menu]'); const li = ul.querySelector('li[role=menuitem]').cloneNode(true); li.id = 'probe-item'; li.addEventListener('click', () => { window.__probe = (window.__probe || 0) + 1; }); ul.append(li); })()`, nil))
		run(t, ctx, chromedp.Evaluate(`document.querySelector('#control-bar button[aria-haspopup]').click()`, nil))
		until(t, ctx, "the added line to be there", `document.getElementById('probe-item') !== null`, 5*time.Second)
		run(t, ctx, chromedp.Evaluate(`document.getElementById('probe-item').click()`, nil))
		until(t, ctx, "the click to be seen", `window.__probe === 1`, 5*time.Second)
	})

	t.Run("the viewer has its address, a toolbar of buttons and a three-dot menu", func(t *testing.T) {
		id := js[string](t, ctx, `document.querySelector('[data-thumbnail-focus-container]').dataset.asset`)
		run(t, ctx, chromedp.Navigate(address+"/photos/"+id))
		until(t, ctx, "the viewer toolbar", `document.querySelector('#immich-asset-viewer [data-testid=asset-viewer-navbar-actions] button') !== null`, 20*time.Second)
		got := js[string](t, ctx, `(() => {
			const bar = document.querySelector('[data-testid=asset-viewer-navbar-actions]');
			const more = bar.querySelector('button[aria-haspopup][id^=context-menu-button]');
			const id = (location.pathname.match(/\/photos\/([0-9a-f-]{36})\/?$/) || [])[1];
			return [id === '`+id+`', more !== null, bar.querySelector('ul[role=menu]') !== null].join(',');
		})()`)
		if got != "true,true,true" {
			t.Fatalf("the photo's id in the address, three dots, menu: %q", got)
		}
	})

	t.Run("on a phone the selection bar is full and the viewer's toolbar scrolls", func(t *testing.T) {
		phone := browse(t, w.port, w.sender, look{w: 390, h: 800, touch: true})
		run(t, phone, chromedp.Navigate(address+"/photos"))
		until(t, phone, "photos", `document.querySelectorAll('[data-thumbnail-focus-container][data-asset]').length >= 3`, 60*time.Second)
		var at []float64
		run(t, phone, chromedp.Evaluate(`(() => { const r = document.querySelector('[data-thumbnail-focus-container]').getBoundingClientRect(); return [r.x + r.width / 2, r.y + r.height / 2]; })()`, &at))
		run(t, phone, input.DispatchTouchEvent(input.TouchStart, []*input.TouchPoint{{X: at[0], Y: at[1]}}))
		time.Sleep(1200 * time.Millisecond) // a long press selects
		run(t, phone, input.DispatchTouchEvent(input.TouchEnd, nil))
		until(t, phone, "the bar", `document.querySelector('#control-bar nav') !== null`, 5*time.Second)
		if !js[bool](t, phone, `(() => { const n = document.querySelector('#control-bar nav'); return n.scrollWidth <= n.clientWidth && n.getBoundingClientRect().width < 400; })()`) {
			t.Error("the selection bar on a phone no longer fits its own buttons: the script's check for room needs another look")
		}
	})
}
