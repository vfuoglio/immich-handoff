// Keys for the dashboard, for people who like them (nothing needs them): A and D answer the request that is waiting
// (the buttons say so), and G followed by O, S or A goes to Overview, Services or Activity. They never fire while
// someone is typing, with a dialog open, or with Ctrl, Cmd or Alt held (Ctrl K belongs to the command palette).

const GO: Record<string, string> = { o: "overview", s: "services", a: "activity" };

function typing(): boolean {
  const el = document.activeElement;
  if (!(el instanceof HTMLElement)) return false;
  return el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName);
}

export function initKeys(): void {
  if (!document.querySelector("nav.tabs")) return; // only the dashboard has views
  let goAt = 0;
  document.addEventListener("keydown", (e) => {
    if (e.ctrlKey || e.metaKey || e.altKey || e.shiftKey || typing() || document.querySelector("dialog[open]")) return;
    const key = e.key.toLowerCase();
    if (goAt && Date.now() - goAt < 900) {
      goAt = 0;
      const view = GO[key];
      if (view) {
        e.preventDefault();
        location.hash = view;
      }
      return;
    }
    if (key === "g") {
      goAt = Date.now();
      return;
    }
    if (document.documentElement.dataset["view"] !== "overview" && document.documentElement.dataset["view"] !== undefined) return;
    const button = document.querySelector<HTMLButtonElement>(`#v-overview .card.need button[data-key="${key.toUpperCase()}"]`);
    if (button && !button.disabled) {
      e.preventDefault();
      button.click();
    }
  });
}
