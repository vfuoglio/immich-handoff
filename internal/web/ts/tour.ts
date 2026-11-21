// The first-run tour: a small card that points at the real page, one stop at a time, switching to the view where the thing is. It starts by
// itself once for a person (the server says so), and can be taken again from Help, from Ctrl K and from the Get started card. What it says
// comes from the guides' front matter (docs/start/the-page.md) through the page's own JSON; this only shows it. The Get started card's
// "Show me" uses the same pointer.

import { t } from "./i18n.ts";
import { showSegment } from "./segments.ts";
import { showSettingsCat } from "./settingsnav.ts";
import { place, plan, step } from "./tour-core.ts";
import type { Stop, Target } from "./tour-core.ts";

interface TourData {
  kind: "user" | "admin";
  auto: boolean;
  stops: Stop[];
  targets: Record<string, Target>;
}

function read(): TourData | null {
  const el = document.getElementById("tour");
  if (!el?.textContent) return null;
  try {
    return JSON.parse(el.textContent) as TourData;
  } catch {
    return null;
  }
}

const pause = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));
const calm = (): boolean => matchMedia("(prefers-reduced-motion: reduce)").matches;

/** The element a selector names that can be seen (the Send icon and the Send tab share one target: one of them is hidden). */
function visible(selector: string): HTMLElement | null {
  for (const el of document.querySelectorAll<HTMLElement>(selector)) if (el.getClientRects().length > 0) return el;
  return null;
}

/** Shows the view, part and category a target is in, and waits for the change to settle. */
async function reveal(target: Target): Promise<HTMLElement | null> {
  if (target.view && document.documentElement.dataset["view"] !== target.view) {
    document.querySelector<HTMLAnchorElement>(`nav.tabs a[data-view="${target.view}"]`)?.click();
    await pause(calm() ? 30 : 420);
  }
  if (target.seg) showSegment(target.seg);
  if (target.cat) showSettingsCat(target.cat);
  return target.selector ? visible(target.selector) : null;
}

let ring: HTMLElement | null = null;

function ringAt(el: HTMLElement | null): void {
  if (!el) { ring?.remove(); ring = null; return; }
  if (!ring) {
    ring = document.createElement("div");
    ring.className = "tour-ring";
    ring.setAttribute("aria-hidden", "true");
    document.body.appendChild(ring);
  }
  const r = el.getBoundingClientRect();
  const pad = 6;
  ring.style.cssText = `left:${r.left - pad}px;top:${r.top - pad}px;width:${r.width + pad * 2}px;height:${r.height + pad * 2}px`;
}

function markSeen(kind: string): void {
  void fetch("tour/seen", {
    method: "POST",
    credentials: "same-origin",
    headers: { "X-Requested-With": "immich-handoff", "Content-Type": "application/x-www-form-urlencoded" },
    body: `kind=${kind}`,
  }).catch(() => { /* it will be offered again, which does no harm */ });
}

let running = false;

