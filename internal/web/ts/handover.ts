// The hand-over button in Immich's own pages: a button in the photo viewer's toolbar (and, in the next parts, in the
// bar that shows when photos are selected and in Immich's three-dot menus), a list of the services the person sends
// to, and a short message with Undo once they have chosen one.
//
// It runs where immich-button.ts does: a reverse proxy adds the script to Immich's pages, and the app is on the same
// address under /handoff/ (docs/admin/find-handoff.md). So a request to the app carries the person's Immich cookie and needs
// no key. The script never tags anything itself: it asks the app, which checks the person and the photos.
//
// Immich offers no place for any of this. What is looked for, and why, is in docs/contributing/design/immich-button-design.md, and
// TestImmichMarkup holds Immich to it. When something is not found the button is simply not shown.

import { addForm, assetOfPath, choose, countIn, formOf, inOrder, ownedQuery, Selection, splitOwned, wordsOf, type Info, type Owner } from "./handover-core.ts";
import { addStyles, oncePerFrame } from "./immich-ui.ts";

const CSS = `
.handoff-act{display:flex;flex:none;align-items:center;justify-content:center;gap:8px;height:40px;min-width:40px;padding:0 10px;border:0;border-radius:9999px;background:transparent;color:inherit;font:inherit;font-size:14px;font-weight:500;cursor:pointer}
.handoff-act img{display:block;width:26px;height:26px;border-radius:7px}
.handoff-act:hover{background:rgb(127 127 127 / .2)}
.handoff-act:focus-visible{outline:2px solid currentColor;outline-offset:2px}
.handoff-act span{white-space:nowrap}
@media (max-width:767px){.handoff-act span{display:none}}
#handoff-menu{position:fixed;z-index:2147483000;box-sizing:border-box;width:min(320px,calc(100vw - 24px));max-height:70vh;overflow:auto;border-radius:12px;background:#fff;color:#1b1b2e;box-shadow:0 8px 30px rgb(0 0 0 / .35);font:14px/1.4 system-ui,sans-serif;padding:6px}
.dark #handoff-menu{background:#232932;color:#e7e9f2}
#handoff-menu h2{margin:0;padding:8px 10px 4px;font-size:12px;font-weight:600;letter-spacing:.04em;text-transform:uppercase;opacity:.7}
#handoff-menu button,#handoff-menu a{display:block;box-sizing:border-box;width:100%;padding:10px;border:0;border-radius:8px;background:transparent;color:inherit;font:inherit;text-align:start;text-decoration:none;cursor:pointer}
#handoff-menu button:hover,#handoff-menu a:hover,#handoff-menu button:focus-visible,#handoff-menu a:focus-visible{background:rgb(127 127 127 / .18);outline:none}
#handoff-menu small{display:block;opacity:.7;font-size:12px}
#handoff-menu p{margin:0;padding:10px}
#handoff-menu .foot{margin-top:4px;border-top:1px solid rgb(127 127 127 / .3);padding-top:4px}
@media (max-width:639px){#handoff-menu{left:12px!important;right:12px;bottom:12px;top:auto!important;width:auto}}
#handoff-toast{position:fixed;z-index:2147483000;left:50%;bottom:20px;transform:translateX(-50%);box-sizing:border-box;max-width:min(520px,calc(100vw - 24px));display:flex;flex-wrap:wrap;align-items:center;gap:4px 12px;padding:10px 14px;border-radius:12px;background:#241a66;color:#fff;box-shadow:0 8px 30px rgb(0 0 0 / .4);font:14px/1.4 system-ui,sans-serif}
#handoff-toast button,#handoff-toast a{padding:4px 8px;border:0;border-radius:6px;background:rgb(255 255 255 / .16);color:#fff;font:inherit;font-weight:600;text-decoration:none;cursor:pointer}
#handoff-toast button:hover,#handoff-toast a:hover{background:rgb(255 255 255 / .3)}
`;

