// The page's scripts. Written in TypeScript and bundled into static/app.js by `go generate ./...`
// (esbuild); the bundle is committed, so building the program needs nothing else. `tsc --noEmit` checks
// the types in CI (tools/typecheck). The Content-Security-Policy allows scripts from the app itself only.

import { initCard, initDrop, initGuestBook, initShare } from "./drop.ts";
import { initWall } from "./wall.ts";
import { initBackup } from "./backup";
import { loadCatalog, t } from "./i18n";
import { parseLive } from "./live";
import { initScene } from "./scene";
import { enhance } from "./odometer";
import { initEffects } from "./effects";
import { initLanguage } from "./lang";
import { initPalette } from "./palette";
import { initRows } from "./rows";
import { initSound } from "./sound";
import { initTabs } from "./tabs";

// htmx drops 4xx/5xx bodies by default; our handlers send a short human message, so show it.
document.addEventListener("htmx:beforeSwap", (e) => {
  if (e.detail.xhr.status >= 400 && e.detail.xhr.status < 600) {
    e.detail.shouldSwap = true;
    e.detail.isError = false;
  }
});

// Questions (hx-confirm) are asked in the page's own dialog, in its own style and words, not the browser's. A button
// can set the title (data-confirm-title), the label of the button that goes ahead (data-confirm-label) and mark the
// action as destructive (data-danger).
document.addEventListener("htmx:confirm", (e) => {
  const { question, elt, issueRequest } = e.detail;
  const dialog = document.getElementById("confirm");
  if (!question || !(dialog instanceof HTMLDialogElement)) return; // no question: go ahead; no dialog: the browser asks
  e.preventDefault();
  const title = document.getElementById("confirm-title");
  const text = document.getElementById("confirm-text");
  const ok = document.getElementById("confirm-ok");
  if (!title || !text || !ok) return;
  const data = elt instanceof HTMLElement ? elt.dataset : {};
  title.textContent = data["confirmTitle"] ?? t("Please confirm");
  text.textContent = question;
  ok.textContent = data["confirmLabel"] ?? t("Continue");
  ok.classList.toggle("danger", data["danger"] === "true");
  ok.classList.toggle("primary", data["danger"] !== "true");
  dialog.returnValue = "cancel";
  dialog.addEventListener("close", () => { if (dialog.returnValue === "ok") issueRequest(true); }, { once: true });
  dialog.showModal();
});

// Switching Tags on from the notice unlocks the rest of the page, which was dimmed and inert until then.
document.addEventListener("tags-on", () => {
  const gated = document.getElementById("gated");
  if (!gated) return;
  gated.classList.remove("gated");
  gated.removeAttribute("inert");
  gated.removeAttribute("aria-disabled");
});

// Live status: the server sends a bare "run" signal when a pass finishes; we ask htmx to re-fetch the card.
(function liveStatus(): void {
  const indicator = document.getElementById("live");
  if (!window.EventSource) return;
  const events = new EventSource("events");
  events.addEventListener("run", () => htmx.trigger(document.body, "handoff:run"));
  // what is happening, as it happens: handed on to whoever listens for "handoff:live" (a malformed event is ignored)
  events.addEventListener("live", (message) => {
    const event = parseLive((message as MessageEvent<string>).data);
    if (event) document.dispatchEvent(new CustomEvent("handoff:live", { detail: event }));
  });
  const stream = (up: boolean): void => {
    indicator?.classList.toggle("off", !up);
    document.dispatchEvent(new CustomEvent("handoff:stream", { detail: { up } }));
  };
  events.onopen = () => stream(true);
  events.onerror = () => stream(false);
})();

// The live scene at the bottom of the Overview, when the page has one.
document.addEventListener("DOMContentLoaded", () => {
  loadCatalog();
  initLanguage();
  initTabs();
  initRows();
  initBackup();
  initEffects();
  initDrop();
  initCard();
  initGuestBook();
  initShare();
  initWall();
  initPalette();
  initSound();
  enhance(document); // numbers that are already on the page roll up to their value
  const scene = document.getElementById("scene");
  if (scene) initScene(scene);
});

// numbers swapped in beside the main target (the number on a service's line) roll too
document.addEventListener("htmx:oobAfterSwap", (e) => {
  if (e.target instanceof Element) enhance(e.target);
});

// Invitation links: show the full address (it depends on where this page is served) and let people copy it.
function fillInvites(scope: ParentNode): void {
  scope.querySelectorAll<HTMLInputElement>("input.invite[data-invite]").forEach((input) => {
    input.value = new URL("invite/" + input.dataset.invite, document.baseURI).href;
  });
  scope.querySelectorAll<HTMLInputElement>("input.invite[data-link]").forEach((input) => {
    input.value = new URL(input.dataset.link ?? "./", document.baseURI).href;
  });
}
document.addEventListener("DOMContentLoaded", () => fillInvites(document));
document.addEventListener("htmx:afterSwap", (e) => {
  const target = e.target;
  if (target instanceof Element) {
    fillInvites(target);
    enhance(target); // a refreshed number rolls from what it showed before
  }
});
document.addEventListener("click", (e) => {
  const target = e.target;
  if (!(target instanceof Element)) return;
  const button = target.closest<HTMLButtonElement>("button.copy");
  if (!button) return;
  const input = button.parentElement?.querySelector<HTMLInputElement>("input.invite");
  if (input && navigator.clipboard) {
    void navigator.clipboard.writeText(input.value).then(() => {
      button.textContent = t("Copied");
    });
  }
});

// "Copy a message": the link inside a short, ready-written invitation, in the language of the page.
document.addEventListener("click", (e) => {
  const button = e.target instanceof Element ? e.target.closest<HTMLButtonElement>("button.copy-message") : null;
  if (!button) return;
  const input = document.getElementById(button.dataset.for ?? "");
  if (!(input instanceof HTMLInputElement) || !navigator.clipboard) return;
  const message = (button.dataset.message ?? "{link}").replace("{link}", input.value);
  void navigator.clipboard.writeText(message).then(() => {
    button.textContent = t("Copied");
  });
});

// "QR code": the app draws it (so a phone can scan it from the screen); the page shows it, or hides it again.
document.addEventListener("click", (e) => {
  const button = e.target instanceof Element ? e.target.closest<HTMLButtonElement>("button.show-qr") : null;
  if (!button) return;
  const input = document.getElementById(button.dataset.for ?? "");
  const box = document.getElementById("qr-" + (button.dataset.for ?? ""));
  if (!(input instanceof HTMLInputElement) || !box) return;
  if (!box.hidden) {
    box.hidden = true;
    return;
  }
  void fetch("qr", {
    method: "POST",
    headers: { "X-Requested-With": "immich-handoff", "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ text: input.value }),
  })
    .then((r) => (r.ok ? r.text() : Promise.reject(new Error(String(r.status)))))
    .then((svg) => {
      const img = document.createElement("img");
      img.alt = button.dataset.alt ?? "";
      img.width = img.height = 220;
      img.src = "data:image/svg+xml;charset=utf-8," + encodeURIComponent(svg);
      box.replaceChildren(img);
      box.hidden = false;
    })
    .catch(() => {
      box.textContent = t("The QR code could not be made.");
      box.hidden = false;
    });
});

// makes this file a module of its own, so names in one script never clash with another
export {};
