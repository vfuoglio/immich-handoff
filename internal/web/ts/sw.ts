// The service worker. It shows a notification when the server sends a push and opens the page when it is clicked; it receives
// the photos and videos that the phone's Share menu hands to the installed app (a POST to static/share), keeping them in the
// browser's database for the Send page to pick up; and, when the browser wakes it for Background Sync, it finishes the queue of
// photos being sent with the screen off and says when it is done. Its only fetch
// handler is for that one address: it keeps no cache and changes nothing about how the page loads. Built into static/sw.js by
// `go generate ./...` (esbuild).

import { appRoot, readPush, targetUrl } from "./pushmsg.ts";
import { announce, SYNC_TAG, withLock } from "./send-bg.ts";
import { heldForWifi, newItem, sharedMedia, type Connection } from "./send-core.ts";
import { Queue } from "./send-queue.ts";
import { getPref, IdbStore } from "./send-store.ts";

// The few parts of the worker's scope this file uses, so the page's type check (which has the browser's types, not the
// worker's) can still check it.
interface ClientLike {
  url: string;
  visibilityState: string;
  focus(): Promise<unknown>;
}
interface PushEventLike {
  data: { json(): unknown } | null;
  waitUntil(work: Promise<unknown>): void;
}
interface ClickEventLike {
  action: string;
  notification: { close(): void; data: unknown };
  waitUntil(work: Promise<unknown>): void;
}
interface FetchEventLike {
  request: Request;
  respondWith(response: Promise<Response>): void;
}
interface SyncEventLike {
  tag: string;
  waitUntil(work: Promise<unknown>): void;
}
interface WorkerScope {
  registration: { scope: string; showNotification(title: string, options: Record<string, unknown>): Promise<void> };
  clients: {
    claim(): Promise<void>;
    matchAll(options: { type: "window"; includeUncontrolled: boolean }): Promise<ClientLike[]>;
    openWindow(url: string): Promise<unknown>;
  };
  skipWaiting(): Promise<void>;
  addEventListener(type: string, listener: (event: never) => void): void;
}
const worker = self as unknown as WorkerScope;

worker.addEventListener("install", () => {
  void worker.skipWaiting();
});
worker.addEventListener("activate", ((event: { waitUntil(work: Promise<unknown>): void }) => {
  event.waitUntil(worker.clients.claim());
}) as (event: never) => void);

worker.addEventListener("push", ((event: PushEventLike) => {
  let raw: unknown = null;
  try {
    raw = event.data ? event.data.json() : null;
  } catch {
    raw = null; // not JSON: shown as the plain notification
  }
  const shown = readPush(raw);
  event.waitUntil(
    worker.registration.showNotification(shown.title, {
      body: shown.body,
      tag: shown.tag,
      renotify: true,
      icon: new URL("icon-192.png", worker.registration.scope).href,
      actions: shown.actions,
      data: { url: shown.url, urls: shown.urls },
    }),
  );
}) as (event: never) => void);

worker.addEventListener("notificationclick", ((event: ClickEventLike) => {
  event.notification.close();
  const data = event.notification.data as { url?: unknown; urls?: Record<string, unknown> } | null;
  const button = event.action !== "" ? data?.urls?.[event.action] : undefined; // a button leads to its own page, a tap to the app
  const wanted = typeof button === "string" ? button : typeof data?.url === "string" ? data.url : "./";
  const target = targetUrl(worker.registration.scope, wanted);
  const root = appRoot(worker.registration.scope);
  event.waitUntil(
    worker.clients.matchAll({ type: "window", includeUncontrolled: true }).then((open) => {
      const mine = open.find((c) => c.url.startsWith(root));
      return mine ? mine.focus() : worker.clients.openWindow(target);
    }),
  );
}) as (event: never) => void);

/** The photos and videos of a share: kept (not yet sent: their person has to choose where they go), then the Send page. */
async function receiveShare(request: Request): Promise<Response> {
  const root = appRoot(worker.registration.scope);
  try {
    const form = await request.formData();
    const store = new IdbStore();
    for (const f of sharedMedia(form.getAll("media"))) {
      const item = newItem(crypto.randomUUID(), f.name || "shared", f.size, f.lastModified, "", "", Date.now());
      item.state = "pick";
      try {
        await store.put(item, f);
      } catch {
        /* no room: the page says so when it opens (it will find fewer files than were shared) */
      }
    }
  } catch {
    /* a share that cannot be read opens the page with nothing in it */
  }
  return Response.redirect(new URL("send?shared=1", root).href, 303);
}

worker.addEventListener("fetch", ((event: FetchEventLike) => {
  const shareAt = new URL("share", worker.registration.scope).href;
  if (event.request.method === "POST" && event.request.url === shareAt) event.respondWith(receiveShare(event.request));
}) as (event: never) => void);

/** Sends what waits, with the screen off. A network that is gone again ends the run with an error, which tells the browser to wake
 *  the worker once more later; whoever else is sending (the page, in front of the person) is left to it. */
async function background(): Promise<void> {
  const store = new IdbStore();
  const wifi = await getPref<boolean>("wifiOnly", false);
  const queue = new Queue(
    store,
    {
      base: appRoot(worker.registration.scope),
      owner: "", // the worker cannot tell who is signed in; the server refuses a file named for another person
      held: () => heldForWifi(wifi, (self.navigator as unknown as { connection?: Connection }).connection),
      now: () => Date.now(),
      newId: () => crypto.randomUUID(),
      pause: () => Promise.reject(new Error("offline")),
      exclusive: withLock,
    },
    () => undefined,
  );
  try {
    await queue.resume();
  } finally {
    const seen = (await worker.clients.matchAll({ type: "window", includeUncontrolled: true })).some((c) => c.visibilityState === "visible");
    await announce(store, { showNotification: (title, options) => worker.registration.showNotification(title, options), scope: worker.registration.scope }, Date.now(), seen);
  }
}

worker.addEventListener("sync", ((event: SyncEventLike) => {
  if (event.tag === SYNC_TAG) event.waitUntil(background());
}) as (event: never) => void);
