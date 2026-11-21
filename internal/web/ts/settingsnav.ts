// The categories of the Settings view: one category's cards are shown at a time. The choice is kept for the visit
// (sessionStorage) so the page comes back on the same category after an action. Without script every category is shown,
// one under the other.

const KEY = "handoff-settings-cat";

function tabs(): HTMLButtonElement[] {
  return [...document.querySelectorAll<HTMLButtonElement>(".settings-nav [role=tab]")];
}

/** Show one category of the Settings view (the palette uses it to reach a card that is in another category). */
export function showSettingsCat(name: string): void {
  const all = tabs();
  if (!all.some((tab) => tab.dataset["cat"] === name)) return;
  all.forEach((tab) => {
    const on = tab.dataset["cat"] === name;
    tab.setAttribute("aria-selected", String(on));
    tab.tabIndex = on ? 0 : -1;
  });
  document.querySelectorAll<HTMLElement>(".settings-panel").forEach((panel) => {
    panel.hidden = panel.dataset["cat"] !== name;
  });
  try {
    sessionStorage.setItem(KEY, name);
  } catch {
    // private window: the choice is simply not kept
  }
}

/** The category that holds an element, so a link to a card can open its category first. */
export function showSettingsCatOf(el: Element): void {
  const panel = el.closest<HTMLElement>(".settings-panel");
  if (panel?.dataset["cat"]) showSettingsCat(panel.dataset["cat"]);
}

export function initSettingsNav(): void {
  const all = tabs();
  if (all.length === 0) return;
  let first = all[0]?.dataset["cat"] ?? "";
  try {
    const saved = sessionStorage.getItem(KEY);
    if (saved && all.some((tab) => tab.dataset["cat"] === saved)) first = saved;
  } catch {
    // the first category it is
  }
  showSettingsCat(first);
  all.forEach((tab, i) => {
    tab.addEventListener("click", () => showSettingsCat(tab.dataset["cat"] ?? first));
    tab.addEventListener("keydown", (e) => {
      const step = e.key === "ArrowDown" || e.key === "ArrowRight" ? 1 : e.key === "ArrowUp" || e.key === "ArrowLeft" ? -1 : 0;
      if (e.key === "Home" || e.key === "End" || step !== 0) {
        e.preventDefault();
        const to = e.key === "Home" ? 0 : e.key === "End" ? all.length - 1 : (i + step + all.length) % all.length;
        const target = all[to];
        if (target) {
          showSettingsCat(target.dataset["cat"] ?? first);
          target.focus();
        }
      }
    });
  });
  // a link to a card (an address with a #, or a button in the page) opens the category the card is in
  const open = (): void => {
    const id = location.hash.slice(1);
    const el = id ? document.getElementById(id) : null;
    if (el) showSettingsCatOf(el);
  };
  window.addEventListener("hashchange", open);
  open();
}
