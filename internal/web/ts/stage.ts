// What the wall and the frame have in common: a screen that shows photos, one after another, with a choice of how one
// gives way to the next (fade, zoom, slide, rise) and of what is behind the photos (a soft glow or plain black), a new
// arrival that is given a moment, controls that fade away, and a kinder mode for old hardware. The choice is made with the
// buttons that appear when the pointer moves, or in the address (?fx=zoom&bg=black), and a device remembers it.
//
// Everything that moves is moved with `transform` and `opacity` alone, which the graphics card does without redrawing,
// nothing moves faster than about a second, nothing flashes, and reduced motion turns it all off (see app.css).

export type Fx = "fade" | "zoom" | "slide" | "rise";
export type Bg = "glow" | "black";

/** The transition between two photos; anything unknown is the gentle fade. */
export function fxOf(value: string | null | undefined): Fx {
  return value === "zoom" || value === "slide" || value === "rise" ? value : "fade";
}

/** What is behind the photos; anything unknown is the glow. */
export function bgOf(value: string | null | undefined): Bg {
  return value === "black" ? "black" : "glow";
}

/** How long a photo stays: a new arrival is given a little longer, so it is seen. */
export function holdFor(baseMs: number, fresh: boolean): number {
  return fresh ? Math.round(baseMs * 1.6) : baseMs;
}

/** Whether to go easy on the hardware: few processor cores, little memory, or a request to save data. */
export function isLite(env: { cores?: number | undefined; memory?: number | undefined; saveData?: boolean | undefined }): boolean {
  if (env.saveData) return true;
  if (env.cores !== undefined && env.cores > 0 && env.cores <= 2) return true;
  return env.memory !== undefined && env.memory > 0 && env.memory <= 2;
}

const KEY = "handoff-wall-";

/** The choice for one setting: the address wins, then what this device chose before, then the default. */
function choice(name: "fx" | "bg"): string {
  const fromAddress = new URLSearchParams(location.search).get(name);
  if (fromAddress) return fromAddress;
  try {
    return localStorage.getItem(KEY + name) ?? "";
  } catch {
    return ""; // storage can be blocked: the default then
  }
}

function remember(name: "fx" | "bg", value: string): void {
  try {
    localStorage.setItem(KEY + name, value);
  } catch {
    /* the choice then lasts for this visit only */
  }
}

/**
 * Sets a screen up: the transition and the backdrop it starts with, the kinder mode where the hardware asks for it, the
 * buttons for the choices and for full screen, and controls that fade when nobody moves the pointer.
 */
export function initStage(root: HTMLElement, stage: HTMLElement): void {
  const nav = navigator as Navigator & { deviceMemory?: number; connection?: { saveData?: boolean } };
  if (isLite({ cores: nav.hardwareConcurrency, memory: nav.deviceMemory, saveData: nav.connection?.saveData })) root.dataset["lite"] = "1";

  let fx = fxOf(choice("fx"));
  let bg = bgOf(choice("bg"));
  const paint = (): void => {
    stage.dataset["fx"] = fx;
    root.dataset["bg"] = bg;
    root.querySelectorAll<HTMLButtonElement>("button[data-fx]").forEach((b) => b.setAttribute("aria-pressed", String(b.dataset["fx"] === fx)));
    root.querySelectorAll<HTMLButtonElement>("button[data-bg]").forEach((b) => b.setAttribute("aria-pressed", String(b.dataset["bg"] === bg)));
  };
  paint();
  root.querySelectorAll<HTMLButtonElement>("button[data-fx]").forEach((b) =>
    b.addEventListener("click", () => {
      fx = fxOf(b.dataset["fx"]);
      remember("fx", fx);
      paint();
    }),
  );
  root.querySelectorAll<HTMLButtonElement>("button[data-bg]").forEach((b) =>
    b.addEventListener("click", () => {
      bg = bgOf(b.dataset["bg"]);
      remember("bg", bg);
      paint();
    }),
  );
  root.querySelector(".wall-full")?.addEventListener("click", () => {
    if (document.fullscreenElement) void document.exitFullscreen();
    else void root.requestFullscreen?.();
  });

  // the controls are there for the person who set the screen up, and fade when nobody moves the pointer
  const controls = root.querySelector<HTMLElement>(".wall-controls");
  let hide: ReturnType<typeof setTimeout> | undefined;
  const wake = (): void => {
    controls?.classList.add("on");
    clearTimeout(hide);
    hide = setTimeout(() => controls?.classList.remove("on"), 3000);
  };
  document.addEventListener("mousemove", wake);
  document.addEventListener("touchstart", wake);
  wake();
}

/**
 * Puts the next photo on the screen and sends the one before it away, in the chosen transition (the style sheet does the
 * moving, keyed on the stage's data-fx). A new arrival gets a soft ring, and the zoom drifts for as long as the photo
 * stays, so how long that is is passed on as a CSS variable.
 */
export function present(stage: HTMLElement, frame: HTMLElement, holdMs: number, fresh: boolean): void {
  frame.classList.add("single");
  if (fresh) frame.classList.add("fresh");
  const turn = Number(stage.dataset["turn"] ?? "0") + 1;
  stage.dataset["turn"] = String(turn);
  if (turn % 2 === 0) frame.classList.add("alt"); // the zoom drifts one way, then the other
  frame.style.setProperty("--hold", `${(holdMs + 3000) / 1000}s`);
  stage.append(frame);
  void frame.offsetWidth; // the photo is laid out in its starting place before it is let in
  frame.classList.add("in");
  Array.from(stage.querySelectorAll("figure.single")).slice(0, -1).forEach((old) => {
    old.classList.remove("in");
    old.classList.add("out");
    setTimeout(() => old.remove(), 1700);
  });
}

/** Runs a change to the tiles of a grid or a collage and glides each tile that stayed from where it was to where it is. */
export function flip(container: HTMLElement, change: () => void): void {
  const before = new Map<Element, DOMRect>();
  for (const child of Array.from(container.children)) before.set(child, child.getBoundingClientRect());
  change();
  if (matchMedia("(prefers-reduced-motion: reduce)").matches) return;
  for (const child of Array.from(container.children)) {
    const was = before.get(child);
    if (!was) continue; // a tile that is new has its own arrival
    const now = child.getBoundingClientRect();
    const dx = was.left - now.left;
    const dy = was.top - now.top;
    if (dx !== 0 || dy !== 0) child.animate([{ translate: `${dx}px ${dy}px` }, { translate: "0 0" }], { duration: 800, easing: "cubic-bezier(.2,.9,.3,1)" });
  }
}
