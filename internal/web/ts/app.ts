// The page's scripts. Written in TypeScript and bundled into static/app.js by `go generate ./...`
// (esbuild); the bundle is committed, so building the program needs nothing else. `tsc --noEmit` checks
// the types in CI (tools/typecheck). The Content-Security-Policy allows scripts from the app itself only.

// htmx drops 4xx/5xx bodies by default; our handlers send a short human message, so show it.
document.addEventListener("htmx:beforeSwap", (e) => {
  if (e.detail.xhr.status >= 400 && e.detail.xhr.status < 600) {
    e.detail.shouldSwap = true;
    e.detail.isError = false;
  }
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
  events.onopen = () => indicator?.classList.remove("off");
  events.onerror = () => indicator?.classList.add("off");
})();

// Invitation links: show the full address (it depends on where this page is served) and let people copy it.
function fillInvites(scope: ParentNode): void {
  scope.querySelectorAll<HTMLInputElement>("input.invite[data-invite]").forEach((input) => {
    input.value = new URL("invite/" + input.dataset.invite, document.baseURI).href;
  });
}
document.addEventListener("DOMContentLoaded", () => fillInvites(document));
document.addEventListener("htmx:afterSwap", (e) => {
  const target = e.target;
  if (target instanceof Element) fillInvites(target);
});
document.addEventListener("click", (e) => {
  const target = e.target;
  if (!(target instanceof Element)) return;
  const button = target.closest<HTMLButtonElement>("button.copy");
  if (!button) return;
  const input = button.parentElement?.querySelector<HTMLInputElement>("input.invite");
  if (input && navigator.clipboard) {
    void navigator.clipboard.writeText(input.value).then(() => {
      button.textContent = "Copied";
    });
  }
});
