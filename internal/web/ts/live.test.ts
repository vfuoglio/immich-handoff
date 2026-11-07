// Run by `npm test` in tools/typecheck (Node's built-in test runner; no browser needed).
import assert from "node:assert/strict";
import { test } from "node:test";
import { parseLive } from "./live.ts";

test("reads a well-formed event with everything it can carry", () => {
  const event = parseLive(
    '{"kind":"held","service":"club","sender":"bob","reason":"limit","count":3,"until":"2026-11-07T09:30:00Z","at":"2026-11-06T21:00:00Z"}',
  );
  assert.deepEqual(event, {
    kind: "held",
    service: "club",
    sender: "bob",
    reason: "limit",
    count: 3,
    until: "2026-11-07T09:30:00Z",
    at: "2026-11-06T21:00:00Z",
  });
});

test("reads a bare event", () => {
  assert.deepEqual(parseLive('{"kind":"resync","at":"2026-11-06T21:00:00Z"}'), { kind: "resync", at: "2026-11-06T21:00:00Z" });
});

test("every kind the server sends is accepted", () => {
  for (const kind of ["started", "handed", "refused", "failed", "held", "lost", "restored", "paused", "resumed", "limit", "resync"]) {
    assert.equal(parseLive(`{"kind":"${kind}","at":"x"}`)?.kind, kind);
  }
});

test("anything that is not a well-formed event is ignored, never thrown", () => {
  for (const bad of [
    "",
    "not json",
    "null",
    "42",
    "[]",
    "{}",
    '{"kind":"exploded","at":"x"}',
    '{"kind":"handed"}',
    '{"kind":7,"at":"x"}',
    '{"kind":"handed","at":5}',
  ]) {
    assert.equal(parseLive(bad), null, bad);
  }
});

test("fields of the wrong type are dropped, and fields it does not know are ignored", () => {
  const event = parseLive('{"kind":"handed","at":"x","service":5,"sender":null,"count":"3","until":false,"surprise":"yes"}');
  assert.deepEqual(event, { kind: "handed", at: "x" });
});
