// The "Send photos" page: choose photos and videos, choose where they go, and watch them leave. The work of sending is the
// queue's (send-queue.ts); this draws it and takes the person's choices.

import { n, t } from "./i18n.ts";
import { ask } from "./ask.ts";
import { finished, heldForWifi, isLargeVideo, looksLikeMedia, onMobileData, percent, sizeText, tally, type Connection, type Item } from "./send-core.ts";
import { MemoryStore, Queue, type Env, type Store } from "./send-queue.ts";
import { register } from "./push.ts";
import { canStore, getPref, IdbStore, setPref } from "./send-store.ts";

interface NavigatorWithConnection extends Navigator {
  connection?: Connection & EventTarget;
}
const connection = (): (Connection & EventTarget) | undefined => (navigator as NavigatorWithConnection).connection;
const SEEN_LOCATION = "handoff-send-location";
const WIFI_ONLY = "wifiOnly";

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
    case "pick":
      return t("Choose where it goes, then press Send");
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
  for (const item of queue.mine) {
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

function seen(key: string): boolean {
  try {
    return localStorage.getItem(key) === "1";
  } catch {
    return false;
  }
}
function markSeen(key: string): void {
  try {
    localStorage.setItem(key, "1");
  } catch {
    /* it will be asked again */
  }
}

export function initSend(): void {
  const form = document.getElementById("send");
  const list = document.getElementById("send-list");
  const input = document.getElementById("send-files");
  const go = document.getElementById("send-go");
  const wifi = document.getElementById("send-wifi");
  if (!(form instanceof HTMLFormElement) || !list || !(input instanceof HTMLInputElement) || !(go instanceof HTMLButtonElement) || !(wifi instanceof HTMLInputElement)) return;
  const maxBytes = Number(form.dataset["fileMb"] ?? "0") * (1 << 20);
  favouriteFirst(form);

  const env: Env = {
    base: document.baseURI,
    owner: form.dataset["user"] ?? "",
    held: () => heldForWifi(wifi.checked, connection()),
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
  const flash = (text: string): void => {
    const el = document.getElementById("flash");
    if (el) el.textContent = text;
  };
  const clear = document.getElementById("send-clear");
  let picked: File[] = [];
  const redraw = (): void => {
    render(list, queue);
    const c = tally(queue.mine);
    const shared = queue.shared.length;
    const held = env.held() && c.open > 0;
    summary.textContent = shared > 0 ? n("{n} file shared with Handoff is waiting for you to choose where it goes", "{n} files shared with Handoff are waiting for you to choose where they go", shared) : held ? t("Waiting for Wi-Fi: this device is on mobile data") : c.total === 0 ? "" : c.open > 0 ? n("{n} file left to send", "{n} files left to send", c.open) : n("{n} file sent", "{n} files sent", c.done);
    if (clear) clear.hidden = !queue.mine.some((i) => finished(i.state));
    const count = picked.length + queue.shared.length;
    go.disabled = count === 0;
    go.textContent = count > 0 ? n("Send {n} file", "Send {n} files", count) : t("Send");
  };
  let store: Store = new MemoryStore();
  const queue: Queue = new Queue(
    {
      put: (i, f) => store.put(i, f),
      file: (id) => store.file(id),
      all: () => store.all(),
      release: (id) => store.release(id),
      remove: (id) => store.remove(id),
    },
    env,
    redraw,
  );

  wifi.addEventListener("change", () => {
    void setPref(WIFI_ONLY, wifi.checked);
    redraw();
    if (!env.held()) void queue.run();
  });
  connection()?.addEventListener("change", () => {
    redraw();
    if (!env.held()) void queue.run();
  });
  addEventListener("online", () => void queue.run());
  clear?.addEventListener("click", () => void queue.clear());

  input.addEventListener("change", () => {
    const skipped: string[] = [];
    picked = Array.from(input.files ?? []).filter((f) => {
      const ok = looksLikeMedia(f.name, f.type) && f.size > 0 && (maxBytes === 0 || f.size <= maxBytes);
      if (!ok) skipped.push(f.name);
      return ok;
    });
    flash(skipped.length > 0 ? n("{n} file was left out: it is not a photo or video, or it is too large", "{n} files were left out: they are not photos or videos, or they are too large", skipped.length) : "");
    redraw();
  });
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    if (picked.length === 0 && queue.shared.length === 0) return;
    const files = picked;
    const chosen = form.querySelector<HTMLInputElement>('input[name="service"]:checked');
    const service = chosen?.value ?? "";
    void (async () => {
      // photos carry the place they were taken: said once, before the first send
      if (!seen(SEEN_LOCATION)) {
        const yes = await ask(t("Locations go with your photos"), t("Photos are sent as they are, so the place where each was taken goes with it, and so do the details Immich shows, for you and for whoever you send them to. Nothing is removed."), t("Cancel"), t("Continue"));
        if (!yes) return;
        markSeen(SEEN_LOCATION);
      }
      // a large video over mobile data is asked about
      const large = files.filter((f) => isLargeVideo(f.name, f.type, f.size)).length;
      if (large > 0 && onMobileData(connection()) && !wifi.checked) {
        const now = await ask(t("A large video on mobile data"), n("{n} large video is about to go over mobile data. Send it now, or wait until this device is on Wi-Fi?", "{n} large videos are about to go over mobile data. Send them now, or wait until this device is on Wi-Fi?", large), t("Wait for Wi-Fi"), t("Send now"));
        if (!now) {
          wifi.checked = true;
          void setPref(WIFI_ONLY, true);
        }
      }
      remember(service);
      picked = [];
      input.value = "";
      const refused = files.length > 0 ? await queue.add(files, service) : [];
      await queue.assign(service);
      flash(refused.length > 0 ? n("{n} file could not be kept for sending: this browser has no room left. Send what is waiting first, or free some space on the device.", "{n} files could not be kept for sending: this browser has no room left. Send what is waiting first, or free some space on the device.", refused.length) : "");
      void queue.run();
    })();
  });

  void (async () => {
    wifi.checked = await getPref<boolean>(WIFI_ONLY, false);
    if (await canStore()) store = new IdbStore();
    else flash(t("This browser cannot keep files waiting while the page is closed; keep this page open until they are sent."));
    await queue.load();
    void queue.run();
  })();
  initShare(form);
  redraw();
}

/** Sharing from the gallery needs the app installed (Android Chrome offers the entry then) and the worker running, which is what
 *  receives the files. The page registers the worker and, where the browser offers to install, explains and offers. */
function initShare(form: HTMLFormElement): void {
  if ("serviceWorker" in navigator) void register().catch(() => undefined);
  const card = document.getElementById("send-install");
  const button = document.getElementById("send-install-go");
  if (!card || !(button instanceof HTMLButtonElement)) return;
  let offered: (Event & { prompt(): Promise<void>; userChoice: Promise<{ outcome: string }> }) | null = null;
  addEventListener("beforeinstallprompt", (e) => {
    e.preventDefault();
    if (matchMedia("(display-mode: standalone)").matches) return;
    offered = e as typeof offered;
    card.hidden = false;
  });
  addEventListener("appinstalled", () => {
    card.hidden = true;
  });
  button.addEventListener("click", () => {
    if (!offered) return;
    void offered.prompt().then(() => {
      offered = null;
      card.hidden = true;
    });
  });
  if (new URLSearchParams(location.search).get("share") === "none") {
    const flash = document.getElementById("flash");
    if (flash) flash.textContent = t("Sharing to Handoff needs the app to be installed on this phone. Choose the photos here instead.");
  }
  void form;
}
