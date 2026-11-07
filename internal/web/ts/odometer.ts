// Rolling digits: a number on the page rolls from its old value to its new one, column by column, like a departures
// board, instead of just changing. The server writes the plain number (so nothing depends on script), and this makes
// it roll for any element marked data-odo="<key>"; the key lets a number that was replaced by a refresh remember what
// it showed before. Not used when the browser asks for reduced motion.

/** The digit each column shows for a number, padded or trimmed to `length` columns. */
export function columns(value: number, length: number): number[] {
  const digits = String(Math.max(0, Math.trunc(value))).padStart(length, "0").slice(-length);
  return Array.from(digits, (d) => Number(d));
}

const shown = new Map<string, number>(); // the last value each key showed

function reduced(): boolean {
  return matchMedia("(prefers-reduced-motion: reduce)").matches;
}

/** Shows the value in the element, rolling from what that key showed last (or from 0). */
export function setOdometer(el: HTMLElement, value: number, key: string): void {
  if (reduced() || !Number.isFinite(value)) {
    el.textContent = String(value);
    shown.set(key, value);
    return;
  }
  const length = String(Math.max(0, Math.trunc(value))).length;
  const from = columns(shown.get(key) ?? 0, length);
  const to = columns(value, length);
  el.replaceChildren();
  el.setAttribute("role", "img");
  el.setAttribute("aria-label", String(value));
  const strips: HTMLElement[] = [];
  for (let i = 0; i < length; i++) {
    const wheel = document.createElement("span");
    wheel.className = "odo";
    wheel.setAttribute("aria-hidden", "true");
    const strip = document.createElement("i");
    for (let d = 0; d < 10; d++) {
      const digit = document.createElement("b");
      digit.textContent = String(d);
      strip.appendChild(digit);
    }
    strip.style.transition = "none";
    strip.style.transform = `translateY(-${from[i] ?? 0}em)`;
    wheel.appendChild(strip);
    el.appendChild(wheel);
    strips.push(strip);
  }
  void el.offsetWidth; // settle at the old value, then roll
  strips.forEach((strip, i) => {
    strip.style.transition = "";
    strip.style.transform = `translateY(-${to[i] ?? 0}em)`;
  });
  shown.set(key, value);
}

/** Makes every marked number inside the scope roll to what it says (skeletons and non-numbers are left alone). */
export function enhance(scope: ParentNode): void {
  scope.querySelectorAll<HTMLElement>("[data-odo]").forEach((el) => {
    const text = (el.getAttribute("aria-label") ?? el.textContent ?? "").trim();
    if (!/^\\d+$/.test(text)) return;
    setOdometer(el, Number(text), el.dataset["odo"] ?? "");
  });
}
