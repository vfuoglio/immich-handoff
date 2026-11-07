// Run by `npm test` in tools/typecheck.
import assert from "node:assert/strict";
import { test } from "node:test";
import { fileName } from "./backup.ts";

test("the saved file is named as the server suggests", () => {
  assert.equal(fileName('attachment; filename="handoff-20261107-120000.db"'), "handoff-20261107-120000.db");
});

test("a missing or odd name falls back to a plain one, and never a path", () => {
  assert.equal(fileName(null), "handoff-backup.db");
  assert.equal(fileName("attachment"), "handoff-backup.db");
  assert.equal(fileName('attachment; filename="../etc/passwd"'), "handoff-backup.db");
});
