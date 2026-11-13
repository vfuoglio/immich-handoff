// The look of the page: a colour scheme (twilight or immich) and a variant (light or dark). This script is loaded in
// the page's head without `defer`, so a saved choice is applied before the first paint (no flash of the wrong look).
// With nothing saved the page keeps what the administrator set (THEME, THEME_MODE) and otherwise follows the browser's
// own light/dark setting through CSS alone. A person's choice lives in this browser only (localStorage), never on the
// server, and wins over the administrator's.

const KEY = "handoff-theme";
const PALETTE_KEY = "handoff-palette";
type Theme = "light" | "dark";
type Palette = "twilight" | "immich";

// the colour of a phone's address bar for each scheme (the same values as theme.go's themeColor)
const BAR: Record<Palette, Record<Theme, string>> = {
  twilight: { light: "#f5f2fd", dark: "#0d0b20" },
  immich: { light: "#f5f7fd", dark: "#07080d" },
};

function savedPalette(): Palette | null {
  try {
    const value = localStorage.getItem(PALETTE_KEY);
    return value === "twilight" || value === "immich" ? value : null;
  } catch {
    return null;
  }
}

function paintBar(palette: Palette): void {
  document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]').forEach((meta) => {
    meta.content = BAR[palette][meta.media.includes("dark") ? "dark" : "light"];
  });
}

function saved(): Theme | null {
  try {
    const value = localStorage.getItem(KEY);
    return value === "light" || value === "dark" ? value : null;
  } catch {
    return null; // storage can be blocked; the browser's own setting then decides
  }
}

function effective(): Theme {
  const set = document.documentElement.dataset["theme"];
  if (set === "light" || set === "dark") return set;
  return matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

// The dashboard has three views (Overview, Services, Activity) chosen by the address's #fragment. Marking the page as having
// script, and which view is showing, before the first paint is what keeps a view from flashing and then hiding; without
// script all three are simply shown one after the other.
const VIEWS = ["overview", "services", "activity"];
document.documentElement.classList.add("js");
document.documentElement.dataset["view"] = VIEWS.includes(location.hash.slice(1)) ? location.hash.slice(1) : "overview";

const initial = saved();
if (initial) document.documentElement.dataset["theme"] = initial;
const chosen = savedPalette();
if (chosen) document.documentElement.dataset["palette"] = chosen;

function reflect(button: HTMLElement): void {
  button.setAttribute("aria-pressed", String(effective() === "dark"));
}

// the menu of colour schemes in the bar
function initScheme(): void {
  const menu = document.querySelector<HTMLDetailsElement>("details.scheme");
  if (!menu) return;
  const buttons = menu.querySelectorAll<HTMLButtonElement>("button[data-palette]");
  const show = (palette: string): void => buttons.forEach((b) => b.setAttribute("aria-checked", String(b.dataset["palette"] === palette)));
  const current = document.documentElement.dataset["palette"];
  if (current === "twilight" || current === "immich") {
    show(current);
    if (chosen) paintBar(chosen);
  }
  buttons.forEach((b) =>
    b.addEventListener("click", () => {
      const palette: Palette = b.dataset["palette"] === "immich" ? "immich" : "twilight";
      document.documentElement.dataset["palette"] = palette;
      try {
        localStorage.setItem(PALETTE_KEY, palette);
      } catch {
        /* the choice then lasts for this visit only */
      }
      show(palette);
      paintBar(palette);
      menu.open = false;
    }),
  );
  document.addEventListener("click", (e) => {
    if (menu.open && e.target instanceof Node && !menu.contains(e.target)) menu.open = false;
  });
}

document.addEventListener("DOMContentLoaded", () => {
  initScheme();
  const button = document.getElementById("theme");
  if (!button) return;
  reflect(button);
  button.addEventListener("click", () => {
    const next: Theme = effective() === "dark" ? "light" : "dark";
    document.documentElement.dataset["theme"] = next;
    try {
      localStorage.setItem(KEY, next);
    } catch {
      /* the choice then lasts for this visit only */
    }
    reflect(button);
  });
});

// makes this file a module of its own, so names in one script never clash with another
export {};
