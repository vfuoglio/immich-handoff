// The tabs in the bar: Overview, Services, Activity and Settings (when something is switched on) are views of one page. The address's #fragment says which is
// showing (so a view can be bookmarked, and the page reloads onto the same one after an action), theme.ts sets it before the
// first paint, and this switches it. Where the browser can, the change is a smooth cross-fade (View Transitions), unless
// reduced motion is asked for.

const VIEWS = ["overview", "services", "activity", "settings"];

function current(): string {
  const hash = location.hash.slice(1);
  // a view whose tab is not there (Settings, when nothing is switched on) is not one to show
  return VIEWS.includes(hash) && document.querySelector(`nav.tabs a[data-view="${hash}"]`) ? hash : "overview";
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
  // the pill is centred and the buttons are on the right: when the labels of a language (German has the longest) would reach the first
  // of them, the pill shows icons only, whatever the width of the window (the style sheet does the same below 1181 pixels for every language)
  const fit = (): void => {
    if (!nav) return;
    delete nav.dataset["compact"];
    const first = document.querySelector<HTMLElement>("header.bar > :not(.brand)");
    if (!first || matchMedia("(max-width: 900px)").matches) return; // a phone has a bar at the bottom
    if (nav.getBoundingClientRect().right + 12 > first.getBoundingClientRect().left) nav.dataset["compact"] = "1";
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

  fit();
  show(current());
  addEventListener("resize", () => { fit(); place(); });
  document.fonts?.ready.then(() => { fit(); place(); }); // the labels change width when the font arrives
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
