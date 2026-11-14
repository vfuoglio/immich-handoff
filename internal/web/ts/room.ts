// A client room's page: the marked pictures open larger, one at a time, with the arrow keys, the buttons or a swipe. Without this
// script each picture is a link to the marked picture itself, so the page still works.

import { ask } from "./ask.ts";
import { t } from "./i18n.ts";

/** The picture after moving `by` places from `from` among `count`, going round at the ends. */
export function step(from: number, by: number, count: number): number {
  if (count <= 0) return 0;
  return (((from + by) % count) + count) % count;
}

export function initRoom(): void {
  const root = document.querySelector<HTMLElement>("[data-room]");
  if (!root) return;
  const page: HTMLElement = root;
  const tiles = Array.from(root.querySelectorAll<HTMLAnchorElement>("a.room-tile"));
  if (tiles.length === 0) return;

  const box = document.createElement("dialog");
  box.className = "gal-lightbox room-lightbox";
  const picture = document.createElement("img");
  picture.alt = "";
  const caption = document.createElement("p");
  caption.className = "meta room-caption";
  caption.setAttribute("aria-live", "polite");
  const bar = document.createElement("p");
  bar.className = "row room-bar";
  const prev = document.createElement("button");
  prev.type = "button";
  prev.textContent = "‹";
  prev.setAttribute("aria-label", t("Previous photo"));
  const next = document.createElement("button");
  next.type = "button";
  next.textContent = "›";
  next.setAttribute("aria-label", t("Next photo"));
  const close = document.createElement("button");
  close.type = "button";
  close.textContent = t("Close");
  bar.append(prev, close, next);
  box.append(picture, caption, bar);
  document.body.append(box);

  let at = 0;
  const show = (i: number): void => {
    at = step(i, 0, tiles.length);
    const tile = tiles[at]!;
    picture.src = tile.href;
    const total = Number(page.dataset["total"] ?? "0");
    caption.textContent = t("Photo {n} of {total}", { n: Number(tile.dataset["n"] ?? at + 1), total: total > 0 ? total : tiles.length });
    refreshLightbox();
  };
  const go = (by: number): void => show(step(at, by, tiles.length));

  tiles.forEach((tile, i) =>
    tile.addEventListener("click", (e) => {
      e.preventDefault();
      show(i);
      if (!box.open) box.showModal();
    }),
  );
  prev.addEventListener("click", () => go(-1));
  next.addEventListener("click", () => go(1));
  close.addEventListener("click", () => box.close());
  box.addEventListener("click", (e) => {
    if (e.target === box) box.close();
  });
  box.addEventListener("keydown", (e) => {
    if (e.key === "ArrowLeft") go(-1);
    else if (e.key === "ArrowRight") go(1);
  });

  // choosing: each press is a form (so it works without this script); here it is sent without leaving the page
  const message = document.getElementById("room-msg");
  const counter = root.querySelector<HTMLElement>("[data-room-count]");
  const say = (text: string): void => {
    if (!message) return;
    message.textContent = text;
    message.hidden = text === "";
  };
  interface Answer {
    count?: number;
    done?: boolean;
    text?: string;
    picked?: boolean;
    error?: string;
  }
  const post = async (url: string): Promise<{ ok: boolean; body: Answer }> => {
    try {
      const r = await fetch(url, { method: "POST", headers: { Accept: "application/json" }, credentials: "same-origin" });
      return { ok: r.ok, body: (await r.json()) as Answer };
    } catch {
      return { ok: false, body: {} };
    }
  };
  const setPicked = (form: HTMLFormElement, on: boolean): void => {
    const button = form.querySelector<HTMLButtonElement>("button.room-heart");
    form.closest("li")?.classList.toggle("picked", on);
    if (!button) return;
    button.setAttribute("aria-pressed", String(on));
    const n = form.closest("li")?.querySelector<HTMLElement>("a.room-tile")?.dataset["n"] ?? "";
    const label = on ? root.dataset["chosen"] ?? "" : root.dataset["choose"] ?? "";
    button.title = label;
    button.setAttribute("aria-label", n === "" ? label : `${label} (${n})`);
  };
  const pickForms = Array.from(root.querySelectorAll<HTMLFormElement>("form.room-pick"));
  pickForms.forEach((form) =>
    form.addEventListener("submit", (e) => {
      e.preventDefault();
      void post(form.action).then(({ ok, body }) => {
        if (body.text !== undefined && counter) counter.textContent = body.text;
        if (ok) {
          say("");
          setPicked(form, body.picked === true);
        } else if (body.error) say(body.error);
        refreshLightbox();
      });
    }),
  );
  root.querySelector<HTMLFormElement>("form.room-done")?.addEventListener("submit", (e) => {
    const form = e.currentTarget as HTMLFormElement;
    e.preventDefault();
    const limit = Number(form.dataset["limit"] ?? "0");
    const count = root.querySelectorAll("li.picked").length;
    const send = (): void => {
      void post(form.action).then(({ ok, body }) => {
        if (ok) location.reload();
        else if (body.error) say(body.error);
      });
    };
    if (limit > 0 && count > 0 && count < limit) {
      const text = (form.dataset["short"] ?? "").replace("{n}", String(count)).replace("{limit}", String(limit));
      void ask(form.dataset["title"] ?? "", text, form.dataset["no"] ?? "", form.dataset["yes"] ?? "").then((yes) => {
        if (yes) send();
      });
    } else send();
  });

  // in the large view the photo can be chosen too, by the button of its tile
  const heart = document.createElement("button");
  heart.type = "button";
  heart.className = "room-heart-big";
  heart.hidden = root.dataset["canPick"] === undefined;
  heart.addEventListener("click", () => tiles[at]?.closest("li")?.querySelector<HTMLButtonElement>("form.room-pick button")?.click());
  bar.append(heart);

  // notes on photos (when the room takes them): written in the large view, kept with the photo
  const noteBox = document.createElement("div");
  noteBox.className = "room-noteform";
  noteBox.hidden = (page.dataset["notes"] ?? "") === "";
  const noteLabel = document.createElement("label");
  noteLabel.textContent = page.dataset["noteLabel"] ?? "";
  const noteArea = document.createElement("textarea");
  noteArea.rows = 2;
  noteArea.maxLength = 280;
  noteArea.placeholder = page.dataset["noteHint"] ?? "";
  noteArea.id = "room-note-area";
  noteLabel.htmlFor = noteArea.id;
  const noteSave = document.createElement("button");
  noteSave.type = "button";
  noteSave.textContent = page.dataset["noteSave"] ?? "";
  const noteStatus = document.createElement("p");
  noteStatus.className = "meta room-note-status";
  noteStatus.setAttribute("role", "status");
  const noteReply = document.createElement("p");
  noteReply.className = "room-reply-text";
  noteBox.append(noteLabel, noteArea, noteSave, noteStatus, noteReply);
  box.insertBefore(noteBox, bar);
  noteSave.addEventListener("click", () => {
    const tile = tiles[at];
    const li = tile?.closest("li");
    if (!tile || !li) return;
    const url = new URL(tile.href).pathname.replace("/photo/", "/note/");
    noteSave.disabled = true;
    void fetch(url, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/x-www-form-urlencoded" }, body: new URLSearchParams({ text: noteArea.value }) })
      .then(async (r) => ({ ok: r.ok, body: (await r.json().catch(() => ({}))) as { error?: string } }))
      .then(({ ok, body }) => {
        if (ok) {
          li.dataset["note"] = noteArea.value.trim();
          li.dataset["reply"] = "";
          li.classList.toggle("noted", li.dataset["note"] !== "");
          const has = tile.querySelector(".room-note");
          if (li.dataset["note"] !== "" && !has) {
            const mark = document.createElement("span");
            mark.className = "room-note";
            mark.setAttribute("aria-hidden", "true");
            mark.textContent = "✎";
            tile.append(mark);
          } else if (li.dataset["note"] === "" && has) has.remove();
          noteStatus.textContent = page.dataset["noteSaved"] ?? "";
          noteReply.textContent = "";
        } else noteStatus.textContent = body.error ?? "";
      })
      .finally(() => {
        noteSave.disabled = false;
      });
  });
  function refreshLightbox(): void {
    const li = tiles[at]?.closest("li");
    const on = li?.classList.contains("picked") === true;
    heart.setAttribute("aria-pressed", String(on));
    heart.textContent = on ? page.dataset["chosen"] ?? "" : page.dataset["choose"] ?? "";
    heart.hidden = page.dataset["canPick"] === undefined;
    const mine = li?.dataset["note"] ?? "";
    noteArea.value = mine;
    noteArea.readOnly = page.dataset["canPick"] === undefined;
    noteSave.hidden = noteArea.readOnly;
    noteStatus.textContent = "";
    const reply = li?.dataset["reply"] ?? "";
    noteReply.textContent = reply === "" ? "" : `${page.dataset["replyLabel"] ?? ""} ${reply}`;
    noteBox.hidden = (page.dataset["notes"] ?? "") === "" || (noteArea.readOnly && mine === "");
  }
  let startX = 0;
  box.addEventListener("touchstart", (e) => {
    startX = e.changedTouches[0]?.clientX ?? 0;
  }, { passive: true });
  box.addEventListener("touchend", (e) => {
    const dx = (e.changedTouches[0]?.clientX ?? 0) - startX;
    if (Math.abs(dx) > 50) go(dx < 0 ? 1 : -1);
  }, { passive: true });
}
