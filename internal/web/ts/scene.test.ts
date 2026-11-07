// Run by `npm test` in tools/typecheck: what a lane shows and what the log says, for every kind of live event.
import assert from "node:assert/strict";
import { test } from "node:test";
import type { LiveEvent } from "./live.ts";
import { applyEvent, describe, flights, laneState, newFlags, stateText, wholeService } from "./scene.ts";

const at = "2026-11-06T21:00:00Z";
const ev = (e: Partial<LiveEvent> & Pick<LiveEvent, "kind">): LiveEvent => ({ at, service: "club", sender: "Bob", ...e });
/** An event about a whole service: no sender. */
const wide = (kind: LiveEvent["kind"], reason?: string): LiveEvent => {
  const e: LiveEvent = { kind, at, service: "club" };
  if (reason !== undefined) e.reason = reason;
  return e;
};

test("a lane starts flowing, and photos being sent show as sending", () => {
  let f = newFlags();
  assert.equal(laneState(f), "flow");
  assert.equal(stateText(f), "Flowing");
  f = applyEvent(f, ev({ kind: "started", count: 3 }));
  assert.equal(stateText(f), "Sending 3");
  f = applyEvent(f, ev({ kind: "handed", count: 3 }));
  assert.equal(f.sending, 0);
  assert.equal(stateText(f), "Flowing");
});

test("sending never goes below zero, whatever order events arrive in", () => {
  const f = applyEvent(newFlags(), ev({ kind: "handed", count: 5 }));
  assert.equal(f.sending, 0);
});

test("pausing: by the owner or by the sender, each says who, and resuming undoes only its own", () => {
  let f = applyEvent(newFlags(), ev({ kind: "paused", reason: "sender" }));
  assert.equal(laneState(f), "paused");
  assert.equal(stateText(f), "Paused by the sender");
  f = applyEvent(f, wide("paused", "service"));
  assert.equal(stateText(f), "Paused by the owner");
  f = applyEvent(f, wide("resumed", "service"));
  assert.equal(laneState(f), "paused", "the sender's own pause is still on");
  f = applyEvent(f, ev({ kind: "resumed", reason: "sender" }));
  assert.equal(laneState(f), "flow");
});

test("the daily limit holds photos, says how many and until when, and lets go once photos are handed over", () => {
  let f = applyEvent(newFlags(), ev({ kind: "held", reason: "limit", count: 2, until: "2026-11-07T09:30:00Z" }));
  assert.equal(laneState(f), "capped");
  assert.match(stateText(f), /^Daily limit reached: 2 photos wait until \d/);
  f = applyEvent(f, ev({ kind: "held", reason: "limit", count: 1, until: "2026-11-07T09:30:00Z" }));
  assert.match(stateText(f), /1 photo waits/);
  f = applyEvent(f, ev({ kind: "handed", count: 1 }));
  assert.equal(laneState(f), "flow");
  assert.equal(f.waiting, 0);
  assert.equal(f.until, undefined);
});

test("a limit event without details still says the limit was reached", () => {
  const f = applyEvent(newFlags(), ev({ kind: "limit" }));
  assert.equal(stateText(f), "Daily limit reached: Photos wait");
});

test("a lost connection beats a pause, which beats the limit, and restoring clears it", () => {
  let f = applyEvent(newFlags(), ev({ kind: "held", count: 1 }));
  f = applyEvent(f, ev({ kind: "paused", reason: "sender" }));
  assert.equal(laneState(f), "paused");
  f = applyEvent(f, ev({ kind: "lost", reason: "sender" }));
  assert.equal(laneState(f), "broken");
  assert.equal(stateText(f), "Connection lost");
  f = applyEvent(f, wide("restored", "service"));
  assert.equal(laneState(f), "paused");
});

test("a failed or refused photo stops counting as being sent, and does not clear a limit", () => {
  let f = applyEvent(newFlags(), ev({ kind: "started", count: 2 }));
  f = applyEvent(f, ev({ kind: "held", count: 1 }));
  f = applyEvent(f, ev({ kind: "failed", count: 1 }));
  f = applyEvent(f, ev({ kind: "refused", count: 1 }));
  assert.equal(f.sending, 0);
  assert.equal(f.capped, true);
});

test("events about a whole service touch every lane; the others only the sender's", () => {
  assert.equal(wholeService(wide("paused", "service")), true);
  assert.equal(wholeService(wide("lost", "service")), true);
  assert.equal(wholeService(wide("restored")), true);
  assert.equal(wholeService(ev({ kind: "paused", reason: "sender" })), false);
  assert.equal(wholeService(ev({ kind: "handed" })), false);
});

test("applying an event never changes the flags it was given", () => {
  const before = newFlags();
  const copy = { ...before };
  applyEvent(before, ev({ kind: "lost" }));
  assert.deepEqual(before, copy);
});

test("every event has a sentence, and none names a photo", () => {
  const kinds = ["started", "handed", "refused", "failed", "held", "lost", "restored", "paused", "resumed", "limit", "resync"] as const;
  for (const kind of kinds) {
    for (const reason of [undefined, "service", "sender", "duplicate"]) {
      const text = describe(ev(reason === undefined ? { kind } : { kind, reason }));
      assert.ok(text.length > 8, `${kind}/${reason}`);
      assert.doesNotMatch(text, /\.jpg|\.heic|\.mov|[0-9a-f]{8}-[0-9a-f]{4}-/i);
    }
  }
  assert.equal(describe(ev({ kind: "handed", count: 3 })), "Bob handed over 3 photos to club");
  assert.equal(describe(ev({ kind: "handed", reason: "duplicate" })), "Bob: 1 photo already there in club");
  assert.equal(describe(wide("paused", "service")), "club was paused by its owner");
  assert.equal(describe(ev({ kind: "lost", reason: "sender" })), "Bob's connection to club was lost; no photo was touched");
});

test("a big burst animates only a few tokens, and counts the rest without one", () => {
  assert.deepEqual(flights(1, 4), { shown: 1, extra: 0 });
  assert.deepEqual(flights(3, 4), { shown: 3, extra: 0 });
  assert.deepEqual(flights(200, 4), { shown: 4, extra: 196 });
  assert.deepEqual(flights(0, 4), { shown: 1, extra: 0 });
});
