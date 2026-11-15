// Small things the scripts that run in Immich's own pages share.

/**
 * Adds a stylesheet to the page. A constructed stylesheet is not held back by a page's content security policy, a
 * <style> element can be, so that is what is used when the browser has it.
 */
export function addStyles(css: string): void {
  if ("adoptedStyleSheets" in document && "replaceSync" in CSSStyleSheet.prototype) {
    const sheet = new CSSStyleSheet();
    sheet.replaceSync(css);
    document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
  } else {
    const style = document.createElement("style");
    style.textContent = css;
    document.head.append(style);
  }
}

/** Runs `work` on the next frame, however many times it is asked for before then. */
export function oncePerFrame(work: () => void): () => void {
  let queued = false;
  return () => {
    if (queued) return;
    queued = true;
    requestAnimationFrame(() => {
      queued = false;
      work();
    });
  };
}
