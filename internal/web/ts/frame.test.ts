// Run by `npm test` in tools/typecheck.
import assert from "node:assert/strict";
import { test } from "node:test";
import { arrivals, backoff, pick } from "./frame.ts";
import type { Show } from "./frame.ts";

const start: Show = { shown: [], fresh: [], last: "" };
const seq = (values: number[]): (() => number) => {
  let i = 0;
  return () => values[i++ % values.length] ?? 0;
};

test("nothing to show when the album is empty", () => {
  assert.equal(pick([], start, Math.random), null);
});

test("every photo is shown once in a round before any is shown again", () => {
  const items = ["a", "b", "c", "d", "e"];
  let show = start;
  const seen: string[] = [];
  for (let i = 0; i < items.length; i++) {
    const p = pick(items, show, Math.random);
    assert.ok(p);
    seen.push(p.id);
    show = p.show;
  }
  assert.deepEqual([...seen].sort(), items);
});

test("a new round never starts with the photo that was just shown", () => {
  const items = ["a", "b", "c"];
  for (let trial = 0; trial < 200; trial++) {
    let show = start;
    let last = "";
    for (let i = 0; i < 12; i++) {
      const p = pick(items, show, Math.random);
      assert.ok(p);
      assert.notEqual(p.id, last, "the same photo twice in a row");
      last = p.id;
      show = p.show;
    }
  }
});

test("a photo that arrived after the page opened is shown next, once", () => {
  const items = ["new", "a", "b"];
  let p = pick(items, { shown: ["a"], fresh: ["new"], last: "a" }, seq([0]));
  assert.equal(p?.id, "new");
  assert.deepEqual(p?.show.fresh, []);
  p = pick(items, p!.show, seq([0]));
  assert.equal(p?.id, "b"); // not "new" again, and "a" was shown in this round
});

test("a photo taken out of the album is forgotten, fresh or not", () => {
  const p = pick(["a", "b"], { shown: ["gone", "a"], fresh: ["gone2"], last: "a" }, seq([0]));
  assert.equal(p?.id, "b");
  assert.deepEqual(p?.show.shown, ["a", "b"]);
});

test("one photo is shown again and again, as it is the only one", () => {
  let show = start;
  for (let i = 0; i < 3; i++) {
    const p = pick(["only"], show, Math.random);
    assert.equal(p?.id, "only");
    show = p!.show;
  }
});

test("arrivals are what the list gained, and nothing on the first list", () => {
  assert.deepEqual(arrivals(null, ["a", "b"]), []);
  assert.deepEqual(arrivals(["a", "b"], ["c", "a", "d"]), ["c", "d"]);
  assert.deepEqual(arrivals(["a"], []), []);
});

test("the wait after a failure doubles from five seconds and stops at fifteen minutes", () => {
  assert.equal(backoff(1), 5_000);
  assert.equal(backoff(2), 10_000);
  assert.equal(backoff(3), 20_000);
  assert.equal(backoff(8), 640_000);
  assert.equal(backoff(9), 900_000);
  assert.equal(backoff(40), 900_000);
  assert.equal(backoff(0), 5_000);
});
