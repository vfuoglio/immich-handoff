// Run by `npm test` in tools/typecheck.
import assert from "node:assert/strict";
import { test } from "node:test";
import { rank, score } from "./palette.ts";
import type { Command } from "./palette.ts";

const noop = (): void => {};
const cmd = (label: string, hint = "view"): Command => ({ label, hint, run: noop });

test("every word of the query must appear, anywhere in the label or its hint", () => {
  assert.equal(score("pause club", "Pause sending: club service"), 6);
  assert.equal(score("pause zzz", "Pause sending: club service"), 0);
  assert.equal(score("", "anything"), 1);
  assert.equal(score("PAUSE", "pause sending"), 3);
});

test("a match at the start of a word beats one inside a word", () => {
  assert.ok(score("pa", "Go to Paused things") > score("us", "Go to Paused things"));
  assert.ok(score("pa", "Pause") > score("ause", "Pause") - 1);
});

test("rank keeps only matches, best first, and the page's own order among equals", () => {
  const all = [cmd("Go to Overview"), cmd("Go to Services"), cmd("Go to Activity"), cmd("Open club", "service"), cmd("Pause club", "service")];
  assert.deepEqual(rank("club", all).map((c) => c.label), ["Open club", "Pause club"]);
  assert.deepEqual(rank("go", all).map((c) => c.label), ["Go to Overview", "Go to Services", "Go to Activity"]);
  assert.deepEqual(rank("nothing like this", all), []);
  assert.equal(rank("", all).length, all.length);
});

test("the hint can be searched too, so 'service' finds the service commands", () => {
  const all = [cmd("Go to Services"), cmd("Open club", "service"), cmd("Sync now", "action")];
  assert.deepEqual(rank("action", all).map((c) => c.label), ["Sync now"]);
});
