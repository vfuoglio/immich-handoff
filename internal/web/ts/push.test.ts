// Run by `npm test` in tools/typecheck.
import assert from "node:assert/strict";
import { test } from "node:test";
import { deviceId, support, urlBase64ToBytes } from "./push.ts";
import type { Env } from "./push.ts";
import { appRoot, readPush, targetUrl } from "./pushmsg.ts";

const capable: Env = { secure: true, serviceWorker: true, pushManager: true, notification: true, ios: false, standalone: false };

test("a capable browser on a secure page can be sent pushes", () => {
  assert.equal(support(capable), "ok");
});

test("the reason a browser cannot is given in the order a person can fix it", () => {
  assert.equal(support({ ...capable, secure: false, serviceWorker: false }), "insecure");
  assert.equal(support({ ...capable, ios: true, pushManager: false }), "install");
  assert.equal(support({ ...capable, ios: true, standalone: true }), "ok");
  assert.equal(support({ ...capable, pushManager: false }), "unsupported");
  assert.equal(support({ ...capable, notification: false }), "unsupported");
});

test("a key written for the web becomes the bytes the browser wants", () => {
  assert.deepEqual(Array.from(urlBase64ToBytes("AQID")), [1, 2, 3]);
  assert.deepEqual(Array.from(urlBase64ToBytes("-_8")), [251, 255]); // - and _ stand for + and /, and the padding is optional
  assert.equal(urlBase64ToBytes("").length, 0);
});

test("a device's id is the hash the server makes of its address (the same value Go's store.PushID gives)", async () => {
  assert.equal(await deviceId("abc"), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
});

test("a push is read leniently: whatever arrives is shown", () => {
  assert.deepEqual(readPush({ title: "T", body: "B", tag: "x", url: "./admin" }), { title: "T", body: "B", tag: "x", url: "./admin" });
  assert.deepEqual(readPush(null), { title: "Immich Handoff", body: "", tag: "handoff", url: "./" });
  assert.deepEqual(readPush("text"), { title: "Immich Handoff", body: "", tag: "handoff", url: "./" });
  assert.equal(readPush({ title: 5, body: ["x"] }).title, "Immich Handoff");
});

test("a click opens the app's own front page, whatever folder it lives in, and never another site", () => {
  assert.equal(appRoot("https://h.example/static/"), "https://h.example/");
  assert.equal(appRoot("https://h.example/handoff/static/"), "https://h.example/handoff/");
  assert.equal(targetUrl("https://h.example/handoff/static/", "./"), "https://h.example/handoff/");
  assert.equal(targetUrl("https://h.example/static/", "./admin"), "https://h.example/admin");
  assert.equal(targetUrl("https://h.example/static/", "https://evil.example/phish"), "https://h.example/");
  assert.equal(targetUrl("https://h.example/static/", "//evil.example/x"), "https://h.example/");
  assert.equal(targetUrl("https://h.example/static/", "http://[bad"), "https://h.example/");
});
