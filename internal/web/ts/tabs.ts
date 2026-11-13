// The tabs in the bar: Overview, Services and Activity are three views of one page. The address's #fragment says which is
// showing (so a view can be bookmarked, and the page reloads onto the same one after an action), theme.ts sets it before the
// first paint, and this switches it. Where the browser can, the change is a smooth cross-fade (View Transitions), unless
// reduced motion is asked for.

const VIEWS = ["overview", "services", "activity"];

function current(): string {
  const hash = location.hash.slice(1);
  return VIEWS.includes(hash) ? hash : "overview";
}

export function initTabs(): void {
  const tabs = document.querySelectorAll<HTMLAnchorElement>("nav.tabs a[data-view]");
  if (tabs.length === 0) return;

  // the gradient under the showing view: its place and width are handed to the style sheet, which slides it there
  const nav = document.querySelector<HTMLElement>("nav.tabs");
  const place = (): void => {
    const on = document.querySelector<HTMLElement>("nav.tabs a[aria-current]");
    if (!nav || !on) return;
    nav.style.setProperty("--bx", `${on.offsetLeft}px`);
    nav.style.setProperty("--bw", `${on.offsetWidth}px`);
  };
  const show = (view: string): void => {
    document.documentElement.dataset["view"] = view;
    tabs.forEach((tab) => {
      if (tab.dataset["view"] === view) tab.setAttribute("aria-current", "page");
      else tab.removeAttribute("aria-current");
    });
    place();
    window.scrollTo(0, 0);
  };
  const change = (view: string): void => {
    const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;
    if (document.startViewTransition && !reduced) document.startViewTransition(() => show(view));
    else show(view);
  };

  show(current());
  addEventListener("resize", place);
  document.fonts?.ready.then(place); // the labels change width when the font arrives
  tabs.forEach((tab) =>
    tab.addEventListener("click", (e) => {
      e.preventDefault(); // the page has a <base> on some routes, so a bare #fragment link must not navigate
      const view = tab.dataset["view"] ?? "overview";
      if (view === current()) return;
      history.pushState(null, "", `#${view}`);
      change(view);
    }),
  );
  addEventListener("hashchange", () => change(current()));
  addEventListener("popstate", () => change(current()));
}
