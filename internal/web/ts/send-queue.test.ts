import assert from "node:assert/strict";
import { test } from "node:test";
import { MemoryStore, Queue, type Env, type Store } from "./send-queue.ts";
import type { Outcome } from "./send-run.ts";

function file(name: string, size = 10): File {
  return new File([new Uint8Array(size)], name, { lastModified: 1000 });
}

function rig(script: (name: string, call: number) => Outcome, over: Partial<Env> = {}, store: Store = new MemoryStore()) {
  const calls: string[] = [];
  const counts = new Map<string, number>();
  const env: Env = {
    base: "http://x/",
    owner: "me",
    held: () => false,
    now: () => 5,
    newId: (() => { let n = 0; const tag = Math.random().toString(36).slice(2, 6); return () => `id${tag}${++n}`; })(),
    pause: async () => undefined,
    send: async (_b, _f, job) => {
      calls.push(job.name);
      const c = (counts.get(job.name) ?? 0) + 1;
      counts.set(job.name, c);
      return script(job.name, c);
    },
    ...over,
  };
  let changes = 0;
  const q = new Queue(store, env, () => changes++);
  return { q, calls, store, changes: () => changes };
}

const done = (id = "s"): Outcome => ({ kind: "done", serverId: id, photo: "p", duplicate: false });

test("files are sent one after the other, in the order they were added", async () => {
  const { q, calls } = rig(() => done());
  await q.add([file("a.jpg"), file("b.jpg")], "family");
  await q.run();
  assert.deepEqual(calls, ["a.jpg", "b.jpg"]);
  assert.deepEqual(q.items.map((i) => i.state), ["done", "done"]);
  assert.equal(q.items[0]!.service, "family");
  assert.equal(q.items[0]!.owner, "me");
});

test("when the network goes the file waits and is sent again by itself, then carries on", async () => {
  const { q, calls } = rig((_n, c) => (c < 3 ? { kind: "offline", serverId: "u1", sent: 4 } : done("u1")));
  await q.add([file("a.jpg"), file("b.jpg")], "");
  await q.run();
  assert.deepEqual(calls, ["a.jpg", "a.jpg", "a.jpg", "b.jpg", "b.jpg", "b.jpg"], "each file waits for the network on its own");
  assert.deepEqual(q.items.map((i) => i.state), ["done", "done"]);
  assert.equal(q.items[0]!.tries, 2, "the tries are kept, for the waiting time");
});

test("a refusal is final for that file only, and a sign-in waits without stopping the others", async () => {
  const { q } = rig((n) => (n === "bad.jpg" ? { kind: "failed", serverId: "", text: "only photos and videos can be sent" } : n === "out.jpg" ? { kind: "signin", serverId: "", sent: 0 } : done()));
  await q.add([file("bad.jpg"), file("out.jpg"), file("ok.jpg")], "");
  await q.run();
  assert.deepEqual(q.items.map((i) => i.state), ["failed", "signin", "done"]);
  assert.equal(q.items[0]!.text, "only photos and videos can be sent");
});

test("a file put back in the line is sent again from the start if it failed, and from where it stood if it waited", async () => {
  let fail = true;
  const { q, calls } = rig((n) => (fail && n === "a.jpg" ? { kind: "failed", serverId: "dead", text: "no" } : done()));
  await q.add([file("a.jpg")], "");
  await q.run();
  fail = false;
  await q.retry(q.items[0]!.id);
  assert.equal(q.items[0]!.serverId, "", "a failed file starts again");
  await q.run();
  assert.equal(q.items[0]!.state, "done");
  assert.equal(calls.length, 2);
});

test("a file that is taken out of the line is not sent, and its bytes go", async () => {
  const { q, calls, store } = rig(() => done());
  await q.add([file("a.jpg"), file("b.jpg")], "");
  await q.cancel(q.items[0]!.id);
  await q.run();
  assert.deepEqual(calls, ["b.jpg"]);
  assert.equal(await store.file(q.items[0]!.id), undefined);
  assert.equal(q.items[0]!.state, "cancelled");
});

test("nothing is sent while the Wi-Fi only switch holds, and it goes when it lets go", async () => {
  let held = true;
  const { q, calls } = rig(() => done(), { held: () => held });
  await q.add([file("a.jpg")], "");
  await q.run();
  assert.deepEqual(calls, []);
  assert.equal(q.items[0]!.state, "waiting");
  held = false;
  await q.run();
  assert.deepEqual(calls, ["a.jpg"]);
});

test("another person's files in the same browser are neither shown nor sent", async () => {
  const store = new MemoryStore();
  const first = rig(() => ({ kind: "offline", serverId: "", sent: 0 }), { owner: "ann", pause: () => new Promise<void>(() => undefined) }, store);
  await first.q.add([file("anns.jpg")], "");
  // ann's page is closed; bob opens the page in the same browser
  const bob = rig(() => done(), { owner: "bob" }, store);
  await bob.q.load();
  await bob.q.add([file("bobs.jpg")], "");
  await bob.q.run();
  assert.deepEqual(bob.calls, ["bobs.jpg"]);
  assert.deepEqual(bob.q.mine.map((i) => i.name), ["bobs.jpg"]);
  assert.equal((await store.all()).length, 2, "ann's file is still kept for ann");
});

