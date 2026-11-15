# Handing over from inside Immich: how the button finds its place

This is the design note for the hand-over button in Immich's own pages (issue #49). The header link already in Immich
(`internal/web/ts/immich-button.ts`, set up as in [find-handoff.md](find-handoff.md)) shows that a script added by a
reverse proxy can live in Immich's page. This note records what Immich's page looks like to such a script, what the
button may rely on, and what it must never do. It was found by driving a real Chrome against Immich 3.2.0 through the
same proxy the browser tests use; `TestImmichMarkup` in `test/e2e` holds Immich to every fact below.

Immich offers no extension points. Everything here is read from its markup, which can change in any release. So the
rules are: look for the few things below, put nothing anywhere else, and **hide the button when anything is missing**
rather than guess.

## How the script talks to the app

The page is Immich's, and the app is on the same address under `/handoff/` (that is already required for the header
link). A `fetch` to `/handoff/...` is therefore same-origin: it carries the person's Immich cookie, needs no CORS
setting and needs no key or token in the page. The app checks the person from the cookie, as it does for its own
pages, and does the tagging itself. A state-changing request carries the header `X-Requested-With`, as the app's forms
do. The script never holds a key and never tags anything itself.

## What the script can read

| Need | Where it is (Immich 3.2.0) | Stable how |
|---|---|---|
| One photo's id, in the viewer | the address: `/photos/<uuid>` (also inside albums, people and places, always the last part) | part of Immich's routes |
| Ids of the photos in a grid | each tile is `[data-thumbnail-focus-container]` with `data-asset="<uuid>"` | a data attribute for Immich's own tests; the thumbnail's address `/api/assets/<uuid>/thumbnail` repeats it |
| Which tiles are selected | the same tile has `data-selected="true"` | same |
| How many are selected | the bar's first `<p>` holds the bare number (the second is the words, in the person's language) | a number, so it needs no translation |
| The selection bar | `#control-bar`, a `nav` inside it | an id |
| The viewer's toolbar | `[data-testid="asset-viewer-navbar-actions"]` inside `#immich-asset-viewer` | a test id |
| The three-dot menus | in both bars, a `button[aria-haspopup][id^="context-menu-button"]` and, beside it in the page all the time, a `ul[role="menu"]` of `li[role="menuitem"]` | ARIA roles |

The button's own text is never matched against Immich's words: they are translated, so a label such as "Share" is not
a way to find anything.

### The selection is only partly in the page

Immich draws only the tiles near the screen. A selected tile that is scrolled out of view is removed from the page and
drawn again when it returns. And "Select all" selects photos that were never drawn. So the page alone cannot always
tell which photos are selected. The script therefore:

- keeps its own set of ids: a tile seen with `data-selected="true"` is added, one seen without it is removed, and the
  set is emptied when the selection bar goes;
- compares the size of the set with the number Immich shows in the bar;
- **offers the button only when the two agree**. When they do not (after "Select all" on a long timeline, say) the
  button is hidden, and the bar's menu entry says nothing either. It never sends a guess.

### The check circle exists only under the pointer

A tile's check circle (`button[role=checkbox]`) is drawn while the pointer is over the tile, and on a phone selection
is a long press. Nothing here depends on the circle: the script only reads `data-selected`.

## Where it goes

- **Viewer toolbar.** A button is added at the end of `[data-testid="asset-viewer-navbar-actions"]`, before the
  container of the three-dot menu. The toolbar scrolls sideways when it is crowded, so on a phone the button is there,
  further along. Immich redraws the toolbar when the person moves between photos; a `MutationObserver`, as for the
  header link, puts the button back at most once per frame.
- **Selection bar.** A button is added in the right-hand group of `#control-bar`, before the three-dot menu. On a phone
  that bar is already full to the edge of the screen (Immich 3.2.0: 374 px of 374 px at 390 px wide), so after adding
  the button the script checks that the bar did not grow past its width (`scrollWidth > clientWidth`), and if it did,
  takes the button out again and leaves the menu entry as the only way.
- **Three-dot menus.** An `li[role="menuitem"]`, made by copying an existing one (so it has Immich's own look, light
  and dark) and given the words "Hand over...", is added to the end of the `ul[role="menu"]` that is already in the
  page. It stays there when the menu is closed and opened again, and a click on it is received by the script and closes
  Immich's menu by itself. The script then opens its own list of services.

If Immich's right-click menu on a tile exists in a later version, it is handled the same way, by the same rule: a
`ul[role="menu"]` that is seen is given the item.

## What the pop-up looks like

The list of services is the script's own element, not Immich's. It is placed on the page with `position: fixed` and a
`z-index` above Immich's menus, which use `z-70` (the header link's floating fallback already uses `2147483000`).
Its style is added as a constructed stylesheet, like the header link's, because that is not held back by a page's
content security policy and a `<style>` element can be. Its colours follow Immich's own light or dark look, read from
the page's computed colours, with a fixed fallback. A phone gets the list at the bottom, as a sheet.

## What a content security policy allows

Immich itself sends none in 3.2.0. A policy added by a proxy or by the person may forbid inline styles, which the
constructed stylesheet is not held back by, and may forbid connecting anywhere but the page's own origin. The button
only ever connects to the page's own origin, `/handoff/`, and loads the script from there. A policy that forbids that
also forbids the header link, and the button then is simply not there.

## What it must never do

- Send anything without a press. No request is made on page load beyond reading the person's services (and that read
  is the only request, made once when the first bar or toolbar shows).
- Guess: a selection that cannot be counted, a person who is not known to the app, an app that cannot be reached on the
  same address, or any piece of Immich's markup not found, each hide the button.
- Hold or show a key, or ask the app for more than the person's own services and own photos.

## How it is held to this

`TestImmichMarkup` checks, on the pinned Immich: that a tile carries its id and a `data-selected` that follows the
person's choice; that the bar has a number, a three-dot menu and a place beside it; that an item added to the menu stays
and is clicked and closes the menu; that the viewer has the id in its address and the toolbar and menu named above; and
that on a phone the bar is full. When Immich is upgraded and one of them moves, that test fails and says which.
