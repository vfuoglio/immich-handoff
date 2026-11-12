// The service worker. It exists for one thing: to show a notification when the server sends a push, and to open the page
// when the notification is clicked. It has no fetch handler and keeps no cache, so it changes nothing about how the page
// loads. Built into static/sw.js by `go generate ./...` (esbuild).

import { appRoot, readPush, targetUrl } from "./pushmsg.ts";

// The few parts of the worker's scope this file uses, so the page's type check (which has the browser's types, not the
// worker's) can still check it.
interface ClientLike {
  url: string;
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
