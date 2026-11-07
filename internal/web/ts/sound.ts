// The arrival chime: a soft two-note sound (made by the browser, no sound file) and a short phone vibration when a photo
// lands. On by default, with a speaker icon in the bar to turn it off; the choice is kept in this browser. Browsers only
// allow sound after the person has interacted with the page, so the first click or key press wakes it.

import type { LiveEvent } from "./live.ts";

const KEY = "handoff-sound";

function enabled(): boolean {
  try {
    return localStorage.getItem(KEY) !== "off";
  } catch {
    return true;
  }
}

export function initSound(): void {
  const button = document.getElementById("sound");
  if (!button) return;
  let on = enabled();
  let context: AudioContext | undefined;
  const reflect = (): void => button.setAttribute("aria-pressed", String(on));
  reflect();

  const wake = (): void => {
    try {
      context ??= new AudioContext();
      if (context.state === "suspended") void context.resume();
    } catch {
      /* no sound on this browser */
    }
  };
  for (const name of ["pointerdown", "keydown"] as const) addEventListener(name, wake, { once: true, passive: true });

  const chime = (): void => {
    if (!on || !context || context.state !== "running") return;
    const start = context.currentTime;
    [660, 990].forEach((hz, i) => {
      const osc = context!.createOscillator();
      const gain = context!.createGain();
      const at = start + i * 0.09;
      osc.type = "sine";
      osc.frequency.value = hz;
      gain.gain.setValueAtTime(0, at);
      gain.gain.linearRampToValueAtTime(0.06, at + 0.02);
      gain.gain.exponentialRampToValueAtTime(0.0001, at + 0.5);
      osc.connect(gain);
      gain.connect(context!.destination);
      osc.start(at);
      osc.stop(at + 0.55);
    });
    if (navigator.vibrate) navigator.vibrate(14);
  };

  button.addEventListener("click", () => {
    on = !on;
    try {
      localStorage.setItem(KEY, on ? "on" : "off");
    } catch {
      /* the choice then lasts for this visit only */
    }
    wake();
    reflect();
    if (on) chime();
  });
  document.addEventListener("handoff:live", (e: CustomEvent<LiveEvent>) => {
    if (e.detail.kind === "handed" && e.detail.reason !== "duplicate" && !document.hidden) chime();
  });
}
