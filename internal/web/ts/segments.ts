// The switch at the top of the Services view (for someone who can make more than services): drop boxes, client rooms,
// frames and shared albums each have a part of their own, one shown at a time. The choice is kept for the visit
// (sessionStorage) so the page comes back on the same part after an action. Without script every part is shown.

const KEY = "handoff-segment";

function names(): string[] {
  return [...document.querySelectorAll<HTMLElement>(".seg [role=tab]")].map((tab) => tab.dataset["seg"] ?? "");
}

/** Show one part of the Services view (the palette uses it to reach a service that is in another part). */
export function showSegment(name: string): void {
  const tabs = document.querySelectorAll<HTMLButtonElement>(".seg [role=tab]");
  if (tabs.length === 0 || !names().includes(name)) return;
  tabs.forEach((tab) => {
    const on = tab.dataset["seg"] === name;
    tab.setAttribute("aria-selected", String(on));
    tab.tabIndex = on ? 0 : -1;
  });
  document.querySelectorAll<HTMLElement>(".seg-panel").forEach((panel) => {
    panel.hidden = panel.dataset["seg"] !== name;
  });
  try {
    sessionStorage.setItem(KEY, name);
  } catch {
    // private window: the choice is simply not kept
  }
}

export function initSegments(): void {
  const tabs = [...document.querySelectorAll<HTMLButtonElement>(".seg [role=tab]")];
  if (tabs.length === 0) return;
  document.querySelector(".seg")?.classList.add("on");
  let first = "services";
  try {
    const saved = sessionStorage.getItem(KEY);
    if (saved && names().includes(saved)) first = saved;
  } catch {
    // the first part it is
  }
  showSegment(first);
  tabs.forEach((tab, i) => {
    tab.addEventListener("click", () => showSegment(tab.dataset["seg"] ?? "services"));
    tab.addEventListener("keydown", (e) => {
      const step = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
      if (e.key === "Home" || e.key === "End" || step !== 0) {
        e.preventDefault();
        const to = e.key === "Home" ? 0 : e.key === "End" ? tabs.length - 1 : (i + step + tabs.length) % tabs.length;
        const target = tabs[to];
        if (target) {
          showSegment(target.dataset["seg"] ?? "services");
          target.focus();
        }
      }
    });
  });
}
