import assert from "node:assert/strict";
import { test } from "node:test";
import { newItem, type Item, type State } from "./send-core.ts";
import { form, lines, plan, type Texts } from "./send-notify.ts";

const item = (state: State, ended = 0): Item => ({ ...newItem("x", "a.jpg", 1, 0, "", "me", 0), state, ended });

test("the person is told once the line is empty, not while anything is still on its way", () => {
  assert.equal(plan([item("done", 5), item("sending")], 0, "all"), null);
  assert.equal(plan([item("done", 5), item("offline")], 0, "all"), null);
  assert.equal(plan([item("done", 5), item("waiting")], 0, "all"), null);
  assert.deepEqual(plan([item("done", 5), item("done", 6)], 0, "all"), { arrived: 2, failed: 0, signin: 0 });
});

test("only what ended since the last message is told, so a second batch is its own message", () => {
  assert.deepEqual(plan([item("done", 5), item("done", 20), item("failed", 21)], 10, "all"), { arrived: 1, failed: 1, signin: 0 });
  assert.equal(plan([item("done", 5)], 10, "all"), null);
});

test("a person who asked for trouble only is not told of arrivals, and one who asked for nothing is told nothing", () => {
  assert.equal(plan([item("done", 5)], 0, "problems"), null);
  assert.deepEqual(plan([item("done", 5), item("failed", 6)], 0, "problems"), { arrived: 0, failed: 1, signin: 0 });
  assert.equal(plan([item("done", 5), item("failed", 6)], 0, "none"), null);
});

test("a file that waits for a sign-in is trouble, and cancelled or shared files are nothing", () => {
  assert.deepEqual(plan([item("signin")], 0, "problems"), { arrived: 0, failed: 0, signin: 1 });
  assert.equal(plan([item("cancelled", 5), item("pick")], 0, "all"), null);
});

const texts: Texts = {
  lang: "en",
  title: "Photos from this device",
  arrived: { one: "{n} photo arrived in your Immich", other: "{n} photos arrived in your Immich" },
  failed: { one: "{n} photo could not be sent", other: "{n} photos could not be sent" },
  signin: { one: "{n} photo is waiting for you to sign in again", other: "{n} photos are waiting for you to sign in again" },
};

test("the words follow the language's plural forms", () => {
  assert.equal(form(texts.arrived, "en", 1), "1 photo arrived in your Immich");
  assert.equal(form(texts.arrived, "en", 40), "40 photos arrived in your Immich");
  const ru = { one: "{n} фото", few: "{n} фото (few)", many: "{n} фото (many)", other: "{n} фото (other)" };
  assert.equal(form(ru, "ru", 21), "21 фото");
  assert.equal(form(ru, "ru", 3), "3 фото (few)");
  assert.equal(form(ru, "ru", 5), "5 фото (many)");
  assert.equal(form({ one: "{n}x" }, "nonsense-!!", 4), "4x");
});

test("a big batch is one message with a line for each thing", () => {
  assert.deepEqual(lines(texts, { arrived: 38, failed: 2, signin: 0 }), ["38 photos arrived in your Immich", "2 photos could not be sent"]);
  assert.deepEqual(lines(texts, { arrived: 0, failed: 0, signin: 1 }), ["1 photo is waiting for you to sign in again"]);
});
