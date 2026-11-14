// A client room's page: the marked pictures open larger, one at a time, with the arrow keys, the buttons or a swipe. Without this
// script each picture is a link to the marked picture itself, so the page still works.

import { t } from "./i18n.ts";

/** The picture after moving `by` places from `from` among `count`, going round at the ends. */
export function step(from: number, by: number, count: number): number {
  if (count <= 0) return 0;
  return (((from + by) % count) + count) % count;
}

export function initRoom(): void {
  const root = document.querySelector<HTMLElement>("[data-room]");
  if (!root) return;
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
    caption.textContent = t("Photo {n} of {total}", { n: at + 1, total: tiles.length });
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
  let startX = 0;
  box.addEventListener("touchstart", (e) => {
    startX = e.changedTouches[0]?.clientX ?? 0;
  }, { passive: true });
  box.addEventListener("touchend", (e) => {
    const dx = (e.changedTouches[0]?.clientX ?? 0) - startX;
    if (Math.abs(dx) > 50) go(dx < 0 ? 1 : -1);
  }, { passive: true });
}
