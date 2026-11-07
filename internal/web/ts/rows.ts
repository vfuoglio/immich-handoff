// The service rows open in place. Most actions (pause, limits, a new link) answer by reloading the page, which would
// close the row the person was working in, so the open row is remembered for this browser tab and opened again.

const KEY = "handoff-open-service";

function nameOf(row: Element): string {
  return row.querySelector(".title b")?.textContent?.trim() ?? "";
}

function remembered(): string | null {
  try {
    return sessionStorage.getItem(KEY);
  } catch {
    return null; // storage can be blocked; the row just starts closed
  }
}

function remember(name: string | null): void {
  try {
    if (name) sessionStorage.setItem(KEY, name);
    else sessionStorage.removeItem(KEY);
  } catch {
    // nothing to do without storage
  }
}

export function initRows(): void {
  const rows = document.querySelectorAll<HTMLDetailsElement>("#v-services details.svc");
  const saved = remembered();
  rows.forEach((row) => {
    if (saved && nameOf(row) === saved) row.open = true;
    row.addEventListener("toggle", () => {
      if (row.open) remember(nameOf(row));
      else if (remembered() === nameOf(row)) remember(null);
    });
  });
}
