// A link to this app in Immich's own header, between the notification bell and the person's avatar.
//
// This script does not run in this app's pages. A reverse proxy in front of Immich adds one <script> tag to Immich's
// pages (see docs/find-handoff.md) and it runs there. It finds its own address from that tag, so the link and the
// icon need no setting: the app's page is where the script came from, and the icon is the app's own.
//
// Immich offers no place for a link, so this looks for the avatar by what is true in every language (its title holds
// the person's email) and puts the link in front of it. If the header cannot be found, because Immich changed, a round
// button floats at the bottom right instead, so the link is never lost. Nothing else of Immich's page is touched.

const script = document.currentScript;
const source = script instanceof HTMLScriptElement ? script.src : "";

if (source) {
  const page = new URL("../", source).href; // .../static/immich-button.js -> the app's page
  const icon = new URL("favicon.svg", source).href;
  const started = Date.now();

  const css = `
#handoff-link{display:flex;flex:none;align-items:center;justify-content:center;width:40px;height:40px;border-radius:9999px;color:inherit}
#handoff-link img{display:block;width:28px;height:28px;border-radius:7px}
#handoff-link:hover{background:rgb(127 127 127 / .2)}
#handoff-link:focus-visible{outline:2px solid currentColor;outline-offset:2px}
#handoff-link.floating{position:fixed;right:16px;bottom:16px;z-index:2147483000;width:48px;height:48px;background:#241a66;box-shadow:0 4px 14px rgb(0 0 0 / .35)}
#handoff-link.floating img{width:32px;height:32px}`;
  // a constructed stylesheet is not held back by a page's content security policy, a <style> element can be
  if ("adoptedStyleSheets" in document && "replaceSync" in CSSStyleSheet.prototype) {
    const sheet = new CSSStyleSheet();
    sheet.replaceSync(css);
    document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
  } else {
    const style = document.createElement("style");
    style.textContent = css;
    document.head.append(style);
  }

  const link = document.createElement("a");
  link.id = "handoff-link";
  link.href = page;
  link.title = "Immich Handoff";
  link.setAttribute("aria-label", "Immich Handoff");
  const image = document.createElement("img");
  image.src = icon;
  image.alt = "";
  link.append(image);

  /** The avatar's own slot in the right-hand group of the header, and the group. */
  const spot = (): { row: Element; avatar: Element } | null => {
    const avatar = document.querySelector("[title*='@']")?.closest("section > *");
    const row = avatar?.parentElement;
    return avatar && row ? { row, avatar } : null;
  };

  const place = (): void => {
    const found = spot();
    if (found) {
      link.classList.remove("floating");
      if (link.parentElement !== found.row || link.nextElementSibling !== found.avatar) found.row.insertBefore(link, found.avatar);
    } else if (Date.now() - started > 5000 && !link.classList.contains("floating")) {
      link.classList.add("floating");
      document.body.append(link);
    }
  };

  // Immich draws its pages as you move around, so the header can be replaced: put the link back, once per frame at most
  let queued = false;
  const schedule = (): void => {
    if (queued) return;
    queued = true;
    requestAnimationFrame(() => {
      queued = false;
      place();
    });
  };
  new MutationObserver(schedule).observe(document.body, { childList: true, subtree: true });
  schedule();
  setTimeout(schedule, 5100);
}

// makes this file a module of its own, so names in it never clash with another script
export {};
