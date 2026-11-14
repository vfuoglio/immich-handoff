// The queue of files on their way. It keeps its list in a store (the browser's own database, see send-store.ts), sends
// one file at a time, and when the network goes away it waits and carries on by itself, from where the server says the
// file stands. The page shows it; the service worker can run it with the screen off (sw.ts).

import { backoff, finished, newItem, nextToSend, type Item } from "./send-core.ts";
import { sendFile } from "./send-run.ts";

export interface Store {
  put(item: Item, file?: Blob): Promise<void>;
  file(id: string): Promise<Blob | undefined>;
  all(): Promise<Item[]>;
  /** Forgets the file's bytes but keeps its record (it is sent). */
  release(id: string): Promise<void>;
  remove(id: string): Promise<void>;
}

/** A store that keeps nothing past this page (the fallback when the browser has no database to give). */
export class MemoryStore implements Store {
  private items = new Map<string, Item>();
  private files = new Map<string, Blob>();
  async put(item: Item, file?: Blob): Promise<void> {
    this.items.set(item.id, { ...item });
    if (file) this.files.set(item.id, file);
  }
  async file(id: string): Promise<Blob | undefined> {
    return this.files.get(id);
  }
  async all(): Promise<Item[]> {
    return [...this.items.values()].map((i) => ({ ...i }));
  }
  async release(id: string): Promise<void> {
    this.files.delete(id);
  }
  async remove(id: string): Promise<void> {
    this.items.delete(id);
    this.files.delete(id);
  }
}

export interface Env {
  base: string;
  now(): number;
  /** Resolves after ms, or at once when the network comes back. */
  pause(ms: number): Promise<void>;
  newId(): string;
}

export class Queue {
  items: Item[] = [];
  private running = false;
  private abort = new Set<string>();

  constructor(
    private store: Store,
    private env: Env,
    private changed: () => void,
  ) {}

  /** Reads what the store holds (a file that was being sent when the page closed goes on again). */
  async load(): Promise<void> {
    this.items = (await this.store.all()).sort((a, b) => a.added - b.added);
    for (const i of this.items) if (i.state === "sending") i.state = "offline";
    this.changed();
  }

  async add(files: File[], service: string): Promise<void> {
    for (const f of files) {
      const item = newItem(this.env.newId(), f.name, f.size, f.lastModified, service, this.env.now());
      this.items.push(item);
      await this.store.put(item, f);
    }
    this.changed();
  }

  private async save(item: Item): Promise<void> {
    await this.store.put(item);
    this.changed();
  }

  async cancel(id: string): Promise<void> {
    const item = this.items.find((i) => i.id === id);
    if (!item || finished(item.state)) return;
    this.abort.add(id);
    item.state = "cancelled";
    if (item.serverId !== "") void fetch(new URL(`send/${item.serverId}/cancel`, this.env.base).href, { method: "POST", headers: { "X-Requested-With": "immich-handoff" } }).catch(() => undefined);
    await this.store.remove(id);
    this.changed();
  }

  /** Puts a file that failed or waited for a sign-in back in the line. */
  async retry(id: string): Promise<void> {
    const item = this.items.find((i) => i.id === id);
    if (!item || (item.state !== "failed" && item.state !== "signin" && item.state !== "offline")) return;
    if (item.state === "failed") {
      item.serverId = "";
      item.sent = 0;
    }
    item.state = "waiting";
    item.text = "";
    item.tries = 0;
    await this.save(item);
  }

  /** Removes what is finished from the list and from the store. */
  async clear(): Promise<void> {
    for (const i of this.items.filter((x) => finished(x.state))) await this.store.remove(i.id);
    this.items = this.items.filter((i) => !finished(i.state));
    this.changed();
  }

  /** Sends what is waiting, one file at a time, until nothing can go on (everything is done, or waits for a sign-in). */
  async run(): Promise<void> {
    if (this.running) return;
    this.running = true;
    try {
      for (;;) {
        const at = nextToSend(this.items);
        if (at < 0) return;
        const item = this.items[at]!;
        const file = await this.store.file(item.id);
        if (!file) {
          item.state = "failed";
          item.text = "the file is not there any more";
          await this.save(item);
          continue;
        }
        item.state = "sending";
        await this.save(item);
        const out = await sendFile(this.env.base, file, item, {
          progress: (sent, serverId) => {
            item.sent = sent;
            item.serverId = serverId;
            this.changed();
          },
          stopped: () => this.abort.has(item.id),
        });
        if (this.abort.has(item.id)) {
          this.abort.delete(item.id);
          continue;
        }
        item.serverId = out.serverId;
        switch (out.kind) {
          case "done":
            item.state = "done";
            item.photo = out.photo;
            item.duplicate = out.duplicate;
            item.sent = item.size;
            await this.store.release(item.id); // the file itself is no longer needed; its record stays to be seen
            break;
          case "failed":
            item.state = "failed";
            item.text = out.text;
            break;
          case "signin":
            item.state = "signin";
            item.sent = out.sent;
            break;
          case "offline":
            item.state = "offline";
            item.sent = out.sent;
            item.tries++;
            break;
        }
        await this.save(item);
        if (item.state === "signin") return; // nothing else will go either
        if (item.state === "offline") await this.env.pause(backoff(item.tries));
      }
    } finally {
      this.running = false;
    }
  }
}
