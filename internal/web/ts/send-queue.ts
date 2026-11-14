// The queue of files on their way. It keeps its list in a store (the browser's own database, see send-store.ts), sends
// one file at a time, and when the network goes away it waits and carries on by itself, from where the server says the
// file stands. The page shows it; the service worker can run it with the screen off (sw.ts).

import { backoff, finished, newItem, nextToSend, type Item } from "./send-core.ts";
import { sendFile as realSend } from "./send-run.ts";

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
  /** Whose files these are: the Immich user on this page (empty for the worker, which sends any and lets the server check). */
  owner: string;
  /** Whether nothing may be sent right now (the Wi-Fi only switch, on mobile data). */
  held(): boolean;
  now(): number;
  /** Resolves after ms, or at once when the network comes back. */
  pause(ms: number): Promise<void>;
  newId(): string;
  /** How a file is sent (the network; tests give their own). */
  send?: typeof realSend;
  /** Runs `work` only if no other runner of this queue is working (the page and the worker must never both send); skips it
   *  and answers false when another is. Default: always. */
  exclusive?: (work: () => Promise<void>) => Promise<boolean>;
}

export class Queue {
  items: Item[] = [];
  private running = false;
  private halted = false;
  private abort = new Set<string>();

  private store: Store;
  private env: Env;
  private changed: () => void;

  constructor(store: Store, env: Env, changed: () => void) {
    this.store = store;
    this.env = env;
    this.changed = changed;
  }

  /** What this person's page shows: their own files. (Another person's, left in this browser, are neither shown nor sent.) */
  get mine(): Item[] {
    return this.items.filter((i) => this.env.owner === "" || i.owner === "" || i.owner === this.env.owner);
  }

  /** Reads what the store holds. A file that was being sent when the page closed, or that waited for a sign-in, goes on again. */
  async load(): Promise<void> {
    this.items = (await this.store.all()).sort((a, b) => a.added - b.added);
    for (const i of this.items) if (i.state === "sending" || i.state === "signin") i.state = "offline";
    this.changed();
  }

  /** Puts files in the line. Those the browser has no room for are returned (by name), the rest are kept. */
  async add(files: File[], service: string): Promise<string[]> {
    const refused: string[] = [];
    for (const f of files) {
      const item = newItem(this.env.newId(), f.name, f.size, f.lastModified, service, this.env.owner, this.env.now());
      try {
        await this.store.put(item, f);
        this.items.push(item);
      } catch {
        refused.push(f.name);
      }
    }
    this.changed();
    return refused;
  }

  private async save(item: Item): Promise<void> {
    await this.store.put(item);
    this.changed();
  }

  async cancel(id: string): Promise<void> {
    const item = this.mine.find((i) => i.id === id);
    if (!item || finished(item.state)) return;
    this.abort.add(id);
    item.state = "cancelled";
    if (item.serverId !== "") void fetch(new URL(`send/${item.serverId}/cancel`, this.env.base).href, { method: "POST", headers: { "X-Requested-With": "immich-handoff" } }).catch(() => undefined);
    await this.store.remove(id);
    this.changed();
  }

  /** Files shared from the gallery that wait for their person to say where they go. */
  get shared(): Item[] {
    return this.mine.filter((i) => i.state === "pick");
  }

  /** Sends the shared files where the person chose: they become the person's own and take their turn in the line. */
  async assign(service: string): Promise<void> {
    for (const item of this.shared) {
      item.service = service;
      item.owner = this.env.owner;
      item.state = "waiting";
      await this.store.put(item);
    }
    this.changed();
  }

  /** Puts a file that failed or waited for a sign-in back in the line. */
  async retry(id: string): Promise<void> {
    const item = this.mine.find((i) => i.id === id);
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
    const gone = this.mine.filter((x) => finished(x.state));
    for (const i of gone) await this.store.remove(i.id);
    this.items = this.items.filter((i) => !gone.includes(i));
    this.changed();
  }

  /** Whether this runner is sending now. */
  get busy(): boolean {
    return this.running;
  }

  /** Stops after the piece being sent, and lets another runner (the worker, with the screen off) take over. */
  halt(): void {
    this.halted = true;
  }

  /** Reads the store again, to see what another runner did (the worker, while the page was not the one sending). */
  async refresh(): Promise<void> {
    if (this.running) return;
    this.items = (await this.store.all()).sort((a, b) => a.added - b.added);
    this.changed();
  }

  /** Sends what is waiting, one file at a time, until nothing can go on (everything is done, or waits for a sign-in). Only one
   *  runner works at a time: if another has the line, this does nothing. */
  async run(): Promise<void> {
    if (this.running) return;
    this.running = true; // before the first await, so two calls in a row cannot both start
    try {
      if (this.env.exclusive) await this.env.exclusive(() => this.loop());
      else await this.loop();
    } finally {
      this.running = false;
    }
  }

  /** For the worker: take the line, read the store afresh (what was being sent was left by a runner that is gone), and send. */
  async resume(): Promise<void> {
    if (this.running) return;
    this.running = true;
    try {
      const work = async (): Promise<void> => {
        await this.load();
        await this.loop();
      };
      if (this.env.exclusive) await this.env.exclusive(work);
      else await work();
    } finally {
      this.running = false;
    }
  }

  private async loop(): Promise<void> {
    this.halted = false;
    for (;;) {
      if (this.halted || this.env.held()) return;
      const at = nextToSend(this.items, this.env.owner);
      if (at < 0) return;
      const item = this.items[at]!;
      const file = await this.store.file(item.id);
      if (!file) {
        item.state = "failed";
        item.ended = this.env.now();
        item.text = "the file is not there any more";
        await this.save(item);
        continue;
      }
      item.state = "sending";
      await this.save(item);
      let kept = this.env.now();
      const out = await (this.env.send ?? realSend)(this.env.base, file, item, {
        progress: (sent, serverId) => {
          const fresh = item.serverId !== serverId;
          item.sent = sent;
          item.serverId = serverId;
          this.changed();
          // where the file stands is kept as it goes (not only at the end), so a phone that dies mid-file carries on from the
          // server's piece, not from the start
          if (fresh || this.env.now() - kept >= 1000) {
            kept = this.env.now();
            void this.store.put(item);
          }
        },
        stopped: () => this.abort.has(item.id) || this.halted,
      });
      if (this.abort.has(item.id)) {
        this.abort.delete(item.id);
        continue;
      }
      item.serverId = out.serverId;
      switch (out.kind) {
        case "done":
          item.state = "done";
          item.ended = this.env.now();
          item.photo = out.photo;
          item.duplicate = out.duplicate;
          item.sent = item.size;
          await this.store.release(item.id); // the file itself is no longer needed; its record stays to be seen
          break;
        case "failed":
          item.state = "failed";
          item.ended = this.env.now();
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
      if (item.state === "offline") {
        if (this.halted) return;
        await this.env.pause(backoff(item.tries));
      }
    }
  }
}
