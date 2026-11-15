import assert from "node:assert/strict";
import { test } from "node:test";
import { assetOfPath, formOf, wordsOf } from "./handover-core.ts";

const id = "624ac6b0-4a95-41c2-b99c-d9cd19de0843";

test("the viewer's address holds the photo's id, wherever the person came from", () => {
  assert.equal(assetOfPath(`/photos/${id}`), id);
  assert.equal(assetOfPath(`/photos/${id}/`), id);
  assert.equal(assetOfPath(`/albums/aaaaaaaa-4a95-41c2-b99c-d9cd19de0843/photos/${id}`), id);
  assert.equal(assetOfPath(`/people/aaaaaaaa-4a95-41c2-b99c-d9cd19de0843/photos/${id.toUpperCase()}`), id);
});

test("any other address has no photo in it", () => {
  assert.equal(assetOfPath("/photos"), null);
  assert.equal(assetOfPath("/photos/not-an-id"), null);
  assert.equal(assetOfPath(`/photos/${id}/info`), null);
  assert.equal(assetOfPath(`/explore`), null);
});

test("a word the app did not send is shown in English, so nothing is ever blank", () => {
  const w = wordsOf({ words: { Undo: "Annulla" }, services: [] });
  assert.equal(w("Undo"), "Annulla");
  assert.equal(w("Hand over..."), "Hand over...");
  assert.equal(wordsOf(null)("Undo"), "Undo");
});

test("the press sends the service and each photo once", () => {
  const form = formOf("Family & friends", [id, "x"]);
  assert.equal(form.get("service"), "Family & friends");
  assert.deepEqual(form.getAll("asset"), [id, "x"]);
});
