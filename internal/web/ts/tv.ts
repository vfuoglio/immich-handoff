// A TV page: it asks the app for a short code and shows it; the person types the code on their own page and chooses what to show;
// then this page shows those photos one after another, and does what the person tells it from their phone (pause, next, previous,
// something else). It holds no sign-in: its only secret is a long id the app gave it, and it asks the app for the state every
// few seconds. Older TVs use tv-legacy.ts instead.

import { arrivals, backoff, pick, type Show } from "./frame.ts";
import { phoneAddress } from "./tv-core.ts";
import { fxOf, holdFor, initStage, present } from "./stage.ts";

interface State {
  state: "waiting" | "expired" | "live" | "ended";
  paused?: boolean;
  seconds?: number;
  fx?: string;
  seq?: number;
  cmd?: string;
  refresh?: number;
}

function initTV(): void {
  const root = document.getElementById("tv");
  const stage = root?.querySelector<HTMLElement>(".wall-stage");
  const codeEl = root?.querySelector<HTMLElement>(".tv-code");
  const hintEl = root?.querySelector<HTMLElement>(".tv-hint");
  const codeBox = root?.querySelector<HTMLElement>(".tv-pair");
  if (!root || !stage || !codeEl || !hintEl || !codeBox) return;
  (window as unknown as { __tvStarted: boolean }).__tvStarted = true;
  initStage(root, stage);
  const hint = (hintEl.dataset["hint"] ?? "").replace("{address}", phoneAddress(location));
  hintEl.textContent = hint;

  let secret = "";
  let items: string[] | null = null;
  let show: Show = { shown: [], fresh: [], last: "" };
  let history: string[] = [];
  let seq = 0;
  let paused = false;
  let holdMs = 15_000;
  let failures = 0;
  let slide: ReturnType<typeof setTimeout> | undefined;
  let feedTimer: ReturnType<typeof setTimeout> | undefined;
  let showing = false;
  let started = false;

  const load = (id: string): Promise<HTMLImageElement | null> =>
    new Promise((resolve) => {
      const img = new Image();
      img.alt = "";
      img.decoding = "async";
      const done = setTimeout(() => resolve(null), 30_000);
      img.onload = () => { clearTimeout(done); resolve(img); };
      img.onerror = () => { clearTimeout(done); resolve(null); };
      img.src = `tv/photo?s=${secret}&id=${id}`;
    });

  const put = async (id: string): Promise<boolean> => {
    const img = await load(id);
    if (!img) return false;
    const fig = document.createElement("figure");
    fig.append(img);
    present(stage, fig, holdFor(holdMs, false), false);
    return true;
  };
  const arm = (): void => {
    clearTimeout(slide);
    if (paused) return;
    slide = setTimeout(() => void step("next"), holdMs);
  };
  const step = async (way: "next" | "prev"): Promise<void> => {
    if (showing || items === null) return;
    showing = true;
    try {
      for (let tries = 0; tries < 3; tries++) {
        let id: string | undefined;
        if (way === "prev" && history.length > 1) {
          history.pop();
          id = history[history.length - 1];
        } else {
          const chosen = pick(items, show, Math.random);
          if (!chosen) return;
          show = chosen.show;
          id = chosen.id;
          history.push(id);
          if (history.length > 50) history = history.slice(-50);
        }
        if (id && (await put(id))) return;
      }
    } finally {
      showing = false;
      arm();
    }
  };

  const feed = (reset: boolean): void => {
    clearTimeout(feedTimer);
    void fetch(`tv/feed?s=${secret}`, { cache: "no-store" })
      .then((r) => (r.ok ? (r.json() as Promise<{ items?: { id: string }[]; refresh?: number }>) : Promise.reject(new Error(String(r.status)))))
      .then((data) => {
        const ids = (data.items ?? []).map((i) => i.id);
        const first = items === null || reset;
        const fresh = reset ? [] : arrivals(items, ids);
        items = ids;
        if (reset) { show = { shown: [], fresh: [], last: "" }; history = []; }
        show = { ...show, fresh: show.fresh.concat(fresh) };
        failures = 0;
        if (first) void step("next");
        feedTimer = setTimeout(() => feed(false), Math.max(5, data.refresh ?? 120) * 1000);
      })
      .catch(() => {
        failures++;
        feedTimer = setTimeout(() => feed(false), backoff(failures));
      });
  };

  const begin = (): void => {
    items = null;
    show = { shown: [], fresh: [], last: "" };
    history = [];
    seq = 0;
    started = false;
    clearTimeout(slide);
    clearTimeout(feedTimer);
    stage.textContent = "";
    codeBox.hidden = false;
    void fetch("tv/new", { method: "POST", cache: "no-store" })
      .then((r) => (r.ok ? (r.json() as Promise<{ s: string; code: string }>) : Promise.reject(new Error(String(r.status)))))
      .then((d) => {
        secret = d.s;
        codeEl.textContent = d.code.toUpperCase();
        setTimeout(poll, 2000);
      })
      .catch(() => setTimeout(begin, 30_000)); // the app is not there or this address asked too often: try again in a while
  };

  const poll = (): void => {
    void fetch(`tv/state?s=${secret}`, { cache: "no-store" })
      .then((r) => (r.ok ? (r.json() as Promise<State>) : Promise.reject(new Error(String(r.status)))))
      .then((st) => {
        if (st.state === "expired" || st.state === "ended") return begin();
        if (st.state === "live") {
          codeBox.hidden = true;
          holdMs = Math.max(5, st.seconds ?? 15) * 1000;
          stage.dataset["fx"] = fxOf(st.fx);
          const wasPaused = paused;
          paused = st.paused === true;
          if (!started) {
            started = true;
            seq = st.seq ?? 0;
            feed(true);
          } else if ((st.seq ?? 0) !== seq) {
            seq = st.seq ?? 0;
            if (st.cmd === "next") void step("next");
            else if (st.cmd === "prev") void step("prev");
            else if (st.cmd === "source") feed(true);
          }
          if (wasPaused !== paused) arm();
        }
        setTimeout(poll, 3000);
      })
      .catch(() => setTimeout(poll, 8000));
  };

  begin();
}

initTV();
