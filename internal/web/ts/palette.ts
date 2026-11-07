// The command palette (Ctrl K or Cmd K): jump to a view or a service and do what the person can do, from the keyboard.
// What it lists is read from the page itself each time it opens, so it offers exactly the buttons the person has (an
// owner's commands differ from a sender's, and the administrator's page appears only for an administrator), and running
// a command presses the very button the page has, so questions are still asked and nothing is bypassed.

import { t } from "./i18n.ts";

export interface Command {
  label: string;
  hint: string;
  run: () => void;
}

/** How well a query matches a label: 0 for no match, higher is better. Every word of the query must appear. */
export function score(query: string, label: string): number {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return 1;
  const text = label.toLowerCase();
  let total = 0;
  for (const word of words) {
    const at = text.indexOf(word);
    if (at < 0) return 0;
    total += at === 0 || text[at - 1] === " " ? 3 : 1;
  }
  return total;
}

export function rank(query: string, commands: readonly Command[]): Command[] {
  return commands
    .map((command, index) => ({ command, index, s: score(query, `${command.label} ${command.hint}`) }))
    .filter((c) => c.s > 0)
    .sort((a, b) => b.s - a.s || a.index - b.index)
    .map((c) => c.command);
}

function goTo(view: string): void {
  const tab = document.querySelector<HTMLAnchorElement>(`nav.tabs a[data-view="${view}"]`);
  tab?.click();
}

/** The commands this person has, from what the page shows. */
function collect(): Command[] {
  const commands: Command[] = [];
  const views = [["overview", t("Overview")], ["services", t("Services")], ["activity", t("Activity")]] as const;
  for (const [view, name] of views) {
    commands.push({ label: t("Go to {view}", { view: name }), hint: t("view"), run: () => goTo(view) });
  }
  // what waits on the person: the buttons on the "Needs you" cards
  document.querySelectorAll<HTMLElement>("#v-overview .needs .card.need").forEach((card) => {
    const title = card.querySelector("b")?.textContent?.trim() ?? "";
    card.querySelectorAll<HTMLButtonElement>("button").forEach((button) => {
      commands.push({ label: `${button.textContent?.trim() ?? ""}: ${title}`, hint: t("needs you"), run: () => button.click() });
    });
  });
  // each service: open it, and pause or resume it
  document.querySelectorAll<HTMLDetailsElement>("#v-services details.svc").forEach((row) => {
    const name = row.querySelector(".title b")?.textContent?.trim() ?? "";
    commands.push({ label: t("Open {name}", { name }), hint: t("service"), run: () => { goTo("services"); row.open = true; row.scrollIntoView({ block: "center" }); } });
    row.querySelectorAll<HTMLButtonElement>("button[data-act]").forEach((button) => {
      // the buttons that pause or resume say so in data-act, whatever language their words are in
      commands.push({ label: `${button.textContent?.trim() ?? ""}: ${name}`, hint: t("service"), run: () => button.click() });
    });
  });
  const offer = document.querySelector<HTMLDetailsElement>("#v-services details.offer");
  if (offer) commands.push({ label: t("Offer a service of your own"), hint: t("service"), run: () => { goTo("services"); offer.open = true; offer.scrollIntoView({ block: "center" }); } });
  const sync = document.querySelector<HTMLButtonElement>('button[hx-post="sync"]');
  if (sync) commands.push({ label: t("Sync now"), hint: t("action"), run: () => sync.click() });
  const admin = document.querySelector<HTMLAnchorElement>("a.adminlink");
  if (admin) commands.push({ label: t("Open the administrator's page"), hint: t("admin"), run: () => { location.href = admin.href; } });
  const theme = document.getElementById("theme");
  if (theme) commands.push({ label: t("Switch between the light and the dark theme"), hint: t("theme"), run: () => theme.click() });
  const sound = document.getElementById("sound");
  if (sound) commands.push({ label: t("Turn the arrival chime on or off"), hint: t("sound"), run: () => sound.click() });
  return commands;
}

export function initPalette(): void {
  const dialog = document.getElementById("cmd");
  const input = document.getElementById("cmd-q");
  const list = document.getElementById("cmd-list");
  if (!(dialog instanceof HTMLDialogElement) || !(input instanceof HTMLInputElement) || !list) return;
  let all: Command[] = [];
  let shown: Command[] = [];
  let selected = 0;

  const draw = (): void => {
    shown = rank(input.value, all);
    selected = Math.min(selected, Math.max(0, shown.length - 1));
    list.replaceChildren();
    shown.forEach((command, i) => {
      const item = document.createElement("li");
      item.setAttribute("role", "option");
      item.setAttribute("aria-selected", String(i === selected));
      item.dataset["index"] = String(i);
      const label = document.createElement("span");
      label.textContent = command.label;
      const hint = document.createElement("small");
      hint.textContent = command.hint;
      item.append(label, hint);
      list.appendChild(item);
    });
    if (shown.length === 0) {
      const none = document.createElement("li");
      none.textContent = t("Nothing matches");
      list.appendChild(none);
    }
    input.setAttribute("aria-activedescendant", "");
  };
  const open = (): void => {
    if (dialog.open) return;
    all = collect();
    input.value = "";
    selected = 0;
    draw();
    dialog.showModal();
    input.focus();
  };
  const run = (index: number): void => {
    const command = shown[index];
    if (!command) return;
    dialog.close();
    setTimeout(command.run, 60); // after the dialog has closed, so a question it leads to can open
  };

  input.addEventListener("input", () => { selected = 0; draw(); });
  input.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown") { selected = Math.min(shown.length - 1, selected + 1); draw(); e.preventDefault(); }
    else if (e.key === "ArrowUp") { selected = Math.max(0, selected - 1); draw(); e.preventDefault(); }
    else if (e.key === "Enter") { run(selected); e.preventDefault(); }
  });
  list.addEventListener("click", (e) => {
    const item = e.target instanceof Element ? e.target.closest<HTMLElement>("li[data-index]") : null;
    if (item) run(Number(item.dataset["index"]));
  });
  dialog.addEventListener("click", (e) => { if (e.target === dialog) dialog.close(); });
  addEventListener("keydown", (e) => {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") { e.preventDefault(); open(); }
  });
  document.getElementById("palette")?.addEventListener("click", open);
}
