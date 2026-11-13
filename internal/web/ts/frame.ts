// A frame: a screen (a photo frame, an old tablet, a TV) shows an album's photos one after another. It holds no sign-in;
// it asks the app for the list of photos now and then and fetches each picture through the app. It has to run on cheap
// hardware that may be slow, may sleep and may lose the network, so it is small, it keeps showing what it has, and it
// asks again with growing pauses when something fails. How one photo gives way to the next, and what is behind them, is
// the engine in stage.ts, which the wall shares.

import { holdFor, initStage, present } from "./stage.ts";

/** The state of the show: which photos were shown in this round, which came in after the page opened, and the last one. */
export interface Show {
  shown: string[];
  fresh: string[];
  last: string;
}

/** What to show next. A photo that arrived after the page opened comes first; then every photo once in a random order
 *  (a round); when all were shown a new round starts, never beginning with the photo that was just shown. */
export function pick(items: string[], show: Show, rnd: () => number): { id: string; show: Show } | null {
  if (items.length === 0) return null;
  const present = new Set(items);
  const fresh = show.fresh.filter((id) => present.has(id));
  const first = fresh[0];
  if (first !== undefined) {
    return { id: first, show: { shown: show.shown.concat(first), fresh: fresh.slice(1), last: first } };
  }
  let shown = show.shown.filter((id) => present.has(id));
  let pool = items.filter((id) => !shown.includes(id));
  if (pool.length === 0) {
    shown = [];
    pool = items.filter((id) => id !== show.last || items.length === 1);
  }
  const id = pool[Math.min(pool.length - 1, Math.floor(rnd() * pool.length))] as string;
  return { id, show: { shown: shown.concat(id), fresh: [], last: id } };
}

/** Which photos of a new list are new: not in the list the page already had. Nothing is new on the first list. */
export function arrivals(before: string[] | null, now: string[]): string[] {
  if (before === null) return [];
  const had = new Set(before);
  return now.filter((id) => !had.has(id));
}

/** How long to wait before asking again after n failures in a row: 5 seconds, doubling, at most 15 minutes. */
export function backoff(failures: number): number {
  return Math.min(15 * 60_000, 5_000 * 2 ** Math.max(0, failures - 1));
}

export function initFrame(): void {
  const root = document.getElementById("frame");
  const stage = root?.querySelector<HTMLElement>(".wall-stage");
  if (!root || !stage) return;
  const feed = root.dataset["feed"] ?? "";
  const photo = root.dataset["photo"] ?? "";
  const hold = Math.max(3, Number(root.dataset["seconds"] ?? "15") || 15) * 1000;
  const wait = root.querySelector<HTMLElement>(".wall-wait");
  initStage(root, stage);

  let items: string[] | null = null;
  let show: Show = { shown: [], fresh: [], last: "" };
  let failures = 0;
  let refreshMs = 120_000;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let showing = false;

  const load = (id: string): Promise<HTMLImageElement | null> =>
    new Promise((resolve) => {
      const img = new Image();
      img.alt = "";
      img.decoding = "async";
      const done = setTimeout(() => resolve(null), 30_000); // a slow device or network: give up on this one
      img.onload = () => { clearTimeout(done); resolve(img); };
      img.onerror = () => { clearTimeout(done); resolve(null); };
      img.src = photo + id;
    });

  // shows the next photo and says whether it was a new arrival (which is then given a little longer)
  const next = async (): Promise<boolean> => {
    if (showing || items === null) return false;
    showing = true;
    try {
      for (let tries = 0; tries < 3; tries++) { // a picture that does not load is skipped
        const arrival = show.fresh.length > 0;
        const chosen = pick(items, show, Math.random);
        if (!chosen) return false;
        show = chosen.show;
        const img = await load(chosen.id);
        if (!img) continue;
        const frame = document.createElement("figure");
        frame.append(img);
        present(stage, frame, holdFor(hold, arrival), arrival);
        return arrival;
      }
      return false;
    } finally {
      showing = false;
    }
  };
  // one photo after another: each stays `hold`, a new arrival longer
  let slide: ReturnType<typeof setTimeout> | undefined;
  const arm = (ms: number): void => {
    clearTimeout(slide);
    slide = setTimeout(() => void next().then((arrival) => arm(holdFor(hold, arrival))), ms);
  };
  const showNow = (): void => void next().then((arrival) => arm(holdFor(hold, arrival)));

  const poll = (): void => {
    void fetch(feed, { cache: "no-store" })
      .then((r) => (r.ok ? (r.json() as Promise<{ items?: { id: string }[]; refresh?: number }>) : Promise.reject(new Error(String(r.status)))))
      .then((data) => {
        const ids = (data.items ?? []).map((i) => i.id);
        const fresh = arrivals(items, ids);
        const first = items === null;
        items = ids;
        show = { ...show, fresh: show.fresh.concat(fresh) };
        failures = 0;
        refreshMs = Math.max(5, data.refresh ?? 120) * 1000;
        if (wait) wait.hidden = ids.length > 0;
        if (first || fresh.length > 0) showNow();
      })
      .catch(() => {
        failures++; // keep showing what is there; ask again later, and less often the longer it fails
      })
      .finally(() => {
        clearTimeout(timer);
        timer = setTimeout(poll, failures > 0 ? backoff(failures) : refreshMs);
      });
  };

  arm(hold);
  root.addEventListener("dblclick", () => {
    if (document.fullscreenElement) void document.exitFullscreen();
    else void root.requestFullscreen?.();
  });
  // keep the screen awake where the browser allows it; it is given up when the page is hidden and taken again when it shows
  const wake = (): void => {
    if (document.visibilityState !== "visible") return;
    const nav = navigator as Navigator & { wakeLock?: { request(kind: "screen"): Promise<unknown> } };
    void nav.wakeLock?.request("screen").catch(() => undefined);
  };
  document.addEventListener("visibilitychange", wake);
  wake();
  poll();
}
