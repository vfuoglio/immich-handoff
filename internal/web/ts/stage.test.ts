// Run by `npm test` in tools/typecheck.
import assert from "node:assert/strict";
import { test } from "node:test";
import { bgOf, fxOf, holdFor, isLite } from "./stage.ts";

test("a transition is one of four, and anything else is the gentle fade", () => {
  for (const fx of ["fade", "zoom", "slide", "rise"] as const) assert.equal(fxOf(fx), fx);
  for (const odd of ["", "spin", "ZOOM", "flip", undefined, null]) assert.equal(fxOf(odd), "fade");
});

test("what is behind the photos is the glow, or black when asked", () => {
  assert.equal(bgOf("black"), "black");
  assert.equal(bgOf("glow"), "glow");
  for (const odd of ["", "white", "BLACK", undefined, null]) assert.equal(bgOf(odd), "glow");
});

test("a new arrival is given a little longer than the others, and nothing else changes", () => {
  assert.equal(holdFor(7000, false), 7000);
  assert.equal(holdFor(7000, true), 11200);
  assert.ok(holdFor(15000, true) > holdFor(15000, false));
});

test("old hardware gets the kinder mode: few cores, little memory, or a request to save data", () => {
  assert.equal(isLite({ cores: 2 }), true);
  assert.equal(isLite({ cores: 1 }), true);
  assert.equal(isLite({ memory: 2 }), true);
  assert.equal(isLite({ memory: 0.5 }), true);
  assert.equal(isLite({ saveData: true }), true);
  assert.equal(isLite({ cores: 8, memory: 8 }), false);
  assert.equal(isLite({ cores: 4 }), false);
  assert.equal(isLite({}), false); // nothing known: the full show
  assert.equal(isLite({ cores: 0, memory: 0 }), false); // a browser that reports nothing useful
});
