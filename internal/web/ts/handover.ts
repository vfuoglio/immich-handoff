// The hand-over button in Immich's own pages: a button in the photo viewer's toolbar (and, in the next parts, in the
// bar that shows when photos are selected and in Immich's three-dot menus), a list of the services the person sends
// to, and a short message with Undo once they have chosen one.
//
// It runs where immich-button.ts does: a reverse proxy adds the script to Immich's pages, and the app is on the same
// address under /handoff/ (docs/find-handoff.md). So a request to the app carries the person's Immich cookie and needs
// no key. The script never tags anything itself: it asks the app, which checks the person and the photos.
//
// Immich offers no place for any of this. What is looked for, and why, is in docs/immich-button-design.md, and
// TestImmichMarkup holds Immich to it. When something is not found the button is simply not shown.

import { assetOfPath, formOf, wordsOf, type Info } from "./handover-core.ts";
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
  const openApp = (): HTMLAnchorElement => element("a", { href: app, textContent: word("Open Handoff") });

  const post = async (path: string, service: string, assets: string[]): Promise<{ ok: boolean; message: string }> => {
    try {
      const res = await fetch(new URL(path, app), {
        method: "POST",
        credentials: "same-origin",
        headers: { "X-Requested-With": "immich-handoff", "Content-Type": "application/x-www-form-urlencoded", Accept: "application/json" },
        body: formOf(service, assets),
      });
      const text = await res.text();
      if (!res.ok) return { ok: false, message: text.trim() };
      return { ok: true, message: (JSON.parse(text) as { message?: string }).message ?? "" };
    } catch {
      return { ok: false, message: "" };
    }
  };

  const handOver = async (service: string, assets: string[]): Promise<void> => {
    toast(word("Handing over..."));
    const done = await post("immich/send", service, assets);
    if (!done.ok) {
      toast(done.message || word("Could not hand over"), openApp());
      return;
    }
    const undo = element("button", { type: "button", textContent: word("Undo") });
    undo.addEventListener("click", async () => {
      undo.disabled = true;
      const back = await post("immich/undo", service, assets);
      toast(back.message || word("Taken off again"), openApp());
    });
    toast(done.message, undo, openApp());
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
    for (const svc of got.services) {
      const item = element("button", { type: "button" }, svc.name);
      item.setAttribute("role", "menuitem");
      const detail = svc.note || word(svc.copy ? "Your original stays in your library" : "Your original goes to your trash once the other side has a copy");
      item.append(element("small", { textContent: detail }));
      item.addEventListener("click", () => {
        const chosen = assets();
        closeMenu();
        if (chosen.length > 0) void handOver(svc.name, chosen);
      });
      menu.append(item);
    }
    menu.append(element("div", { className: "foot" }, openApp()));
    document.body.append(menu);
    const at = anchor.getBoundingClientRect();
    const room = menu.getBoundingClientRect();
    menu.style.left = `${Math.max(12, Math.min(at.right - room.width, window.innerWidth - room.width - 12))}px`;
    menu.style.top = `${Math.min(at.bottom + 6, window.innerHeight - room.height - 12)}px`;
    menu.querySelector<HTMLElement>("button, a")?.focus();
    document.addEventListener("pointerdown", outside, true);
    document.addEventListener("keydown", escape, true);
  };

  // ---- the button ----
  const makeButton = (id: string, assets: () => string[], like: Element | null): HTMLButtonElement => {
    const label = word("Hand over...");
    const button = element("button", { id, type: "button", className: "handoff-act", title: label }, element("img", { src: icon, alt: "" }), element("span", { textContent: label }));
    button.setAttribute("aria-label", label);
    button.setAttribute("aria-haspopup", "menu");
    if (like) button.style.color = getComputedStyle(like).color; // the text of the bar it sits in, light or dark
    button.addEventListener("click", (e) => {
      e.stopPropagation();
      void openMenu(button, assets);
    });
    return button;
  };

  /** The toolbar of the photo viewer, the place in it before the three dots, and a button that looks like its neighbours. */
  const viewerToolbar = (): { bar: Element; before: Element | null; like: Element | null } | null => {
    const bar = document.querySelector('#immich-asset-viewer [data-testid="asset-viewer-navbar-actions"]');
    if (!bar) return null;
    let before: Element | null = bar.querySelector('button[aria-haspopup][id^="context-menu-button"]');
    while (before && before.parentElement !== bar) before = before.parentElement;
    return { bar, before, like: bar.querySelector("button") };
  };

  const place = (): void => {
    const viewer = viewerToolbar();
    const own = document.getElementById("handoff-viewer-act");
    const photo = assetOfPath(location.pathname);
    if (!viewer || !photo || !info) {
      own?.remove();
      if (viewer && photo && !info) void load(); // asks once; the button comes when the answer does
      if (!viewer) closeMenu();
      return;
    }
    const button = own ?? makeButton("handoff-viewer-act", () => [assetOfPath(location.pathname) ?? ""].filter(Boolean), viewer.like);
    if (button.parentElement !== viewer.bar || (viewer.before && button.nextElementSibling !== viewer.before)) viewer.bar.insertBefore(button, viewer.before);
  };

  const schedule = oncePerFrame(place);
  new MutationObserver(schedule).observe(document.body, { childList: true, subtree: true });
  window.addEventListener("popstate", schedule);
  schedule();
}
