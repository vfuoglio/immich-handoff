// What the service worker does with a push, kept apart from the worker so it can be tested: reading the message the
// server sent, and finding the page a click should open. Nothing here touches the browser.

export interface PushAction {
  action: string;
  title: string;
}

export interface Shown {
  title: string;
  body: string;
  tag: string;
  url: string;
  /** Buttons for the notification (a browser that cannot show them shows none), and where each one leads. */
  actions: PushAction[];
  urls: Record<string, string>;
}

/** Reads the JSON the server put in a push; anything else is shown as a plain notification, never as nothing. */
export function readPush(raw: unknown): Shown {
  const o = typeof raw === "object" && raw !== null ? (raw as Record<string, unknown>) : {};
  const text = (v: unknown, fallback: string): string => (typeof v === "string" && v !== "" ? v : fallback);
  const actions: PushAction[] = [];
  if (Array.isArray(o["actions"])) {
    for (const a of o["actions"].slice(0, 2)) { // a notification shows two buttons at most
      const r = typeof a === "object" && a !== null ? (a as Record<string, unknown>) : {};
      if (typeof r["action"] === "string" && r["action"] !== "" && typeof r["title"] === "string" && r["title"] !== "") actions.push({ action: r["action"], title: r["title"] });
    }
  }
  const urls: Record<string, string> = {};
  if (typeof o["urls"] === "object" && o["urls"] !== null) {
    for (const [k, v] of Object.entries(o["urls"] as Record<string, unknown>)) if (typeof v === "string") urls[k] = v;
  }
  return { title: text(o["title"], "Immich Handoff"), body: text(o["body"], ""), tag: text(o["tag"], "handoff"), url: text(o["url"], "./"), actions: actions.filter((a) => a.action in urls), urls };
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
