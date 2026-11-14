import assert from "node:assert/strict";
import { test } from "node:test";
import { heartText, mergeNewest, newestId, oldestId, parseFeed, type GalleryItem } from "./gallery.ts";

const item = (id: number, hearts = 0): GalleryItem => ({ id, name: "", caption: "", hearts, mine: false });

test("a feed is read, and what is not a feed is not one", () => {
  const feed = parseFeed({ items: [{ id: 3, name: "Ann", hearts: 2, mine: true }, { id: "x" }, null, { id: 1 }], download: true });
  assert.deepEqual(feed, {
    items: [
      { id: 3, name: "Ann", caption: "", hearts: 2, mine: true },
      { id: 1, name: "", caption: "", hearts: 0, mine: false },
    ],
    download: true,
  });
  assert.equal(parseFeed(null), null);
  assert.equal(parseFeed({ items: "no" }), null);
  assert.equal(parseFeed({ items: [{ id: 1, hearts: -4 }] })?.items[0]?.hearts, 0, "a negative count is none");
});

test("newer photos go in front, none twice, and an update wins", () => {
  const shown = [item(5), item(3), item(1)];
  assert.deepEqual(mergeNewest(shown, [item(7), item(6)]).map((i) => i.id), [7, 6, 5, 3, 1]);
  const merged = mergeNewest(shown, [item(3, 4)]);
  assert.deepEqual(merged.map((i) => i.id), [5, 3, 1]);
  assert.equal(merged[1]?.hearts, 4);
  assert.deepEqual(mergeNewest([], [item(1), item(2)]).map((i) => i.id), [2, 1], "older ones that come later are put in place");
});

test("the edges of what is shown", () => {
  assert.equal(newestId([]), 0);
  assert.equal(oldestId([]), 0);
  assert.equal(newestId([item(5), item(9), item(2)]), 9);
  assert.equal(oldestId([item(5), item(9), item(2)]), 2);
});

test("a heart count is written only when there is one", () => {
  assert.equal(heartText(0), "");
  assert.equal(heartText(12), "12");
});