test("what was being sent, or waited for a sign-in, when the page closed goes on at the next load", async () => {
  const store = new MemoryStore();
  const a = rig(() => done(), {}, store);
  await a.q.add([file("a.jpg"), file("b.jpg")], "");
  const items = await store.all();
  items[0]!.state = "sending";
  items[0]!.serverId = "u9";
  items[0]!.sent = 5;
  items[1]!.state = "signin";
  for (const i of items) await store.put(i);
  const b = rig(() => done(), {}, store);
  await b.q.load();
  assert.deepEqual(b.q.items.map((i) => i.state), ["offline", "offline"]);
  assert.equal(b.q.items[0]!.serverId, "u9", "it asks the server where it stands");
  await b.q.run();
  assert.deepEqual(b.q.items.map((i) => i.state), ["done", "done"]);
});

test("a browser with no room refuses the file and says which, keeping the rest", async () => {
  const store = new MemoryStore();
  const real = store.put.bind(store);
  store.put = async (item, f) => {
    if (item.name === "huge.mov") throw new Error("QuotaExceededError");
    return real(item, f);
  };
  const { q } = rig(() => done(), {}, store);
  const refused = await q.add([file("a.jpg"), file("huge.mov"), file("b.jpg")], "");
  assert.deepEqual(refused, ["huge.mov"]);
  assert.deepEqual(q.items.map((i) => i.name), ["a.jpg", "b.jpg"]);
});

test("finished files are cleared from the list and the store", async () => {
  const { q, store } = rig(() => done());
  await q.add([file("a.jpg")], "");
  await q.run();
  await q.clear();
  assert.deepEqual(q.items, []);
  assert.deepEqual(await store.all(), []);
});

test("when another runner has the line (the worker, with the screen off), this one sends nothing", async () => {
  const { q, calls } = rig(() => done(), { exclusive: async () => false });
  await q.add([file("a.jpg")], "");
  await q.run();
  assert.deepEqual(calls, []);
  assert.equal(q.items[0]!.state, "waiting");
});

test("the worker takes the line, reads what the page left, and sends it", async () => {
  const store = new MemoryStore();
  const page = rig(() => done(), {}, store);
  await page.q.add([file("a.jpg"), file("b.jpg")], "family");
  const items = await store.all();
  items[0]!.state = "sending"; // the page was killed while it was sending
  items[0]!.serverId = "u7";
  await store.put(items[0]!);
  const held: string[] = [];
  const worker = rig(() => done(), { owner: "", exclusive: async (work) => { held.push("took"); await work(); held.push("let go"); return true; } }, store);
  await worker.q.resume();
  assert.deepEqual(held, ["took", "let go"]);
  assert.deepEqual(worker.calls, ["a.jpg", "b.jpg"]);
  assert.deepEqual((await store.all()).map((i) => i.state).sort(), ["done", "done"]);
});

test("a worker whose network is gone again ends with an error, so the browser wakes it later, and keeps where the file stands", async () => {
  const { q, store } = rig(() => ({ kind: "offline", serverId: "u1", sent: 3 }), { pause: () => Promise.reject(new Error("offline")) });
  await q.add([file("a.jpg")], "");
  await assert.rejects(q.resume(), /offline/);
  const kept = (await store.all())[0]!;
  assert.equal(kept.state, "offline");
  assert.equal(kept.serverId, "u1");
  assert.equal(kept.sent, 3);
  assert.equal(q.busy, false, "the line is let go of");
});

test("halting stops after the piece being sent and does not wait for the network", async () => {
  let q: Queue | undefined;
  const r = rig((n) => { if (n === "a.jpg") q!.halt(); return { kind: "offline", serverId: "u2", sent: 1 }; }, { pause: () => new Promise<void>(() => undefined) });
  q = r.q;
  await q.add([file("a.jpg"), file("b.jpg")], "");
  await q.run(); // returns instead of waiting for the network for ever
  assert.deepEqual(r.calls, ["a.jpg"]);
  assert.equal(q.items[0]!.state, "offline");
  assert.equal(q.items[1]!.state, "waiting");
});

test("a file's end is stamped, so that only what ended since the last message is told", async () => {
  const { q } = rig((n) => (n === "bad.jpg" ? { kind: "failed", serverId: "", text: "no" } : done()), { now: () => 77 });
  await q.add([file("ok.jpg"), file("bad.jpg")], "");
  await q.run();
  assert.deepEqual(q.items.map((i) => i.ended), [77, 77]);
});

test("where a file stands is kept while it goes, so a phone that dies mid-file carries on from the server's piece", async () => {
  let t = 0;
  const store = new MemoryStore();
  const calls: string[] = [];
  const env: Env = {
    base: "http://x/", owner: "me", held: () => false, now: () => (t += 1500), newId: () => "k1", pause: async () => undefined,
    send: async (_b, _f, _job, hooks) => {
      hooks.progress(0, "srv-1");
      const mid = (await store.all())[0]!;
      calls.push(`${mid.serverId}:${mid.state}`); // what a restart would find
      hooks.progress(4, "srv-1");
      return done("srv-1");
    },
  };
  const queue = new Queue(store, env, () => undefined);
  await queue.add([file("a.jpg")], "");
  await queue.run();
  assert.deepEqual(calls, ["srv-1:sending"]);
});
