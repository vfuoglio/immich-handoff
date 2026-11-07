// The light/dark choice. This script is loaded in the page's head without `defer`, so a saved choice is applied
// before the first paint (no flash of the wrong theme). With no saved choice the page follows the browser's own
// setting through CSS alone. The choice lives in this browser only (localStorage), never on the server.

const KEY = "handoff-theme";
type Theme = "light" | "dark";

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

const initial = saved();
if (initial) document.documentElement.dataset["theme"] = initial;

function reflect(button: HTMLElement): void {
  button.setAttribute("aria-pressed", String(effective() === "dark"));
}

document.addEventListener("DOMContentLoaded", () => {
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
