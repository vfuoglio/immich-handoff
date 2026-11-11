// What the service worker does with a push, kept apart from the worker so it can be tested: reading the message the
// server sent, and finding the page a click should open. Nothing here touches the browser.

export interface Shown {
  title: string;
  body: string;
  tag: string;
  url: string;
}

/** Reads the JSON the server put in a push; anything else is shown as a plain notification, never as nothing. */
export function readPush(raw: unknown): Shown {
  const o = typeof raw === "object" && raw !== null ? (raw as Record<string, unknown>) : {};
  const text = (v: unknown, fallback: string): string => (typeof v === "string" && v !== "" ? v : fallback);
  return { title: text(o["title"], "Immich Handoff"), body: text(o["body"], ""), tag: text(o["tag"], "handoff"), url: text(o["url"], "./") };
}

/** The address of the app's front page, from the address the worker was registered for (its folder, `static/`). */
export function appRoot(scope: string): string {
  return new URL("../", scope).href;
}

/** Where a click goes: the address in the push, relative to the app, and never to another site. */
export function targetUrl(scope: string, url: string): string {
  const root = appRoot(scope);
  try {
    const target = new URL(url, root);
    return target.origin === new URL(root).origin ? target.href : root;
  } catch {
    return root;
  }
}
