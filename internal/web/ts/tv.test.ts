// Run by `npm test` in tools/typecheck.
import assert from "node:assert/strict";
import { test } from "node:test";
import { phoneAddress } from "./tv-core.ts";

test("the address to type on the phone is the TV page's address without /tv or /tv/legacy", () => {
  assert.equal(phoneAddress({ host: "photos.example.com", pathname: "/tv" }), "photos.example.com/");
  assert.equal(phoneAddress({ host: "photos.example.com", pathname: "/tv/" }), "photos.example.com/");
  assert.equal(phoneAddress({ host: "photos.example.com", pathname: "/tv/legacy" }), "photos.example.com/");
  assert.equal(phoneAddress({ host: "nas.local:8080", pathname: "/handoff/tv" }), "nas.local:8080/handoff/");
  assert.equal(phoneAddress({ host: "tv.example.com", pathname: "/" }), "tv.example.com/");
});
