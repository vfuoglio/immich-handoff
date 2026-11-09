// Run by `npm test` in tools/typecheck.
import assert from "node:assert/strict";
import { test } from "node:test";
import { backoffMs, fileKey, fitWithin } from "./drop.ts";

test("a file is recognised again by its name, size and date", () => {
  const a = { name: "a.jpg", size: 10, lastModified: 5 };
  assert.equal(fileKey(a), fileKey({ ...a }));
  assert.notEqual(fileKey(a), fileKey({ ...a, size: 11 }));
  assert.notEqual(fileKey(a), fileKey({ ...a, lastModified: 6 }));
  assert.notEqual(fileKey(a), fileKey({ ...a, name: "b.jpg" }));
});

test("the wait grows after each failure and stops growing at half a minute", () => {
  assert.deepEqual([1, 2, 3, 4].map(backoffMs), [2000, 4000, 8000, 16000]);
  assert.equal(backoffMs(5), 30000);
  assert.equal(backoffMs(50), 30000);
  assert.equal(backoffMs(0), 2000);
});

test("a picture is shrunk by its longer side and keeps its shape", () => {
  assert.deepEqual(fitWithin(4000, 3000, 2000), { width: 2000, height: 1500, shrunk: true });
  assert.deepEqual(fitWithin(3000, 4000, 2000), { width: 1500, height: 2000, shrunk: true });
  assert.deepEqual(fitWithin(1000, 800, 2000), { width: 1000, height: 800, shrunk: false });
  assert.deepEqual(fitWithin(2000, 1000, 2000), { width: 2000, height: 1000, shrunk: false });
  assert.deepEqual(fitWithin(4000, 3000, 0), { width: 4000, height: 3000, shrunk: false });
  assert.equal(fitWithin(10000, 1, 100).height, 1);
});
