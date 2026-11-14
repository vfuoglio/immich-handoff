import assert from "node:assert/strict";
import { test } from "node:test";
import { step } from "./room.ts";

test("moving through the pictures goes round at both ends", () => {
  assert.equal(step(0, 1, 5), 1);
  assert.equal(step(4, 1, 5), 0);
  assert.equal(step(0, -1, 5), 4);
  assert.equal(step(2, 0, 5), 2);
  assert.equal(step(7, 0, 5), 2);
  assert.equal(step(0, 1, 1), 0);
  assert.equal(step(3, 1, 0), 0);
});
