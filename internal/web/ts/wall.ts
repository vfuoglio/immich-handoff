// The wall of a drop box: a screen shows the box's photos, one after another or as a grid or a collage, and picks up new
// ones a few seconds after they arrive. It asks the app for what is new now and then (it holds no sign-in, so it cannot
// listen to the stream the other pages use) and fetches each picture through the app. Nothing here needs more than a
// browser on a TV.

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
  const controls = root.querySelector<HTMLElement>(".wall-controls");
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
  };

  const showSingle = (): void => {
    if (mode !== "single") return;
    const id = nextToShow(items, shown, fresh);
    fresh = fresh.filter((f) => f !== id);
    if (id < 0) return;
    shown = id;
    const frame = document.createElement("figure");
    frame.className = "single";
    frame.append(img(id));
    const text = caption(id);
    if (text) {
      const c = document.createElement("figcaption");
      c.textContent = text;
      frame.append(c);
    }
    stage.append(frame);
    requestAnimationFrame(() => frame.classList.add("in"));
    Array.from(stage.querySelectorAll("figure.single")).slice(0, -1).forEach((old) => {
      old.classList.remove("in");
      setTimeout(() => old.remove(), 1500);
    });
  };

  let noteIndex = 0;
  const showMessage = (): void => {
    if (!note || messages.length === 0) return;
    const m = messages[noteIndex++ % messages.length];
    if (!m) return;
    note.textContent = m.name ? `${m.text} — ${m.name}` : m.text;
    note.hidden = false;
    setTimeout(() => (note.hidden = true), 8000);
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
        if (before === 0 && items.length > 0) showSingle();
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
      showSingle();
    }),
  );
  root.querySelector(".wall-full")?.addEventListener("click", () => {
    if (document.fullscreenElement) void document.exitFullscreen();
    else void root.requestFullscreen?.();
  });
  // the controls are there for the person who set the screen up, and fade when nobody moves the mouse
  let hide: ReturnType<typeof setTimeout> | undefined;
  const wake = (): void => {
    controls?.classList.add("on");
    clearTimeout(hide);
    hide = setTimeout(() => controls?.classList.remove("on"), 3000);
  };
  document.addEventListener("mousemove", wake);
  document.addEventListener("touchstart", wake);
  wake();

  draw();
  poll();
  setInterval(poll, POLL_MS);
  setInterval(showSingle, SHOW_MS);
  setInterval(showMessage, SHOW_MS * 3);
}
