// Help: the "?" beside a card opens the guide in a drawer (a bottom sheet on a phone), the Help page has a search box, and a
// few words on a button show as a tip. The guides are rendered by the server from the same Markdown as the public site; this
// only shows them. Without the script the "?" is a plain link that opens the guide in a new tab.

import { t } from "./i18n.ts";

/** The feature a link to a guide points at ("help/stories#a-heading" is "stories"), or null for any other link. */
export function featureOf(href: string): string | null {
  const m = /(?:^|\/)help\/([a-z][a-z0-9-]*)(?:[#?].*)?$/.exec(href);
  if (!m || m[1] === "search" || m[1] === "img") return null;
  return m[1] ?? null;
}

/** Turns each group of tabs (made from the ":::tabs" fence) into tabs; without it every panel stays under its label. */
export function initTabs(root: ParentNode): void {
  root.querySelectorAll<HTMLElement>(".doc-tabs:not(.ready)").forEach((box) => {
    const panels = Array.from(box.querySelectorAll<HTMLElement>(":scope > .doc-tab"));
    if (panels.length === 0) return;
    const bar = document.createElement("div");
    bar.className = "doc-tabbar";
    bar.setAttribute("role", "tablist");
    const show = (i: number): void => {
      panels.forEach((p, j) => { p.hidden = j !== i; });
      Array.from(bar.children).forEach((b, j) => b.setAttribute("aria-selected", String(j === i)));
    };
    panels.forEach((p, i) => {
      const b = document.createElement("button");
      b.type = "button";
      b.setAttribute("role", "tab");
      b.textContent = p.dataset["label"] ?? "";
      b.addEventListener("click", () => show(i));
      bar.appendChild(b);
    });
    box.insertBefore(bar, box.firstChild);
    box.classList.add("ready");
    show(0);
  });
}

function initDrawer(): void {
  const dialog = document.getElementById("help-drawer");
  const content = document.getElementById("help-content");
  const back = document.getElementById("help-back");
  if (!(dialog instanceof HTMLDialogElement) || !content || !(back instanceof HTMLButtonElement)) return;
  const trail: string[] = [];

  const load = async (feature: string, push: boolean): Promise<void> => {
    content.setAttribute("aria-busy", "true");
    try {
      const res = await fetch(`fragments/help/${feature}`, { credentials: "same-origin" });
      if (!res.ok) throw new Error(String(res.status));
      content.innerHTML = await res.text(); // the server's own, already escaped, rendering of the page's Markdown
      if (push) trail.push(feature);
      initTabs(content);
      content.scrollTop = 0;
    } catch {
      content.textContent = t("This guide could not be loaded.");
    } finally {
      content.removeAttribute("aria-busy");
      back.hidden = trail.length < 2;
    }
  };

  document.addEventListener("click", (e) => {
    const link = e.target instanceof Element ? e.target.closest<HTMLAnchorElement>("a[data-help]") : null;
    if (!link || e.ctrlKey || e.metaKey || e.shiftKey || e.button !== 0) return; // a click that wants a tab of its own gets one
    const feature = link.dataset["help"];
    if (!feature) return;
    e.preventDefault();
    trail.length = 0;
    void load(feature, true);
    if (!dialog.open) dialog.showModal();
  });
  // inside the drawer: a link to another guide loads it here, an anchor scrolls the drawer (it must not change the page's own address)
  content.addEventListener("click", (e) => {
    const link = e.target instanceof Element ? e.target.closest<HTMLAnchorElement>("a[href]") : null;
    if (!link || link.classList.contains("leaves") || link.classList.contains("help-full")) return;
    const href = link.getAttribute("href") ?? "";
    if (href.startsWith("#")) {
      e.preventDefault();
      content.querySelector(`[id="${href.slice(1)}"]`)?.scrollIntoView({ block: "start" });
      return;
    }
    const feature = featureOf(href);
    if (feature) { e.preventDefault(); void load(feature, true); }
  });
  back.addEventListener("click", () => {
    trail.pop();
    const previous = trail.pop();
    if (previous) void load(previous, true);
  });
  document.getElementById("help-close")?.addEventListener("click", () => dialog.close());
  dialog.addEventListener("click", (e) => { if (e.target === dialog) dialog.close(); });
  dialog.addEventListener("close", () => { content.replaceChildren(); trail.length = 0; });
}

function initSearch(): void {
  const input = document.getElementById("help-q");
  const list = document.getElementById("help-hits");
  if (!(input instanceof HTMLInputElement) || !list) return;
  let timer = 0;
  let latest = 0;
  const run = async (): Promise<void> => {
    const q = input.value.trim();
    const mine = ++latest;
    if (q.length < 2) { list.replaceChildren(); return; }
    const res = await fetch(`help/search?q=${encodeURIComponent(q)}`, { credentials: "same-origin" });
    if (!res.ok || mine !== latest) return;
    const hits = (await res.json()) as { feature: string; title: string; tip: string; url: string }[];
    list.replaceChildren();
    for (const h of hits) {
      const li = document.createElement("li");
      const a = document.createElement("a");
      a.href = h.url;
      a.textContent = h.title;
      const small = document.createElement("small");
      small.textContent = h.tip;
      li.append(a, small);
      list.appendChild(li);
    }
    if (hits.length === 0) {
      const none = document.createElement("li");
      none.textContent = t("Nothing matches");
      list.appendChild(none);
    }
  };
  input.addEventListener("input", () => { clearTimeout(timer); timer = window.setTimeout(() => void run(), 120); });
  input.closest("form")?.addEventListener("submit", (e) => { e.preventDefault(); void run(); });
}

/** A tip: the words of data-tip, shown on hover, on keyboard focus and on a tap, and hidden on leaving, on Escape and on a tap elsewhere. */
function initTips(): void {
  let bubble: HTMLElement | null = null;
  let owner: Element | null = null;
  const hide = (): void => {
    bubble?.remove();
    bubble = null;
    owner?.removeAttribute("aria-describedby");
    owner = null;
  };
  const show = (el: Element): void => {
    const text = el.getAttribute("data-tip");
    if (!text) return;
    hide();
    bubble = document.createElement("div");
    bubble.id = "tip";
    bubble.className = "tip";
    bubble.setAttribute("role", "tooltip");
    bubble.textContent = text;
    document.body.appendChild(bubble);
    owner = el;
    el.setAttribute("aria-describedby", "tip");
    const r = el.getBoundingClientRect();
    const w = bubble.offsetWidth;
    const left = Math.min(Math.max(8, r.left + r.width / 2 - w / 2), innerWidth - w - 8);
    const above = r.top - bubble.offsetHeight - 8 > 8;
    bubble.style.left = `${left + scrollX}px`;
    bubble.style.top = `${(above ? r.top - bubble.offsetHeight - 8 : r.bottom + 8) + scrollY}px`;
  };
  const target = (e: Event): Element | null => (e.target instanceof Element ? e.target.closest("[data-tip]") : null);
  document.addEventListener("mouseover", (e) => { const el = target(e); if (el && el !== owner) show(el); });
  document.addEventListener("mouseout", (e) => { if (target(e) === owner && !(e.relatedTarget instanceof Node && owner?.contains(e.relatedTarget))) hide(); });
  document.addEventListener("focusin", (e) => { const el = target(e); if (el) show(el); });
  document.addEventListener("focusout", hide);
  document.addEventListener("click", (e) => { const el = target(e); if (el && matchMedia("(hover: none)").matches) show(el); else if (!el) hide(); });
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") hide(); });
  addEventListener("scroll", hide, { passive: true });
}

export function initHelp(): void {
  initDrawer();
  initSearch();
  initTips();
  initTabs(document);
}
