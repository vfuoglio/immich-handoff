// Run by `npm test` in tools/typecheck.
import assert from "node:assert/strict";
import { test } from "node:test";
import { featureOf } from "./help.ts";

test("a link to a guide names its feature, whatever follows", () => {
  assert.equal(featureOf("help/stories"), "stories");
  assert.equal(featureOf("help/stories#a-heading"), "stories");
  assert.equal(featureOf("../help/folder-albums"), "folder-albums");
  assert.equal(featureOf("/handoff/help/pets?x=1"), "pets");
});

test("the search and the pictures are not guides", () => {
  assert.equal(featureOf("help/search?q=a"), null);
  assert.equal(featureOf("help/img/hero.jpg"), null);
  assert.equal(featureOf("https://example.org/page"), null);
  assert.equal(featureOf("admin"), null);
});