/** Starts the button. `app` is the address of the app's pages; `icon` is its icon. */
export function startHandover(app: string, icon: string): void {
  addStyles(CSS);

  let info: Info | null = null;
  let loading: Promise<Info | null> | null = null;
  let failedAt = 0;
  const word = (id: string): string => wordsOf(info)(id);

  /** What the app says the person can do. A person the app does not know, or an app that is off, hides the button. */
  const load = (): Promise<Info | null> => {
    if (info) return Promise.resolve(info);
    if (Date.now() - failedAt < 60_000) return Promise.resolve(null);
    loading ??= fetch(new URL("immich/services", app), { credentials: "same-origin", headers: { Accept: "application/json" } })
      .then((res) => (res.ok ? (res.json() as Promise<Info>) : null))
      .catch(() => null)
      .then((got) => {
        loading = null;
        if (got && Array.isArray(got.services) && got.words) info = got;
        else failedAt = Date.now();
        schedule();
        return info;
      });
    return loading;
  };

  const element = <K extends keyof HTMLElementTagNameMap>(tag: K, props: Partial<HTMLElementTagNameMap[K]> = {}, ...kids: (Node | string)[]): HTMLElementTagNameMap[K] => {
    const el = Object.assign(document.createElement(tag), props);
    el.append(...kids);
    return el;
  };

  // ---- the message after a press ----
  let toastTimer = 0;
  const closeToast = (): void => {
    clearTimeout(toastTimer);
    document.getElementById("handoff-toast")?.remove();
  };
  const toast = (message: string, ...actions: HTMLElement[]): void => {
    closeToast();
    const box = element("div", { id: "handoff-toast" }, element("span", { textContent: message }), ...actions);
    box.setAttribute("role", "status");
    document.body.append(box);
    toastTimer = window.setTimeout(closeToast, 9000);
  };
  // in a tab of its own: the person is in the middle of something in Immich and must find it as it was
  const openApp = (): HTMLAnchorElement => element("a", { href: app, target: "_blank", rel: "noopener", textContent: word("Open Handoff") });

  const post = async (path: string, body: URLSearchParams): Promise<{ ok: boolean; message: string; data: Record<string, unknown> }> => {
    try {
      const res = await fetch(new URL(path, app), {
        method: "POST",
        credentials: "same-origin",
        headers: { "X-Requested-With": "immich-handoff", "Content-Type": "application/x-www-form-urlencoded", Accept: "application/json" },
        body,
      });
      const text = await res.text();
      if (!res.ok) return { ok: false, message: text.trim(), data: {} };
      const data = JSON.parse(text) as Record<string, unknown>;
      return { ok: true, message: typeof data.message === "string" ? data.message : "", data };
    } catch {
      return { ok: false, message: "", data: {} };
    }
  };

  // the service used last is kept in this browser only, and offered first
  const LAST = "handoff-last-service";
  const remembered = (): string | null => {
    try {
      return localStorage.getItem(LAST);
    } catch {
      return null;
    }
  };
  const remember = (name: string | null): void => {
    try {
      if (name) localStorage.setItem(LAST, name);
      else localStorage.removeItem(LAST);
    } catch {
      /* storage is off: nothing is remembered */
    }
  };

  const handOver = async (service: string, assets: string[]): Promise<void> => {
    toast(word("Handing over..."));
    const done = await post("immich/send", formOf(service, assets));
    if (!done.ok) {
      toast(done.message || word("Could not hand over"), openApp());
      return;
    }
    const undo = element("button", { type: "button", textContent: word("Undo") });
    undo.addEventListener("click", async () => {
      undo.disabled = true;
      const back = await post("immich/undo", formOf(service, assets));
      toast(back.ok ? back.message || word("Taken off again") : back.message || word("Could not hand over"), openApp());
    });
    remember(service);
    toast(done.message, undo, openApp());
  };

  // ---- whose the photos are ----
  // Hand over is for the person's own photos, Add to my library for what others share with them, so the app is asked once
  // for each photo (a few at a time, after the selection settles). Until it has answered nothing is shown for them.
  const owners = new Map<string, Owner>();
  const asked = new Set<string>();
  const wanted = new Set<string>();
  let askTimer = 0;
  let inflight: Promise<void> | null = null;
  /** Asks the app about the photos waiting to be asked about (up to 100 at a time, as the address would get long). */
  const flushOwners = (): Promise<void> => {
    if (inflight) return inflight;
    if (wanted.size === 0) return Promise.resolve();
    const batch = [...wanted].slice(0, 100);
    for (const id of batch) {
      wanted.delete(id);
      asked.add(id);
    }
    inflight = (async () => {
      let answered = false;
      try {
        const res = await fetch(new URL(`immich/owned?${ownedQuery(batch)}`, app), { credentials: "same-origin", headers: { Accept: "application/json" } });
        if (res.ok) {
          const got = (await res.json()) as { mine?: string[]; others?: string[] };
          for (const id of got.mine ?? []) owners.set(id, "mine");
          for (const id of got.others ?? []) owners.set(id, "other");
          answered = true;
        }
      } catch {
        /* the app did not answer: the photos are treated as the person's own, as before this question existed */
      }
      if (!answered) for (const id of batch) owners.set(id, "mine");
      schedule();
    })().finally(() => {
      inflight = null;
      if (wanted.size > 0) askOwners([]);
    });
    return inflight;
  };
  const askOwners = (ids: string[]): void => {
    for (const id of ids) if (!owners.has(id) && !asked.has(id)) wanted.add(id);
    if (wanted.size === 0 || askTimer) return;
    askTimer = window.setTimeout(() => {
      askTimer = 0;
      void flushOwners();
    }, 250);
  };
  /** Waits until every photo asked about has been answered, so a press acts on all the photos selected, not on the ones known so far. */
  const settle = async (): Promise<void> => {
    clearTimeout(askTimer);
    askTimer = 0;
    for (let round = 0; round < 5 && (wanted.size > 0 || inflight); round++) await flushOwners();
  };

  // ---- the list of services ----
  const closeMenu = (): void => {
    document.getElementById("handoff-menu")?.remove();
    document.removeEventListener("pointerdown", outside, true);
    document.removeEventListener("keydown", escape, true);
  };
  const outside = (e: Event): void => {
    const menu = document.getElementById("handoff-menu");
    if (menu && !menu.contains(e.target as Node) && !(e.target as Element).closest?.(".handoff-act")) closeMenu();
  };
  const escape = (e: KeyboardEvent): void => {
    if (e.key === "Escape") {
      e.stopPropagation();
      closeMenu();
    }
  };

  /** Shows the services the person can hand `assets()` over to, under `anchor` (or as a sheet at the bottom of a phone). */
  const openMenu = async (anchor: Element, assets: () => string[]): Promise<void> => {
    closeMenu();
    const got = await load();
    if (!got) return;
    const menu = element("div", { id: "handoff-menu" });
    menu.setAttribute("role", "menu");
    menu.append(element("h2", { textContent: word("Hand over to") }));
    if (got.services.length === 0) menu.append(element("p", { textContent: word("You do not send to any service yet.") }));
    const last = remembered();
    for (const svc of inOrder(got.services, last)) {
      const item = element("button", { type: "button" }, svc.name);
      item.setAttribute("role", "menuitem");
      const about = svc.note || word(svc.copy ? "Your original stays in your library" : "Your original goes to your trash once the other side has a copy");
      const detail = svc.name === last ? `${word("Last used")} · ${about}` : about;
      item.append(element("small", { textContent: detail }));
      item.addEventListener("click", () => {
        const chosen = assets();
        closeMenu();
        if (chosen.length > 0) void handOver(svc.name, chosen);
      });
      menu.append(item);
    }
    const offers = got.offers ?? [];
    if (offers.length > 0) menu.append(element("h2", { textContent: word("Ask to join") }));
    for (const offer of offers) {
      const item = element("button", { type: "button", disabled: offer.pending }, offer.name);
      item.setAttribute("role", "menuitem");
      if (offer.pending) item.append(element("small", { textContent: word("Waiting for approval") }));
      else
        item.addEventListener("click", async () => {
          closeMenu();
          const asked = await post("immich/ask", formOf(offer.name, []));
          if (asked.ok && info?.offers) for (const o of info.offers) if (o.name === offer.name) o.pending = true;
          toast(asked.message || word("Could not hand over"), openApp());
        });
      menu.append(item);
    }
    const foot = element("div", { className: "foot" }, openApp());
    if (last) {
      const forget = element("button", { type: "button", textContent: word("Forget my choice") });
      forget.addEventListener("click", () => {
        remember(null);
        closeMenu();
      });
      foot.append(forget);
    }
    menu.append(foot);
    document.body.append(menu);
    const at = anchor.getBoundingClientRect();
    const room = menu.getBoundingClientRect();
    menu.style.left = `${Math.max(12, Math.min(at.right - room.width, window.innerWidth - room.width - 12))}px`;
    menu.style.top = `${Math.min(at.bottom + 6, window.innerHeight - room.height - 12)}px`;
    menu.querySelector<HTMLElement>("button, a")?.focus();
    document.addEventListener("pointerdown", outside, true);
    document.addEventListener("keydown", escape, true);
  };

  // ---- Add to my library ----
  const placeMenu = (menu: HTMLElement, anchor: Element): void => {
    document.body.append(menu);
    const at = anchor.getBoundingClientRect();
    const room = menu.getBoundingClientRect();
    menu.style.left = `${Math.max(12, Math.min(at.right - room.width, window.innerWidth - room.width - 12))}px`;
    menu.style.top = `${Math.min(at.bottom + 6, window.innerHeight - room.height - 12)}px`;
    menu.querySelector<HTMLElement>("button, a, select, input")?.focus();
    document.addEventListener("pointerdown", outside, true);
    document.addEventListener("keydown", escape, true);
  };

  const addToLibrary = async (assets: string[], dest: string, album: string, remember: boolean): Promise<void> => {
    toast(word("Adding to your library..."));
    const done = await post("immich/add", addForm(assets, dest, album, remember));
    if (!done.ok) {
      toast(done.message || word("Could not add"), openApp());
      return;
    }
    if (remember && info) info.library = { dest, album, chosen: true };
    const copies = Array.isArray(done.data.copies) ? (done.data.copies as string[]) : [];
    const actions: HTMLElement[] = [];
    if (copies.length > 0) {
      const undo = element("button", { type: "button", textContent: word("Undo") });
      undo.addEventListener("click", async () => {
        undo.disabled = true;
        const form = formOf("", copies);
        if (dest === "album") form.set("album", album);
        const back = await post("immich/add-undo", form);
        toast(back.ok ? back.message : back.message || word("Could not add"), openApp());
      });
      actions.push(undo);
    }
    toast(done.message, ...actions, openApp());
  };

  /** The first time: where should the copies go? Once the person has chosen to remember it, a press adds at once. */
  const openAdd = async (anchor: Element, assets: () => string[]): Promise<void> => {
    closeMenu();
    const got = await load();
    if (!got) return;
    const lib = got.library ?? { dest: "tag", album: "", chosen: false };
    if (lib.chosen) {
      const chosen = assets();
      if (chosen.length > 0) void addToLibrary(chosen, lib.dest, lib.album, false);
      return;
    }
    const menu = element("div", { id: "handoff-menu" });
    menu.setAttribute("role", "menu");
    menu.append(element("h2", { textContent: word("Where should it go?") }));
    const remember = element("input", { type: "checkbox", checked: true });
    const go = (dest: string, album: string): void => {
      const chosen = assets();
      const keep = remember.checked;
      closeMenu();
      if (chosen.length > 0) void addToLibrary(chosen, dest, album, keep);
    };
    for (const [dest, label] of [
      ["tag", "In my library, tagged with who shared it"],
      ["plain", "In my library"],
    ] as const) {
      const item = element("button", { type: "button", textContent: word(label) });
      item.setAttribute("role", "menuitem");
      item.addEventListener("click", () => go(dest, ""));
      menu.append(item);
    }
    const inAlbum = element("button", { type: "button", textContent: word("In an album") });
    inAlbum.setAttribute("role", "menuitem");
    const albumBox = element("div");
    const showAlbums = async (): Promise<void> => {
      albumBox.replaceChildren();
      try {
        const res = await fetch(new URL("immich/albums", app), { credentials: "same-origin", headers: { Accept: "application/json" } });
        if (!res.ok) throw new Error("albums");
        const list = ((await res.json()) as { albums?: { name: string }[] }).albums ?? [];
        const pick = element("select", {}, element("option", { value: "", textContent: word("Choose an album") }));
        for (const a of list) pick.append(element("option", { value: a.name, textContent: a.name }));
        const fresh = element("input", { type: "text", maxLength: 100, placeholder: word("or the name of a new album") });
        const confirm = element("button", { type: "button", textContent: word("Add here") });
        confirm.addEventListener("click", () => {
          const name = fresh.value.trim() || pick.value;
          if (name) go("album", name);
        });
        albumBox.append(pick, fresh, confirm);
        pick.focus();
      } catch {
        const retry = element("button", { type: "button", textContent: word("Try again") });
        retry.addEventListener("click", () => void showAlbums());
        albumBox.append(element("p", { textContent: word("Could not list your albums") }), retry);
      }
    };
    inAlbum.addEventListener("click", () => void showAlbums());
    menu.append(inAlbum, albumBox);
    const keep = element("label", {}, remember, " ", word("Remember this choice"));
    menu.append(element("div", { className: "foot" }, keep));
    placeMenu(menu, anchor);
  };

  // ---- the button ----
  const makeButton = (id: string, assets: () => string[], like: Element | null, kind: "hand" | "add"): HTMLButtonElement => {
    const label = word(kind === "add" ? "Add to my library" : "Hand over...");
    const button = element("button", { id, type: "button", className: "handoff-act", title: label }, element("img", { src: icon, alt: "" }), element("span", { textContent: label }));
    button.setAttribute("aria-label", label);
    button.setAttribute("aria-haspopup", "menu");
    if (like) button.style.color = getComputedStyle(like).color; // the text of the bar it sits in, light or dark
    button.addEventListener("click", (e) => {
      e.stopPropagation();
      void settle().then(() => (kind === "add" ? openAdd : openMenu)(button, assets));
    });
    return button;
  };

  /** The child of `bar` that holds the three dots of a toolbar (the place the button goes in front of), if there are any. */
  const beforeDots = (bar: Element): Element | null => {
    let dots: Element | null = bar.querySelector('button[aria-haspopup][id^="context-menu-button"]');
    while (dots && dots.parentElement !== bar) dots = dots.parentElement;
    return dots;
  };

  /** Puts a button in a toolbar before its three dots, or takes it out when it should not be there. */
  const mount = (id: string, bar: Element | null, assets: (() => string[]) | null, kind: "hand" | "add"): HTMLButtonElement | null => {
    const own = document.getElementById(id) as HTMLButtonElement | null;
    if (!bar || !assets || !info) {
      own?.remove();
      return null;
    }
    const button = own ?? makeButton(id, assets, bar.querySelector("button"), kind);
    const before = beforeDots(bar);
    // somewhere before the three dots (two buttons can sit there, so "right before" would have them move each other)
    if (button.parentElement !== bar || (before && !(button.compareDocumentPosition(before) & Node.DOCUMENT_POSITION_FOLLOWING))) bar.insertBefore(button, before);
    return button;
  };

  /** "Hand over..." as a line of Immich's own three-dot menu, made from one of its lines so it looks like them. */
  const mountItem = (menu: Element, dots: Element | null, assets: (() => string[]) | null, kind: "hand" | "add"): void => {
    const own = menu.querySelector<HTMLElement>(`li[data-handoff-item="${kind}"]`);
    const first = menu.querySelector<HTMLElement>('li[role="menuitem"]:not([data-handoff-item])');
    if (!assets || !info || !first) {
      own?.remove();
      return;
    }
    if (own) {
      // at the end of the menu, after its other lines (our two lines may follow each other there)
      let next = own.nextElementSibling;
      while (next?.matches("[data-handoff-item]")) next = next.nextElementSibling;
      if (next && !menu.querySelector("hr")) menu.append(own);
      return;
    }
    const item = first.cloneNode(true) as HTMLElement;
    item.removeAttribute("id");
    item.dataset.handoffItem = kind;
    item.querySelector("svg")?.replaceWith(element("img", { src: icon, alt: "", width: 20, height: 20 }));
    const text = item.querySelector("div div") ?? item.querySelector("div") ?? item;
    text.textContent = word(kind === "add" ? "Add to my library" : "Hand over...");
    item.addEventListener("click", () => void settle().then(() => (kind === "add" ? openAdd : openMenu)(dots ?? item, assets)));
    const rule = menu.querySelector("hr");
    menu.insertBefore(item, rule);
  };

  // ---- what is selected ----
  const picked = new Selection();
  const tiles = (): { id: string; selected: boolean }[] =>
    [...document.querySelectorAll<HTMLElement>("[data-thumbnail-focus-container][data-asset]")].map((t) => ({ id: t.dataset.asset ?? "", selected: t.dataset.selected === "true" }));
  let noRoom = false; // the selection bar is full (a phone): the menu line is the way there
  window.addEventListener("resize", () => {
    noRoom = false;
    schedule();
  });

  // the selection bar: Immich 3.2 to 3.3 gave it an id, from 3.4 it has a data-testid
  const BAR = '[data-testid="control-bar"], #control-bar';
  const barOf = (n: Element): Element | null => n.closest(BAR);

  // read when the button is pressed, not when it was made: the selection changes while the button stays
  const inViewer = (): string[] => [assetOfPath(location.pathname) ?? ""].filter(Boolean);
  const inGrid = (): string[] => {
    const bar = document.querySelector(BAR);
    return bar ? (picked.idsFor(countIn([...bar.querySelectorAll("p")].map((p) => p.textContent ?? ""))) ?? []) : [];
  };

  const place = (): void => {
    // the viewer: one photo, the one in the address
    const viewer = document.querySelector('#immich-asset-viewer [data-testid="asset-viewer-navbar-actions"]');
    const photo = assetOfPath(location.pathname);

    // the grid: the photos selected, when they can be counted
    const bar = document.querySelector(BAR);
    if (bar) picked.see(tiles());
    else picked.clear();
    const selected = bar ? inGrid() : [];

    if ((viewer && photo) || bar) void load();
    // whose the photos are, for the buttons: Hand over for the person's own, Add to my library for the others
    askOwners([...(viewer && photo ? inViewer() : []), ...selected]);
    const mineOf = (ids: () => string[]) => (): string[] => splitOwned(ids(), owners).mine;
    const othersOf = (ids: () => string[]) => (): string[] => splitOwned(ids(), owners).others;
    const inViewerNow = viewer && photo ? choose(splitOwned(inViewer(), owners)) : { hand: false, add: false };
    const inGridNow = selected.length > 0 ? choose(splitOwned(selected, owners)) : { hand: false, add: false };
    const handOne = inViewerNow.hand ? mineOf(inViewer) : null;
    const addOne = inViewerNow.add ? othersOf(inViewer) : null;
    const handMany = inGridNow.hand ? mineOf(inGrid) : null;
    const addMany = inGridNow.add ? othersOf(inGrid) : null;

    mount("handoff-viewer-act", viewer, handOne, "hand");
    mount("handoff-viewer-add", viewer, addOne, "add");
    const selectionBar = bar?.querySelector("nav") ?? null;
    // the group of buttons before the three dots; a bar with no three dots (a partner's photos page: Share, Add to album, Download)
    // has the group of actions at its right, the last group of the nav that is not the first (Close and the count)
    const target =
      selectionBar?.querySelector('button[aria-haspopup][id^="context-menu-button"]')?.closest("nav > div") ??
      [...(selectionBar?.children ?? [])].filter((c, i) => i > 0 && c.tagName === "DIV" && c.querySelector("button")).pop() ??
      null;
    const there = noRoom ? null : target;
    const hand = mount("handoff-bar-act", there, handMany, "hand");
    const add = mount("handoff-bar-add", there, addMany, "add");
    if ((hand || add) && selectionBar && selectionBar.scrollWidth > selectionBar.clientWidth) {
      noRoom = true; // the bar did not have the room: only the menu lines are offered
      hand?.remove();
      add?.remove();
    }

    for (const m of document.querySelectorAll(`:is(${BAR}) ul[role="menu"], #immich-asset-viewer ul[role="menu"]`)) {
      const inBar = barOf(m) !== null;
      const labelled = m.getAttribute("aria-labelledby");
      const dots = labelled ? document.getElementById(labelled) : null; // the three dots that open this menu
      mountItem(m, dots, inBar ? handMany : viewer ? handOne : null, "hand");
      mountItem(m, dots, inBar ? addMany : viewer ? addOne : null, "add");
    }
  };

  const schedule = oncePerFrame(place);
  new MutationObserver(schedule).observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ["data-selected"] });
  window.addEventListener("popstate", () => {
    closeMenu();
    schedule();
  });
  schedule();
}
