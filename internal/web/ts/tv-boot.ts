// The first script a TV loads. It is plain old JavaScript on purpose: a smart TV's browser can be many years old, and a script it cannot
// read would leave a blank screen. It decides which page script to load: the modern one (tv.ts) where the browser has what it uses,
// otherwise the legacy one (tv-legacy.ts), which is written for browsers from about 2016. The modern script says it started; if it
// does not within a few seconds (a browser that looked able and was not), the legacy one is loaded instead.

(function boot(): void {
  var legacy = document.documentElement.getAttribute("data-legacy") === "1";
  var win = window as unknown as { __tvStarted?: boolean };
  var canModern =
    typeof Promise === "function" &&
    typeof fetch === "function" &&
    typeof (String.prototype as unknown as { replaceAll?: unknown }).replaceAll === "function"; // browsers from about 2021 have this, and so can read the modern script
  function load(src: string): void {
    var s = document.createElement("script");
    s.src = src;
    document.head.appendChild(s);
  }
  if (legacy || !canModern) {
    load("static/tv-legacy.js");
    return;
  }
  load("static/tv.js");
  setTimeout(function (): void {
    if (!win.__tvStarted) load("static/tv-legacy.js");
  }, 5000);
})();
