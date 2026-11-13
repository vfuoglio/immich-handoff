// The wall of a drop box: a screen shows the box's photos, one after another or as a grid or a collage, and picks up new
// ones a few seconds after they arrive. It asks the app for what is new now and then (it holds no sign-in, so it cannot
// listen to the stream the other pages use) and fetches each picture through the app. Nothing here needs more than a
// browser on a TV. How one photo gives way to the next, and what is behind them, is the engine in stage.ts, which the
// frame shares.

import { flip, holdFor, initStage, present } from "./stage.ts";

export interface WallItem {
  id: number;
  caption?: string;
}

export interface WallMessage {
  name?: string;
  text: string;
}

/** Adds what is new to what is shown, once each, oldest first, and keeps at most `max` of the newest. */
export function mergeItems(prev: WallItem[], incoming: WallItem[], max: number): WallItem[] {
  const seen = new Set(prev.map((i) => i.id));
  const merged = prev.concat(incoming.filter((i) => !seen.has(i.id) && seen.add(i.id)));
  merged.sort((a, b) => a.id - b.id);
  return merged.slice(Math.max(0, merged.length - max));
}

/** The position to ask for next: the newest photo the wall has seen. */
export function cursorOf(items: WallItem[], current: number): number {
  return items.reduce((c, i) => Math.max(c, i.id), current);
}

/** The newest n photos, newest first: what a grid or a collage shows. */
export function latest(items: WallItem[], n: number): WallItem[] {
  return items.slice(Math.max(0, items.length - n)).reverse();
}

/** A tilt for a photo in the collage, from its id, so it is the same every time it is drawn: -6 to 6 degrees. */
export function tiltOf(id: number): number {
  const x = Math.sin(id * 12.9898) * 43758.5453;
  return Math.round((x - Math.floor(x) - 0.5) * 12 * 10) / 10;
}

/** Which photo comes next in the single-photo show: a new arrival first, then round the others in order. */
export function nextToShow(items: WallItem[], shown: number, fresh: number[]): number {
  const waiting = fresh.find((id) => items.some((i) => i.id === id));
  if (waiting !== undefined) return waiting;
  if (items.length === 0) return -1;
  const at = items.findIndex((i) => i.id === shown);
  return items[(at + 1) % items.length]?.id ?? -1;
}

export function modeOf(value: string | undefined): "single" | "grid" | "collage" {
  return value === "grid" || value === "collage" ? value : "single";
}

const POLL_MS = 4000;
const SHOW_MS = 7000;
const KEEP = 200;

export function initWall(): void {
  const root = document.getElementById("wall");
  const stage = root?.querySelector<HTMLElement>(".wall-stage");
  if (!root || !stage) return;
  const feed = root.dataset.feed ?? "";
  const photo = root.dataset.photo ?? "";
  const wait = root.querySelector<HTMLElement>(".wall-wait");
  const note = root.querySelector<HTMLElement>(".wall-note");
  initStage(root, stage);
  let items: WallItem[] = [];
  let messages: WallMessage[] = [];
  let cursor = 0;
  let shown = -1;
  let fresh: number[] = [];
  let mode = modeOf(root.dataset.mode);

  const img = (id: number): HTMLImageElement => {
    const el = document.createElement("img");
    el.src = photo + id;
    el.alt = "";
    el.decoding = "async";
    return el;
  };
  const caption = (id: number): string => items.find((i) => i.id === id)?.caption ?? "";

  const draw = (): void => {
    stage.className = "wall-stage " + mode;
    if (wait) wait.hidden = items.length > 0;
    if (mode === "single") return; // the single-photo show draws itself, one photo at a time
    const tiles = latest(items, mode === "grid" ? 12 : 9);
    const have = new Map(Array.from(stage.children).map((c) => [(c as HTMLElement).dataset.id ?? "", c]));
    const wanted = new Set(tiles.map((t) => String(t.id)));
    // the tiles that stay glide to their new places while the new one springs in
    flip(stage, () => {
      for (const [id, el] of have) if (!wanted.has(id)) el.remove();
      tiles.forEach((tile, i) => {
        let el = have.get(String(tile.id)) as HTMLElement | undefined;
        if (!el) {
          el = document.createElement("figure");
          el.dataset.id = String(tile.id);
          el.append(img(tile.id));
          if (tile.caption) {
            const c = document.createElement("figcaption");
            c.textContent = tile.caption;
            el.append(c);
          }
          el.classList.add("arrive");
          stage.append(el);
        }
        el.style.order = String(i);
        if (mode === "collage") el.style.setProperty("--tilt", tiltOf(tile.id) + "deg");
      });
    });
  };

  // shows the next photo and says whether it was a new arrival (which is then given a little longer)
  const showSingle = (): boolean => {
    if (mode !== "single") return false;
    const id = nextToShow(items, shown, fresh);
    const arrival = fresh.includes(id);
    fresh = fresh.filter((f) => f !== id);
    if (id < 0) return false;
    shown = id;
    const frame = document.createElement("figure");
    frame.append(img(id));
    const text = caption(id);
    if (text) {
      const c = document.createElement("figcaption");
      c.textContent = text;
      frame.append(c);
    }
    present(stage, frame, holdFor(SHOW_MS, arrival), arrival);
    return arrival;
  };
  // one photo after another: each stays SHOW_MS, a new arrival longer; what arrives is shown as soon as it is seen
  let slide: ReturnType<typeof setTimeout> | undefined;
  const arm = (ms: number): void => {
    clearTimeout(slide);
    slide = setTimeout(() => arm(holdFor(SHOW_MS, showSingle())), ms);
  };
  const showNow = (): void => {
    if (mode === "single") arm(holdFor(SHOW_MS, showSingle()));
  };

  let noteIndex = 0;
  const showMessage = (): void => {
    if (!note || messages.length === 0) return;
    const m = messages[noteIndex++ % messages.length];
    if (!m) return;
    note.textContent = m.name ? `${m.text} — ${m.name}` : m.text;
    note.hidden = false;
    void note.offsetWidth;
    note.classList.add("in"); // it rises in, stays, and fades out
    setTimeout(() => {
      note.classList.remove("in");
      setTimeout(() => (note.hidden = true), 1000);
    }, 8000);
  };

  const poll = (): void => {
    void fetch(`${feed}?after=${cursor}`, { cache: "no-store" })
      .then((r) => (r.ok ? (r.json() as Promise<{ items?: WallItem[]; messages?: WallMessage[] }>) : Promise.reject(new Error(String(r.status)))))
      .then((data) => {
        const incoming = data.items ?? [];
        const before = items.length;
        items = mergeItems(items, incoming, KEEP);
        cursor = cursorOf(items, cursor);
        if (before > 0) fresh.push(...incoming.map((i) => i.id)); // what arrives after the first look is shown at once
        messages = data.messages ?? [];
        draw();
        if (before === 0 && items.length > 0) showNow();
        else if (before > 0 && incoming.length > 0 && mode === "single") showNow(); // a new arrival is shown at once
      })
      .catch(() => {
        /* the screen keeps showing what it has and asks again */
      });
  };

  root.querySelectorAll<HTMLButtonElement>("button[data-mode]").forEach((b) =>
    b.addEventListener("click", () => {
      mode = modeOf(b.dataset.mode);
      root.querySelectorAll("button[data-mode]").forEach((o) => o.setAttribute("aria-pressed", String(o === b)));
      stage.replaceChildren();
      shown = -1;
      draw();
      clearTimeout(slide);
      showNow();
    }),
  );

  draw();
  poll();
  setInterval(poll, POLL_MS);
  setInterval(showMessage, SHOW_MS * 3);
}
