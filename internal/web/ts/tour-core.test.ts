// Run by `npm test` in tools/typecheck.
import assert from "node:assert/strict";
import { test } from "node:test";
import { place, plan, step } from "./tour-core.ts";

const targets = { a: { selector: "#a" }, b: { selector: "#b" }, none: {}, ghost: { selector: "#ghost" } };
const stop = (target: string) => ({ target, title: target, text: "x" });

test("a stop is kept only when its thing is on the page", () => {
  const here = (s: string): boolean => s === "#a" || s === "#b";
  assert.deepEqual(plan([stop("a"), stop("ghost"), stop("b"), stop("none"), stop("unknown")], targets, here).map((s) => s.target), ["a", "b"]);
  assert.deepEqual(plan([], targets, here), []);
});

test("the card goes under the thing, above it when there is no room, over the middle when there is none either", () => {
  const view = { width: 1000, height: 700 };
  const card = { width: 300, height: 160 };
  assert.deepEqual(place({ left: 100, top: 100, width: 200, height: 40 }, card, view), { left: 50, top: 152 });
  assert.deepEqual(place({ left: 100, top: 600, width: 200, height: 40 }, card, view), { left: 50, top: 428 });
  const tall = place({ left: 100, top: 20, width: 200, height: 660 }, card, view);
  assert.deepEqual(tall, { left: 350, top: 270 });
});

test("the card stays inside the window at its edges", () => {
  const view = { width: 400, height: 700 };
  const card = { width: 300, height: 160 };
  assert.equal(place({ left: 0, top: 50, width: 40, height: 40 }, card, view).left, 8);
  assert.equal(place({ left: 380, top: 50, width: 20, height: 40 }, card, view).left, 92);
});

test("steps move by one, stop at the first, and end after the last", () => {
  assert.equal(step(0, 1, 3), 1);
  assert.equal(step(2, 1, 3), null);
  assert.equal(step(0, -1, 3), 0);
  assert.equal(step(2, -1, 3), 1);
});
