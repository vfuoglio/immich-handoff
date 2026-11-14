import assert from "node:assert/strict";
import { test } from "node:test";
import { backoff, finished, looksLikeMedia, newItem, nextToSend, percent, pieceSize, sizeText, tally, verdict } from "./send-core.ts";

test("a reply says what to do next", () => {
  assert.deepEqual(verdict(200, "{}"), { kind: "ok" });
  assert.deepEqual(verdict(201, "{}"), { kind: "ok" });
  assert.deepEqual(verdict(409, '{"offset":1200}'), { kind: "offset", offset: 1200 });
  assert.deepEqual(verdict(404, "gone"), { kind: "gone" });
  assert.deepEqual(verdict(401, ""), { kind: "signin" });
  assert.deepEqual(verdict(403, ""), { kind: "signin" });
  for (const code of [408, 429, 500, 502, 503]) assert.deepEqual(verdict(code, ""), { kind: "retry" }, String(code));
  assert.deepEqual(verdict(422, "  only photos and videos can be sent \n"), { kind: "fail", text: "only photos and videos can be sent" });
  // a 409 that is words, not an offset, is a refusal
  assert.deepEqual(verdict(409, "too many"), { kind: "fail", text: "too many" });
  assert.equal(verdict(422, "x".repeat(1000)).kind, "fail");
});

test("waiting doubles from two seconds up to five minutes", () => {
  assert.deepEqual([1, 2, 3, 4].map(backoff), [2000, 4000, 8000, 16000]);
  assert.equal(backoff(0), 2000);
  assert.equal(backoff(50), 300000);
});

test("a piece is what the server asks for, but at most 4 MiB", () => {
  assert.equal(pieceSize(8 << 20), 4 << 20);
  assert.equal(pieceSize(1 << 20), 1 << 20);
  assert.equal(pieceSize(0), 4 << 20);
});

test("sizes read like a person would say them", () => {
  assert.equal(sizeText(300), "1 KB");
  assert.equal(sizeText(800 * 1024), "800 KB");
  assert.equal(sizeText(5.5 * 1024 * 1024), "5.5 MB");
  assert.equal(sizeText(124 * 1024 * 1024), "124 MB");
  assert.equal(sizeText(1.3 * 1024 ** 3), "1.3 GB");
});

test("photos and videos are told by type or by name, anything else is not", () => {
  assert.ok(looksLikeMedia("a.bin", "image/heic"));
  assert.ok(looksLikeMedia("IMG_1.HEIC", ""));
  assert.ok(looksLikeMedia("clip.MOV", ""));
  assert.ok(looksLikeMedia("raw.dng", "application/octet-stream"));
  assert.ok(!looksLikeMedia("notes.pdf", "application/pdf"));
  assert.ok(!looksLikeMedia("page.html", "text/html"));
});

test("progress is a whole number between 0 and 100", () => {
  assert.equal(percent(50, 200), 25);
  assert.equal(percent(0, 0), 0);
  assert.equal(percent(300, 200), 100);
  assert.equal(percent(-1, 200), 0);
});

test("the queue takes what waits, or waits for the network, in order, and tells how many are left", () => {
  const mk = (state: string) => ({ ...newItem("x", "a.jpg", 10, 0, "", 0), state }) as ReturnType<typeof newItem>;
  assert.equal(nextToSend([mk("done"), mk("failed"), mk("offline"), mk("waiting")]), 2);
  assert.equal(nextToSend([mk("sending"), mk("signin"), mk("cancelled")]), -1);
  assert.deepEqual(tally([mk("done"), mk("failed"), mk("waiting"), mk("sending"), mk("cancelled")]), { done: 1, failed: 1, open: 2, total: 4 });
  assert.ok(finished("done") && finished("failed") && finished("cancelled") && !finished("offline"));
});
