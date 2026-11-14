// What the page and the service worker share to send in the background: taking the line (only one of them sends at a time) and
// telling the person when it is over. Background Sync is what wakes the worker with the screen off; the browsers that have it
// are the Chromium ones (Android Chrome); on others the queue goes only while the page is open, and the page says so.

import { plan, lines, isTell, type Tell, type Texts } from "./send-notify.ts";
import { getPref, setPref } from "./send-store.ts";
import type { Store } from "./send-queue.ts";

export const SYNC_TAG = "handoff-send";

interface Locks {
  request(name: string, options: { ifAvailable: boolean }, work: (lock: object | null) => Promise<void>): Promise<void>;
}

/** Runs `work` if nobody else (the page, or the worker) is sending, and says whether it ran. Browsers without Web Locks always run. */
export async function withLock(work: () => Promise<void>): Promise<boolean> {
  const locks = (globalThis.navigator as unknown as { locks?: Locks } | undefined)?.locks;
  if (!locks) {
    await work();
    return true;
  }
  let ran = false;
  await locks.request(SYNC_TAG, { ifAvailable: true }, async (lock) => {
    if (!lock) return;
    ran = true;
    await work();
  });
  return ran;
}

/** The registration the message is shown through (the page's and the worker's both have one). */
export interface Shower {
  showNotification(title: string, options: Record<string, unknown>): Promise<void>;
  scope: string;
}

/** After a run: say how it went, once, if the person wants it and the queue is empty. `quiet` (the page is in front of the person)
 *  only marks what ended as seen, so a later message does not repeat it. */
export async function announce(store: Store, reg: Shower, now: number, quiet: boolean): Promise<void> {
  const raw = await getPref<unknown>("sendTell", "all");
  const tell: Tell = isTell(raw) ? raw : "all";
  const told = await getPref<number>("sendTold", 0);
  const counts = plan(await store.all(), told, tell);
  if (!counts) {
    // nothing to say (or nothing more to wait for): what ended is now seen
    if (plan(await store.all(), told, "all") !== null || quiet) await setPref("sendTold", now);
    return;
  }
  await setPref("sendTold", now);
  if (quiet || typeof Notification === "undefined" || Notification.permission !== "granted") return;
  const texts = await getPref<Texts | null>("sendTexts", null);
  if (!texts) return;
  const body = lines(texts, counts).join("\n");
  if (body === "") return;
  await reg.showNotification(texts.title, {
    body,
    tag: "handoff-send", // one message for the whole batch, replaced by the next, never a stack of them
    icon: new URL("icon-192.png", reg.scope).href,
    data: { url: "send", urls: {} },
  });
}
