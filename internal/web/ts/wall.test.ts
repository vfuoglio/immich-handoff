// Run by `npm test` in tools/typecheck.
import assert from "node:assert/strict";
import { test } from "node:test";
import { cursorOf, latest, mergeItems, modeOf, nextToShow, tiltOf } from "./wall.ts";

const item = (id: number) => ({ id });

test("what is new is added once, in order, and only the newest are kept", () => {
  const merged = mergeItems([item(1), item(2)], [item(3), item(2), item(4)], 3);
  assert.deepEqual(
    merged.map((i) => i.id),
    [2, 3, 4],
  );
  assert.deepEqual(mergeItems([], [], 5), []);
  assert.deepEqual(
    mergeItems([item(2)], [item(1)], 5).map((i) => i.id),
    [1, 2],
  );
});

test("the wall asks next for what is newer than anything it has seen, and never goes back", () => {
  assert.equal(cursorOf([item(3), item(9), item(5)], 0), 9);
  assert.equal(cursorOf([], 7), 7);
  assert.equal(cursorOf([item(2)], 7), 7);
});

test("a grid shows the newest first", () => {
  assert.deepEqual(
    latest([item(1), item(2), item(3), item(4)], 3).map((i) => i.id),
    [4, 3, 2],
  );
  assert.deepEqual(latest([item(1)], 12).map((i) => i.id), [1]);
});

test("a collage tilts each photo a little, the same way every time", () => {
  for (let id = 1; id < 200; id++) {
    const t = tiltOf(id);
    assert.ok(t >= -6 && t <= 6, `${id}: ${t}`);
    assert.equal(t, tiltOf(id));
  }
  assert.notEqual(tiltOf(1), tiltOf(2));
});

test("a photo that has just arrived is shown before going round the others", () => {
  const items = [item(1), item(2), item(3)];
  assert.equal(nextToShow(items, 1, []), 2);
  assert.equal(nextToShow(items, 3, []), 1);
  assert.equal(nextToShow(items, 1, [3]), 3);
  assert.equal(nextToShow(items, 1, [99]), 2); // one that is no longer there is skipped
  assert.equal(nextToShow([], -1, []), -1);
});

test("an unknown layout is the single photo", () => {
  assert.equal(modeOf("grid"), "grid");
  assert.equal(modeOf("collage"), "collage");
  assert.equal(modeOf("zigzag"), "single");
  assert.equal(modeOf(undefined), "single");
});
