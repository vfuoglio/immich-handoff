import assert from "node:assert/strict";
import { test } from "node:test";
import { assetOfPath, countIn, formOf, inOrder, Selection, wordsOf } from "./handover-core.ts";

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

test("a selection that has scrolled out of the page is still known, and one that cannot be known says so", () => {
  const sel = new Selection();
  sel.see([{ id: "a", selected: true }, { id: "b", selected: true }, { id: "c", selected: false }]);
  assert.deepEqual(sel.idsFor(2)?.sort(), ["a", "b"]);
  sel.see([{ id: "c", selected: true }]); // another screen: a and b are no longer drawn
  assert.deepEqual(sel.idsFor(3)?.sort(), ["a", "b", "c"]);
  sel.see([{ id: "b", selected: false }]); // b is drawn again and the person took it off
  assert.deepEqual(sel.idsFor(2)?.sort(), ["a", "c"]);
  assert.equal(sel.idsFor(500), null, "Select all: Immich counts photos that were never drawn");
  assert.equal(sel.idsFor(null), null);
  assert.equal(sel.idsFor(0), null);
  sel.clear();
  assert.equal(sel.size, 0);
  assert.equal(sel.idsFor(2), null);
});

test("the count in the bar is the paragraph that is only a number, in any language", () => {
  assert.equal(countIn(["12", "12 selected"]), 12);
  assert.equal(countIn(["12", "12 ausgewählt"]), 12);
  assert.equal(countIn(["12 selected"]), null);
  assert.equal(countIn([]), null);
});

test("the service used last comes first, the others keep their order", () => {
  const list = [{ name: "a", copy: true }, { name: "b", copy: true }, { name: "c", copy: false }];
  assert.deepEqual(inOrder(list, "c").map((s) => s.name), ["c", "a", "b"]);
  assert.deepEqual(inOrder(list, null).map((s) => s.name), ["a", "b", "c"]);
  assert.deepEqual(inOrder(list, "gone").map((s) => s.name), ["a", "b", "c"]);
});
