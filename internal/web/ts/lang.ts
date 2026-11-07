// The language menu in the bar. Choosing a language remembers it in a cookie (the server reads it on the next request)
// and reloads the page. The cookie is scoped to the app's own address, found from the page's base, so it also works when
// the app is served under a path prefix and does not leak to other apps on the same host.

const COOKIE = "handoff-lang";

function appPath(): string {
  return new URL(document.baseURI).pathname.replace(/[^/]*$/, "");
}

export function initLanguage(): void {
  const menu = document.querySelector<HTMLDetailsElement>("details.lang");
  if (!menu) return;
  menu.addEventListener("click", (e) => {
    const target = e.target instanceof Element ? e.target.closest<HTMLButtonElement>("button[data-lang]") : null;
    if (!target) return;
    const secure = location.protocol === "https:" ? "; secure" : "";
    document.cookie = `${COOKIE}=${target.dataset["lang"] ?? "en"}; path=${appPath()}; max-age=31536000; samesite=lax${secure}`;
    location.reload();
  });
  // a click anywhere else, or Escape, closes the menu
  document.addEventListener("click", (e) => {
    if (menu.open && e.target instanceof Node && !menu.contains(e.target)) menu.open = false;
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && menu.open) {
      menu.open = false;
      menu.querySelector("summary")?.focus();
    }
  });
}
