// htmx drops 4xx/5xx bodies by default; our handlers send a short human message, so show it.
document.addEventListener("htmx:beforeSwap", (e) => {
  if (e.detail.xhr.status >= 400 && e.detail.xhr.status < 600) {
    e.detail.shouldSwap = true;
    e.detail.isError = false;
  }
});

// Live status: the server sends a bare "run" signal when a pass finishes; we ask htmx to re-fetch the card.
(function () {
  const indicator = document.getElementById("live");
  if (!window.EventSource) return;
  const es = new EventSource("events");
  es.addEventListener("run", () => htmx.trigger(document.body, "handoff:run"));
  es.onopen = () => indicator && indicator.classList.remove("off");
  es.onerror = () => indicator && indicator.classList.add("off");
})();
