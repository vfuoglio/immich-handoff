import assert from "node:assert/strict";
import { test } from "node:test";
import { addForm, assetOfPath, choose, countIn, formOf, inOrder, ownedQuery, Selection, splitOwned, wordsOf, type Owner } from "./handover-core.ts";

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

test("photos are sorted by whose they are, and the ones not asked about yet are unknown", () => {
  const known = new Map<string, Owner>([["a", "mine"], ["b", "other"], ["c", "mine"]]);
  assert.deepEqual(splitOwned(["a", "b", "x", "c"], known), { mine: ["a", "c"], others: ["b"], unknown: ["x"] });
  assert.deepEqual(splitOwned([], known), { mine: [], others: [], unknown: [] });
});

test("the buttons are chosen by whose the photos are", () => {
  assert.deepEqual(choose({ mine: ["a"], others: [], unknown: [] }), { hand: true, add: false });
  assert.deepEqual(choose({ mine: [], others: ["b"], unknown: [] }), { hand: false, add: true });
  assert.deepEqual(choose({ mine: ["a"], others: ["b"], unknown: [] }), { hand: true, add: true }, "a mix shows both");
});

test("photos not answered about yet do not count, and nothing shows while none is known", () => {
  assert.deepEqual(choose({ mine: ["a"], others: [], unknown: ["x"] }), { hand: true, add: false }, "a showing button stays while the next photo is asked about");
  assert.deepEqual(choose({ mine: [], others: [], unknown: ["x"] }), { hand: false, add: false });
  assert.deepEqual(choose({ mine: [], others: [], unknown: [] }), { hand: false, add: false });
});

test("the question about ownership has one asset for each photo", () => {
  assert.deepEqual(ownedQuery(["a", "b"]).getAll("asset"), ["a", "b"]);
});

test("adding to the library sends the photos, where they go and, only when asked, to remember it", () => {
  const plain = addForm(["a", "b"], "plain", "ignored", false);
  assert.equal(plain.get("dest"), "plain");
  assert.equal(plain.get("album"), null, "an album name only goes with an album");
  assert.equal(plain.get("remember"), null);
  assert.deepEqual(plain.getAll("asset"), ["a", "b"]);
  const album = addForm(["a"], "album", "From Ann", true);
  assert.equal(album.get("album"), "From Ann");
  assert.equal(album.get("remember"), "1");
});
