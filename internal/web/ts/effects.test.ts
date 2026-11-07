import assert from "node:assert/strict";
import { test } from "node:test";
import { tiltFor } from "./effects.ts";
import { columns } from "./odometer.ts";

test("a card does not tilt when the pointer is in its middle, and tilts toward the pointer at the edges", () => {
  assert.deepEqual(tiltFor(0.5, 0.5), { rx: 0, ry: 0 });
  const right = tiltFor(1, 0.5);
  assert.ok(right.ry > 0 && right.rx === 0);
  const topLeft = tiltFor(0, 0);
  assert.ok(topLeft.ry < 0 && topLeft.rx > 0);
});

test("the tilt is limited, whatever the pointer does", () => {
  const far = tiltFor(50, -50);
  assert.ok(Math.abs(far.rx) <= 6 && Math.abs(far.ry) <= 6);
});

test("digits for the rolling columns: padded to the length, never negative, trimmed when the number shrank", () => {
  assert.deepEqual(columns(12, 2), [1, 2]);
  assert.deepEqual(columns(5, 3), [0, 0, 5]);
  assert.deepEqual(columns(123, 2), [2, 3]);
  assert.deepEqual(columns(-4, 2), [0, 0]);
  assert.deepEqual(columns(7.9, 1), [7]);
});
