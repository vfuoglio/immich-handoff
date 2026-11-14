// The queue's memory: the browser's own database (IndexedDB), so a file waits through a closed tab, a restarted phone and
// a night without signal. It works in the page and in the service worker alike. The files themselves are kept as they
// were chosen (a File is stored by reference to its bytes, not copied into the page's memory).

import type { Item } from "./send-core.ts";
import type { Store } from "./send-queue.ts";

/** The browser has no room left to keep a file waiting. */
export class StorageFull extends Error {
  constructor() {
    super("the browser has no room left to keep this file");
  }
}

const NAME = "handoff-send";

function open(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const r = indexedDB.open(NAME, 1);
    r.onupgradeneeded = () => {
      const db = r.result;
      db.createObjectStore("items", { keyPath: "id" });
      db.createObjectStore("files", { keyPath: "id" });
      db.createObjectStore("meta", { keyPath: "key" });
    };
    r.onsuccess = () => resolve(r.result);
    r.onerror = () => reject(r.error ?? new Error("no database"));
  });
}

/** Runs `work` in one transaction and resolves when it is committed. A full disk is a StorageFull, not a mystery. */
async function within<T>(stores: string[], mode: IDBTransactionMode, work: (tx: IDBTransaction) => IDBRequest<T> | void): Promise<T | undefined> {
  const db = await open();
  try {
    return await new Promise<T | undefined>((resolve, reject) => {
      const tx = db.transaction(stores, mode);
      const r = work(tx);
      tx.oncomplete = () => resolve(r ? r.result : undefined);
      tx.onabort = () => reject(tx.error?.name === "QuotaExceededError" ? new StorageFull() : (tx.error ?? new Error("the database stopped")));
      tx.onerror = () => undefined; // the abort says why
    });
  } finally {
    db.close();
  }
}

export class IdbStore implements Store {
  async put(item: Item, file?: Blob): Promise<void> {
    await within(["items", "files"], "readwrite", (tx) => {
      tx.objectStore("items").put(item);
      if (file) tx.objectStore("files").put({ id: item.id, blob: file });
    });
  }
  async file(id: string): Promise<Blob | undefined> {
    const row = (await within(["files"], "readonly", (tx) => tx.objectStore("files").get(id))) as { blob?: Blob } | undefined;
    return row?.blob;
  }
  async all(): Promise<Item[]> {
    return ((await within(["items"], "readonly", (tx) => tx.objectStore("items").getAll())) as Item[] | undefined) ?? [];
  }
  async release(id: string): Promise<void> {
    await within(["files"], "readwrite", (tx) => {
      tx.objectStore("files").delete(id);
    });
  }
  async remove(id: string): Promise<void> {
    await within(["items", "files"], "readwrite", (tx) => {
      tx.objectStore("items").delete(id);
      tx.objectStore("files").delete(id);
    });
  }
}

/** Whether this browser can keep a queue past the page at all (a private window or an old browser may not). */
export async function canStore(): Promise<boolean> {
  try {
    if (typeof indexedDB === "undefined") return false;
    (await open()).close();
    return true;
  } catch {
    return false;
  }
}

/** A small setting the worker also needs to know (it cannot read the page's localStorage). */
export async function getPref<T>(key: string, fallback: T): Promise<T> {
  try {
    const row = (await within(["meta"], "readonly", (tx) => tx.objectStore("meta").get(key))) as { value?: T } | undefined;
    return row?.value ?? fallback;
  } catch {
    return fallback;
  }
}

export async function setPref(key: string, value: unknown): Promise<void> {
  try {
    await within(["meta"], "readwrite", (tx) => {
      tx.objectStore("meta").put({ key, value });
    });
  } catch {
    /* a setting that cannot be kept is just not kept */
  }
}
