// "Notifications on this device": turning pushes on for this browser, and the list of the person's devices. The server
// does the sending (internal/push); this script only asks the browser for permission, makes the subscription and tells
// the server about it. Nothing is cached and nothing is sent anywhere but to the app itself.

import { t } from "./i18n.ts";

export type Support = "ok" | "insecure" | "install" | "unsupported";

export interface Env {
  secure: boolean;
  serviceWorker: boolean;
  pushManager: boolean;
  notification: boolean;
  ios: boolean;
  standalone: boolean;
}

/** Whether this browser can be sent pushes, and if not, why. */
export function support(e: Env): Support {
  if (!e.secure) return "insecure";
  if (e.ios && !e.standalone) return "install"; // on an iPhone or iPad pushes work only for the page added to the Home Screen
  if (!e.serviceWorker || !e.pushManager || !e.notification) return "unsupported";
  return "ok";
}

/** The browser's way of writing a key (base64url) as bytes, for subscribing. */
export function urlBase64ToBytes(s: string): Uint8Array {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (s.length % 4)) % 4);
  const raw = atob(b64);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

/** The id the server gives a device: a hash of its address, so the page can tell which one is "this device" without
 *  the address ever being shown. */
export async function deviceId(endpoint: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(endpoint));
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, "0")).join("");
}

function say(text: string): void {
  const flash = document.getElementById("flash");
  if (flash) flash.textContent = text;
}

const HEADERS = { "X-Requested-With": "immich-handoff" };

async function post(path: string, form: Record<string, string>): Promise<boolean> {
  const response = await fetch(path, { method: "POST", headers: { ...HEADERS, "Content-Type": "application/x-www-form-urlencoded" }, body: new URLSearchParams(form), credentials: "same-origin" });
  if (!response.ok) {
    say((await response.text()).trim() || t("Could not turn on notifications."));
    return false;
  }
  return true;
}

/** Waits until a freshly registered worker is running. (`navigator.serviceWorker.ready` would wait for a worker that
 *  controls this page, and this one is only for pushes, so it never does.) */
function whenActive(reg: ServiceWorkerRegistration): Promise<ServiceWorkerRegistration> {
  if (reg.active) return Promise.resolve(reg);
  const worker = reg.installing ?? reg.waiting;
  if (!worker) return Promise.reject(new Error("no worker"));
  return new Promise((resolve, reject) => {
    worker.addEventListener("statechange", () => {
      if (worker.state === "activated") resolve(reg);
      else if (worker.state === "redundant") reject(new Error("redundant"));
    });
  });
}

async function register(): Promise<ServiceWorkerRegistration> {
  return whenActive(await navigator.serviceWorker.register("static/sw.js", { scope: "static/" }));
}

async function turnOn(card: HTMLElement): Promise<void> {
  const key = card.dataset["key"] ?? "";
  const mode = card.querySelector<HTMLInputElement>("input[name=pushmode]:checked")?.value ?? "all";
  if ((await Notification.requestPermission()) !== "granted") { // asked first thing, while the click still counts
    say(t("Notifications are blocked for this site. Allow them in your browser's settings for this page, then try again."));
    return;
  }
  try {
    const reg = await register();
    const sub = (await reg.pushManager.getSubscription()) ?? (await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlBase64ToBytes(key) as BufferSource }));
    const json = sub.toJSON();
    const response = await fetch("me/push", {
      method: "POST",
      headers: { ...HEADERS, "Content-Type": "application/json" },
      body: JSON.stringify({ endpoint: json.endpoint, keys: json.keys, mode }),
      credentials: "same-origin",
    });
    if (!response.ok) {
      say((await response.text()).trim() || t("Could not turn on notifications."));
      return;
    }
    location.reload();
  } catch {
    say(t("Could not turn on notifications."));
  }
}

async function thisDevice(): Promise<{ id: string; sub: PushSubscription } | null> {
  const reg = await navigator.serviceWorker.getRegistration("static/");
  const sub = await reg?.pushManager.getSubscription();
  return sub ? { id: await deviceId(sub.endpoint), sub } : null;
}

/** Wires the card on the Overview, if there is one. */
export function initPush(): void {
  const card = document.getElementById("pushcard");
  if (!card) return;
  const note = card.querySelector<HTMLElement>("[data-push-note]");
  const controls = card.querySelector<HTMLElement>("[data-push-controls]");
  const standalone = window.matchMedia("(display-mode: standalone)").matches || (navigator as { standalone?: boolean }).standalone === true;
  const state = support({
    secure: window.isSecureContext,
    serviceWorker: "serviceWorker" in navigator,
    pushManager: "PushManager" in window,
    notification: "Notification" in window,
    ios: /iPhone|iPad|iPod/.test(navigator.userAgent),
    standalone,
  });
  const why: Record<Support, string> = {
    ok: "",
    insecure: t("Notifications need this page to be opened over HTTPS."),
    install: t("On an iPhone or iPad, add this page to the Home Screen first, then open it from there."),
    unsupported: t("This browser cannot show notifications from this page."),
  };
  if (state !== "ok") {
    if (note) { note.textContent = why[state]; note.hidden = false; }
    return;
  }
  if (controls) controls.hidden = false;

  void thisDevice().then((here) => {
    if (!here) return;
    card.querySelectorAll<HTMLElement>(`[data-device="${here.id}"]`).forEach((row) => {
      row.dataset["here"] = "true";
      row.querySelector<HTMLElement>("[data-here-mark]")?.removeAttribute("hidden");
    });
    if (card.querySelector(`[data-device="${here.id}"]`)) card.querySelector<HTMLElement>("[data-push-on]")?.setAttribute("hidden", "");
  });

  card.addEventListener("click", (e) => {
    const target = e.target instanceof Element ? e.target : null;
    const button = target?.closest<HTMLButtonElement>("button");
    if (!button) return;
    const row = button.closest<HTMLElement>("[data-device]");
    const id = row?.dataset["device"] ?? "";
    if (button.hasAttribute("data-push-on")) {
      void turnOn(card);
    } else if (button.hasAttribute("data-push-test")) {
      button.disabled = true;
      void post("me/push/test", { id }).then((ok) => { button.disabled = false; if (ok) say(t("Notifications work on this device.")); });
    } else if (button.hasAttribute("data-push-remove")) {
      button.disabled = true;
      void (async () => {
        if (row?.dataset["here"] === "true") (await thisDevice())?.sub.unsubscribe().catch(() => false);
        if (await post("me/push/remove", { id })) location.reload();
        else button.disabled = false;
      })();
    }
  });
  card.addEventListener("change", (e) => {
    const select = e.target instanceof HTMLSelectElement && e.target.hasAttribute("data-push-mode") ? e.target : null;
    const id = select?.closest<HTMLElement>("[data-device]")?.dataset["device"];
    if (select && id) void post("me/push/mode", { id, mode: select.value });
  });
}
