// The "Send photos" page: choose photos and videos, choose where they go, and watch them leave. The work of sending is the
// queue's (send-queue.ts); this draws it and takes the person's choices.

import { n, t } from "./i18n.ts";
import { finished, looksLikeMedia, percent, sizeText, tally, type Item } from "./send-core.ts";
import { MemoryStore, Queue, type Env } from "./send-queue.ts";

const FAVOURITE = "handoff-send-service";

function remembered(): string | null {
  try {
    return localStorage.getItem(FAVOURITE);
  } catch {
    return null;
  }
}
function remember(service: string): void {
  try {
    localStorage.setItem(FAVOURITE, service);
  } catch {
    /* a private window keeps nothing; the choice is just not remembered */
  }
}

/** Puts the service the person chose last on top and selects it. */
function favouriteFirst(form: HTMLFormElement): void {
  const last = remembered();
  if (last === null) return;
  const radios = Array.from(form.querySelectorAll<HTMLInputElement>('input[name="service"]'));
  const hit = radios.find((r) => r.value === last);
  const fieldset = form.querySelector("fieldset.targets");
  const label = hit?.closest("label");
  if (!hit || !fieldset || !label) return;
  hit.checked = true;
  if (last !== "") fieldset.querySelector("legend")?.after(label);
}

function stateText(i: Item): string {
  switch (i.state) {
    case "waiting":
      return t("Waiting");
    case "sending":
      return t("Sending, {pct}%", { pct: percent(i.sent, i.size) });
    case "offline":
      return t("Waiting for the network");
    case "signin":
      return t("Sign in to Immich again to go on");
    case "done":
      return i.duplicate ? t("Already in your library") : t("Sent");
    case "failed":
      return t("Could not be sent: {reason}", { reason: i.text });
    default:
      return "";
  }
}

function render(list: HTMLElement, queue: Queue): void {
  list.replaceChildren();
  for (const item of queue.items) {
    if (item.state === "cancelled") continue;
    const li = document.createElement("li");
    li.className = `send-item ${item.state}`;
    const name = document.createElement("span");
    name.className = "name";
    name.textContent = item.name;
    const size = document.createElement("small");
    size.className = "meta";
    size.textContent = sizeText(item.size);
    const bar = document.createElement("span");
    bar.className = "send-bar";
    bar.setAttribute("role", "progressbar");
    bar.setAttribute("aria-valuemin", "0");
    bar.setAttribute("aria-valuemax", "100");
    bar.setAttribute("aria-valuenow", String(percent(item.sent, item.size)));
    bar.setAttribute("aria-label", item.name);
    const fill = document.createElement("i");
    fill.style.width = `${item.state === "done" ? 100 : percent(item.sent, item.size)}%`;
    bar.append(fill);
    const state = document.createElement("small");
    state.className = "state";
    state.textContent = stateText(item);
    li.append(name, size, bar, state);
    const actions = document.createElement("span");
    actions.className = "actions";
    if (item.state === "failed" || item.state === "signin") {
      const again = document.createElement("button");
      again.type = "button";
      again.className = "link";
      again.textContent = item.state === "signin" ? t("Try again") : t("Send again");
      again.addEventListener("click", () => void queue.retry(item.id).then(() => void queue.run()));
      actions.append(again);
    }
    if (!finished(item.state) || item.state === "failed") {
      const cancel = document.createElement("button");
      cancel.type = "button";
      cancel.className = "link";
      cancel.textContent = t("Remove");
      cancel.addEventListener("click", () => void queue.cancel(item.id));
      actions.append(cancel);
    }
    li.append(actions);
    list.append(li);
  }
}

export function initSend(): void {
  const form = document.getElementById("send");
  const list = document.getElementById("send-list");
  const input = document.getElementById("send-files");
  const go = document.getElementById("send-go");
  if (!(form instanceof HTMLFormElement) || !list || !(input instanceof HTMLInputElement) || !(go instanceof HTMLButtonElement)) return;
  const maxBytes = Number(form.dataset["fileMb"] ?? "0") * (1 << 20);
  favouriteFirst(form);

  const env: Env = {
    base: document.baseURI,
    now: () => Date.now(),
    newId: () => crypto.randomUUID(),
    pause: (ms) =>
      new Promise((resolve) => {
        const done = (): void => {
          clearTimeout(timer);
          removeEventListener("online", done);
          resolve();
        };
        const timer = setTimeout(done, ms);
        addEventListener("online", done);
      }),
  };
  const summary = document.createElement("p");
  summary.className = "meta send-summary";
  summary.setAttribute("role", "status");
  list.before(summary);
  const redraw = (): void => {
    render(list, queue);
    const c = tally(queue.items);
    summary.textContent = c.total === 0 ? "" : c.open > 0 ? n("{n} file left to send", "{n} files left to send", c.open) : n("{n} file sent", "{n} files sent", c.done);
    go.disabled = picked.length === 0;
    go.textContent = picked.length > 0 ? n("Send {n} file", "Send {n} files", picked.length) : t("Send");
  };
  const queue: Queue = new Queue(new MemoryStore(), env, redraw);
  let picked: File[] = [];

  input.addEventListener("change", () => {
    const skipped: string[] = [];
    picked = Array.from(input.files ?? []).filter((f) => {
      const ok = looksLikeMedia(f.name, f.type) && f.size > 0 && (maxBytes === 0 || f.size <= maxBytes);
      if (!ok) skipped.push(f.name);
      return ok;
    });
    const flash = document.getElementById("flash");
    if (flash) flash.textContent = skipped.length > 0 ? n("{n} file was left out: it is not a photo or video, or it is too large", "{n} files were left out: they are not photos or videos, or they are too large", skipped.length) : "";
    redraw();
  });
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    if (picked.length === 0) return;
    const chosen = form.querySelector<HTMLInputElement>('input[name="service"]:checked');
    const service = chosen?.value ?? "";
    remember(service);
    const files = picked;
    picked = [];
    input.value = "";
    void queue.add(files, service).then(() => queue.run());
  });
  void queue.load().then(() => queue.run());
  redraw();
}
