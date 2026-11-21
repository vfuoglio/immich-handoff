// Run by `npm test` in tools/typecheck.
import assert from "node:assert/strict";
import { test } from "node:test";
import { DURATIONS, after, before, buildTimeline, progressOf, rangeText, swipeOf, tapOf, type Doc } from "./story-core.ts";

const fmt = (d: string): string => `<${d}>`;
const doc: Doc = {
  title: "Portugal",
  chapters: [
    { title: "Lisbon", from: "2026-08-01", to: "2026-08-03" },
    { title: "", from: "2026-08-04", to: "2026-08-04" },
  ],
  scenes: [
    { id: "a", chapter: 0, date: "2026-08-01", place: "Lisbon, Portugal" },
    { id: "b", chapter: 0, date: "2026-08-02", people: ["Nora", "Leo"] },
    { id: "c", chapter: 1, date: "2026-08-04" },
  ],
};

test("a story is a title, a card for each chapter with its photos, and an end", () => {
  const s = buildTimeline(doc, fmt);
  assert.deepEqual(s.map((x) => x.kind), ["title", "chapter", "photo", "photo", "chapter", "photo", "end"]);
  assert.equal(s[0]?.kind === "title" && s[0].range, "<2026-08-01> – <2026-08-04>");
  assert.equal(s[1]?.kind === "chapter" && s[1].title, "Lisbon");
  assert.equal(s[1]?.kind === "chapter" && s[1].range, "<2026-08-01> – <2026-08-03>");
  assert.equal(s[4]?.kind === "chapter" && s[4].range, "<2026-08-04>");
  assert.equal(s[2]?.kind === "photo" && s[2].caption, "<2026-08-01> · Lisbon, Portugal");
  assert.equal(s[3]?.kind === "photo" && s[3].caption, "<2026-08-02> · Nora, Leo");
  assert.equal(s[6]?.kind === "end" && s[6].count, 3);
  assert.equal(s[0]?.ms, DURATIONS.title);
  assert.equal(s[2]?.ms, DURATIONS.photo);
});

test("a story with one chapter and no name has no chapter card, and without dates there is no range", () => {
  const one: Doc = { title: "Day", chapters: [{ title: "" }], scenes: [{ id: "x", chapter: 0 }, { id: "y", chapter: 0 }] };
  const s = buildTimeline(one, fmt);
  assert.deepEqual(s.map((x) => x.kind), ["title", "photo", "photo", "end"]);
  assert.equal(s[0]?.kind === "title" && s[0].range, "");
  assert.equal(s[1]?.kind === "photo" && s[1].caption, "");
  assert.equal(rangeText(undefined, undefined, fmt), "");
  assert.equal(rangeText("2026-01-01", "2026-01-01", fmt), "<2026-01-01>");
});

test("moving stays inside the story", () => {
  assert.equal(after(0, 5), 1);
  assert.equal(after(4, 5), 4);
  assert.equal(before(0), 0);
  assert.equal(before(3), 2);
});

test("a tap on the left third goes back and anywhere else goes on; a swipe needs to be long and mostly sideways", () => {
  assert.equal(tapOf(10, 300), "prev");
  assert.equal(tapOf(150, 300), "next");
  assert.equal(tapOf(290, 300), "next");
  assert.equal(swipeOf(-80, 10), "next");
  assert.equal(swipeOf(80, -10), "prev");
  assert.equal(swipeOf(30, 0), "");
  assert.equal(swipeOf(60, 120), "");
});

test("the progress bar goes from the first scene to the last", () => {
  assert.equal(progressOf(0, 10, 0), 0);
  assert.equal(progressOf(5, 10, 0.5), 0.55);
  assert.equal(progressOf(9, 10, 5), 1);
  assert.equal(progressOf(0, 1, 0), 1);
});
