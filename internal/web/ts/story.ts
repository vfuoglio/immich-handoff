// A story's page: it asks the app for the story (the title, the chapters, the photos by opaque ids), then plays it scene by scene: a
// title, a card for each chapter, the photos with their dates and places, and an end. A tap goes on, a tap on the left goes back, a swipe
// does the same, holding the screen pauses, and the arrow keys and the space bar work too. There is no sound. It holds no sign-in; the
// photos come through the app. The way one photo gives way to the next is stage.ts, which frames and walls share.

import { n, t } from "./i18n";
import { after, before, buildTimeline, progressOf, swipeOf, tapOf, type Doc, type Scene } from "./story-core.ts";
import { fxOf, initStage, present } from "./stage.ts";

export function initStory(): void {
  const root = document.getElementById("story");
  const stage = root?.querySelector<HTMLElement>(".wall-stage");
  const bar = root?.querySelector<HTMLElement>(".st-bar");
  const wait = root?.querySelector<HTMLElement>(".wall-wait");
  const pauseBadge = root?.querySelector<HTMLElement>(".st-pause");
  if (!root || !stage || !bar) return;
  const docUrl = root.dataset["doc"] ?? "";
  const photo = root.dataset["photo"] ?? "";
  initStage(root, stage);
  stage.dataset["fx"] = fxOf("zoom");
  const lang = document.documentElement.lang || undefined;
  const fmt = (day: string): string => {
    const d = new Date(day + "T12:00:00");
    return Number.isNaN(d.getTime()) ? day : d.toLocaleDateString(lang, { day: "numeric", month: "long", year: "numeric" });
  };

  let scenes: Scene[] = [];
  let i = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let startedAt = 0;
  let spent = 0; // how much of this scene has passed when the story is paused
  let paused = false;
  let ticker: ReturnType<typeof setInterval> | undefined;

  const text = (cls: string, words: string): HTMLElement => {
    const el = document.createElement("p");
    el.className = cls;
    el.textContent = words;
    return el;
  };
  const card = (...children: HTMLElement[]): HTMLElement => {
    const fig = document.createElement("figure");
    fig.classList.add("st-card");
    const box = document.createElement("div");
    box.className = "st-cardbox";
    box.append(...children.filter((c) => c.textContent !== ""));
    fig.append(box);
    return fig;
  };
  const load = (id: string): Promise<HTMLImageElement | null> =>
    new Promise((resolve) => {
      const img = new Image();
      img.alt = "";
      img.decoding = "async";
      const done = setTimeout(() => resolve(null), 30_000);
      img.onload = () => { clearTimeout(done); resolve(img); };
      img.onerror = () => { clearTimeout(done); resolve(null); };
      img.src = photo + id;
    });

  const frameOf = async (s: Scene): Promise<HTMLElement | null> => {
    switch (s.kind) {
      case "title":
        return card(text("st-title", s.title), text("st-sub", s.range));
      case "chapter":
        return card(text("st-chapter", s.title !== "" ? s.title : s.range !== "" ? s.range : t("Part {n}", { n: s.index + 1 })), text("st-sub", s.title !== "" ? s.range : ""));
      case "end":
        return card(text("st-title", t("The end")), text("st-sub", n("{n} photo", "{n} photos", s.count)));
      case "photo": {
        const img = await load(s.id);
        if (!img) return null;
        const fig = document.createElement("figure");
        const back = new Image(); // a soft copy of the photo fills the screen behind it, so a tall photo on a wide screen is not black
        back.alt = "";
        back.src = img.src;
        back.className = "st-back";
        fig.append(back, img);
        if (s.caption !== "") {
          const cap = document.createElement("figcaption");
          cap.textContent = s.caption;
          fig.append(cap);
        }
        return fig;
      }
    }
  };

  const paint = (): void => {
    const inside = paused ? spent / (scenes[i]?.ms ?? 1) : Math.min(1, (Date.now() - startedAt + spent) / (scenes[i]?.ms ?? 1));
    bar.style.transform = `scaleX(${progressOf(i, scenes.length, inside).toFixed(4)})`;
  };

  const arm = (): void => {
    clearTimeout(timer);
    clearInterval(ticker);
    if (paused) { paint(); return; }
    const s = scenes[i];
    if (!s) return;
    startedAt = Date.now();
    ticker = setInterval(paint, 250);
    // the end waits for a tap; every other scene goes on by itself
    if (s.kind !== "end") timer = setTimeout(() => go(after(i, scenes.length)), Math.max(300, s.ms - spent));
    paint();
  };

  let busy = false;
  const go = async (to: number): Promise<void> => {
    if (busy || scenes.length === 0) return;
    busy = true;
    try {
      let target = to;
      for (let tries = 0; tries < 3; tries++) {
        const s = scenes[target];
        if (!s) return;
        const fig = await frameOf(s);
        if (!fig) { target = target >= i ? after(target, scenes.length) : before(target); continue; } // a photo that does not load is skipped
        i = target;
        spent = 0;
        present(stage, fig, s.ms, false);
        // the next photo is fetched ahead, so it is there when its turn comes
        const upcoming = scenes[i + 1];
        if (upcoming?.kind === "photo") void load(upcoming.id);
        arm();
        return;
      }
    } finally {
      busy = false;
    }
  };

  const pause = (on: boolean): void => {
    if (on === paused) return;
    if (on) spent += Date.now() - startedAt;
    paused = on;
    if (pauseBadge) pauseBadge.hidden = !on;
    arm();
  };

  // taps, holds and swipes
  let downAt = 0;
  let downX = 0;
  let downY = 0;
  let holding: ReturnType<typeof setTimeout> | undefined;
  let held = false;
  root.addEventListener("pointerdown", (e) => {
    downAt = Date.now();
    downX = e.clientX;
    downY = e.clientY;
    held = false;
    clearTimeout(holding);
    holding = setTimeout(() => { held = true; pause(true); }, 350);
  });
  const release = (e: PointerEvent, cancelled: boolean): void => {
    clearTimeout(holding);
    if (held) { held = false; pause(false); return; }
    if (cancelled || Date.now() - downAt > 600) return;
    const swipe = swipeOf(e.clientX - downX, e.clientY - downY);
    if (swipe === "next") void go(after(i, scenes.length));
    else if (swipe === "prev") void go(before(i));
    else if (Math.abs(e.clientX - downX) < 10) void go(tapOf(e.clientX, root.clientWidth) === "prev" ? before(i) : after(i, scenes.length));
  };
  root.addEventListener("pointerup", (e) => release(e, false));
  root.addEventListener("pointercancel", (e) => release(e, true));
  document.addEventListener("keydown", (e) => {
    if (e.key === "ArrowRight") void go(after(i, scenes.length));
    else if (e.key === "ArrowLeft") void go(before(i));
    else if (e.key === " ") { e.preventDefault(); pause(!paused); }
  });
  document.addEventListener("visibilitychange", () => { if (document.visibilityState !== "visible") pause(true); });
  root.addEventListener("dblclick", () => {
    if (document.fullscreenElement) void document.exitFullscreen();
    else void root.requestFullscreen?.();
  });

  void fetch(docUrl, { cache: "no-store" })
    .then((r) => (r.ok ? (r.json() as Promise<Doc>) : Promise.reject(new Error(String(r.status)))))
    .then((doc) => {
      scenes = buildTimeline(doc, fmt);
      if (wait) wait.hidden = true;
      void go(0);
    })
    .catch(() => {
      if (wait) wait.textContent = t("This story is not available any more.");
    });
}