async function start(data: TourData): Promise<void> {
  if (running) return;
  const stops = plan(data.stops, data.targets, (sel) => document.querySelector(sel) !== null);
  if (stops.length === 0) return;
  running = true;
  const before = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  const card = document.createElement("div");
  card.className = "tour-card";
  card.setAttribute("role", "dialog");
  card.setAttribute("aria-modal", "false");
  card.setAttribute("aria-labelledby", "tour-title");
  card.tabIndex = -1;
  const count = document.createElement("p");
  count.className = "tour-count";
  const title = document.createElement("h3");
  title.id = "tour-title";
  const text = document.createElement("p");
  text.className = "tour-text";
  const guide = document.createElement("a");
  guide.className = "tour-guide";
  guide.target = "_blank";
  guide.rel = "noopener";
  guide.textContent = t("Open the guide");
  const actions = document.createElement("p");
  actions.className = "tour-actions";
  const skip = document.createElement("button");
  skip.type = "button";
  skip.className = "tour-skip";
  skip.textContent = t("Skip the tour");
  const back = document.createElement("button");
  back.type = "button";
  back.textContent = t("Back");
  const next = document.createElement("button");
  next.type = "button";
  next.className = "primary";
  actions.append(skip, back, next);
  card.append(count, title, text, guide, actions);
  document.body.appendChild(card);

  let at = 0;
  const end = (): void => {
    running = false;
    removeEventListener("keydown", onKey, true);
    removeEventListener("resize", follow);
    removeEventListener("scroll", follow, true);
    card.remove();
    ringAt(null);
    markSeen(data.kind);
    before?.focus();
  };
  let current: HTMLElement | null = null;
  const follow = (): void => {
    if (!current) return;
    ringAt(current);
    const r = current.getBoundingClientRect();
    const p = place(r, { width: card.offsetWidth, height: card.offsetHeight }, { width: innerWidth, height: innerHeight });
    card.style.left = `${p.left}px`;
    card.style.top = `${p.top}px`;
  };
  const show = async (i: number): Promise<void> => {
    at = i;
    const stop = stops[i];
    if (!stop) return;
    const target = data.targets[stop.target];
    current = target ? await reveal(target) : null;
    if (!current) { // the thing is not there after all: go on in the direction we were going
      const to = step(i, 1, stops.length);
      if (to === null || to === i) return end();
      return show(to);
    }
    current.scrollIntoView({ block: "center", behavior: calm() ? "auto" : "smooth" });
    await pause(calm() ? 0 : 280);
    count.textContent = t("Step {n} of {total}", { n: i + 1, total: stops.length });
    title.textContent = stop.title;
    text.textContent = stop.text;
    guide.hidden = !stop.url;
    if (stop.url) guide.href = stop.url;
    back.hidden = i === 0;
    next.textContent = i === stops.length - 1 ? t("Done") : t("Next");
    follow();
    card.classList.add("on");
    card.focus({ preventScroll: true });
  };
  const go = (by: 1 | -1): void => {
    const to = step(at, by, stops.length);
    if (to === null) end();
    else void show(to);
  };
  const onKey = (e: KeyboardEvent): void => {
    if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); end(); }
    else if (e.key === "ArrowRight" && !(e.target instanceof HTMLInputElement)) go(1);
    else if (e.key === "ArrowLeft" && !(e.target instanceof HTMLInputElement)) go(-1);
  };
  next.addEventListener("click", () => go(1));
  back.addEventListener("click", () => go(-1));
  skip.addEventListener("click", end);
  addEventListener("keydown", onKey, true);
  addEventListener("resize", follow);
  addEventListener("scroll", follow, true);
  await show(0);
}

/** "Show me" on the Get started card: scroll to the thing and ring it until the next click or key. */
async function showMe(data: TourData, key: string): Promise<void> {
  if (key === "tour") { void start(data); return; }
  const target = data.targets[key];
  if (!target) return;
  const el = await reveal(target);
  if (!el) return;
  el.scrollIntoView({ block: "center", behavior: calm() ? "auto" : "smooth" });
  await pause(calm() ? 0 : 300);
  ringAt(el);
  const off = (): void => { ringAt(null); removeEventListener("click", off, true); removeEventListener("keydown", off, true); removeEventListener("scroll", off, true); };
  setTimeout(() => { addEventListener("click", off, true); addEventListener("keydown", off, true); addEventListener("scroll", off, true); }, 400);
  setTimeout(off, 6000);
}

export function initTour(): void {
  const data = read();
  if (!data || data.stops.length === 0) return;
  document.addEventListener("click", (e) => {
    const button = e.target instanceof Element ? e.target.closest<HTMLElement>("[data-show]") : null;
    if (button?.dataset["show"]) void showMe(data, button.dataset["show"]);
  });
  document.addEventListener("tour:start", () => void start(data));
  // replayed from Help or Ctrl K: the address says so
  if (location.hash === "#tour") {
    history.replaceState(null, "", location.pathname + location.search + (data.kind === "user" ? "#overview" : ""));
    setTimeout(() => void start(data), 300);
    return;
  }
  // a browser driven by a test does not get the tour by itself (it would cover what the test clicks); #tour still starts it
  if (data.auto && !navigator.webdriver) {
    setTimeout(() => {
      if (!document.querySelector("dialog[open]")) void start(data);
    }, 900);
  }
}
