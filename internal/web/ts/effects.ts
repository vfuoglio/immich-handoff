// The optional effects: the light behind the page follows the pointer and flares when a photo lands, and cards tilt
// toward the pointer with a moving highlight. All of it is driven by events (nothing runs while the page is idle),
// only on a fine pointer such as a mouse, never with reduced motion, and nothing is needed to use any control.

import type { LiveEvent } from "./live.ts";

const TILT = ".card.need, .kpi, .guide";
const MAX_DEGREES = 6;

/** The tilt, in degrees, for a pointer at (x, y) as a fraction of a card's width and height. */
export function tiltFor(x: number, y: number): { rx: number; ry: number } {
  const clamp = (n: number): number => Math.min(1, Math.max(0, n));
  return { ry: (clamp(x) - 0.5) * 2 * MAX_DEGREES, rx: (0.5 - clamp(y)) * 2 * MAX_DEGREES };
}

export function initEffects(): void {
  const root = document.documentElement;
  const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;
  const fine = matchMedia("(pointer: fine)").matches;
  if (reduced) return;

  // the light behind the page follows the pointer
  let frame = 0;
  let last: PointerEvent | undefined;
  addEventListener("pointermove", (e) => {
    last = e;
    if (frame) return;
    frame = requestAnimationFrame(() => {
      frame = 0;
      if (!last) return;
      root.style.setProperty("--px", (last.clientX / innerWidth).toFixed(3));
      root.style.setProperty("--py", (last.clientY / innerHeight).toFixed(3));
    });
  }, { passive: true });

  // a flare from the horizon when a photo lands
  const glow = document.getElementById("glow");
  document.addEventListener("handoff:live", (e: CustomEvent<LiveEvent>) => {
    if (e.detail.kind !== "handed" || document.hidden || !glow) return;
    glow.animate([{ opacity: 0 }, { opacity: 0.9, offset: 0.18 }, { opacity: 0 }], { duration: 1100, easing: "ease-out" });
  });

  // a soft light under the pointer inside a card (the style sheet shows it while the pointer is over the card)
  if (fine) {
    addEventListener("pointermove", (ev) => {
      const card = ev.target instanceof Element ? ev.target.closest<HTMLElement>(".card") : null;
      if (!card) return;
      const box = card.getBoundingClientRect();
      card.style.setProperty("--sx", `${(ev.clientX - box.left).toFixed(0)}px`);
      card.style.setProperty("--sy", `${(ev.clientY - box.top).toFixed(0)}px`);
    }, { passive: true });
  }

  // cards tilt toward the pointer
  if (!fine) return;
  root.dataset["fxTilt"] = "on";
  let current: HTMLElement | null = null;
  const release = (card: HTMLElement): void => {
    card.classList.remove("is-tilting");
    card.style.removeProperty("--rx");
    card.style.removeProperty("--ry");
  };
  addEventListener("pointermove", (e) => {
    const card = e.target instanceof Element ? e.target.closest<HTMLElement>(TILT) : null;
    if (current && current !== card) release(current);
    current = card;
    if (!card) return;
    const box = card.getBoundingClientRect();
    const x = (e.clientX - box.left) / box.width;
    const y = (e.clientY - box.top) / box.height;
    const { rx, ry } = tiltFor(x, y);
    card.classList.add("is-tilting");
    card.style.setProperty("--rx", `${rx.toFixed(2)}deg`);
    card.style.setProperty("--ry", `${ry.toFixed(2)}deg`);
    card.style.setProperty("--mx", `${(x * 100).toFixed(1)}%`);
    card.style.setProperty("--my", `${(y * 100).toFixed(1)}%`);
  }, { passive: true });
  document.addEventListener("pointerleave", () => current && release(current));
}
