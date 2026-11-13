// The gallery of an event, on the guest page: the photos the box has kept, newest first, filling in by themselves as
// more are kept, with a heart on each and, when the owner allows it, a way to save the original. Pictures come from the
// app by a photo's place in the box's list, never by an address in Immich. Without this script the page has no gallery:
// everything it does is an addition to the form above it.

export interface GalleryItem {
  id: number;
  name: string;
  caption: string;
  hearts: number;
  mine: boolean;
}

export interface Feed {
  items: GalleryItem[];
  download: boolean;
}

/** Reads what the feed answered; anything that does not look like a feed is no feed. */
export function parseFeed(raw: unknown): Feed | null {
  if (typeof raw !== "object" || raw === null) return null;
  const o = raw as { items?: unknown; download?: unknown };
  if (!Array.isArray(o.items)) return null;
  const items: GalleryItem[] = [];
  for (const x of o.items as unknown[]) {
    if (typeof x !== "object" || x === null) continue;
    const i = x as Record<string, unknown>;
    if (typeof i["id"] !== "number" || !Number.isFinite(i["id"])) continue;
    items.push({
      id: i["id"],
      name: typeof i["name"] === "string" ? i["name"] : "",
      caption: typeof i["caption"] === "string" ? i["caption"] : "",
      hearts: typeof i["hearts"] === "number" && i["hearts"] > 0 ? Math.floor(i["hearts"]) : 0,
      mine: i["mine"] === true,
    });
  }
  return { items, download: o.download === true };
}

/** The photos shown with newer ones put in front, newest first, none twice (a newer answer wins for one that is in both). */
export function mergeNewest(shown: GalleryItem[], incoming: GalleryItem[]): GalleryItem[] {
  const seen = new Set(incoming.map((i) => i.id));
  return [...incoming, ...shown.filter((i) => !seen.has(i.id))].sort((a, b) => b.id - a.id);
}

/** The position of the newest photo shown (0 when there is none), to ask for what came after it. */
export function newestId(shown: GalleryItem[]): number {
  return shown.reduce((m, i) => Math.max(m, i.id), 0);
}

/** The position of the oldest photo shown (0 when there is none), to ask for what came before it. */
export function oldestId(shown: GalleryItem[]): number {
  return shown.length === 0 ? 0 : shown.reduce((m, i) => Math.min(m, i.id), Number.MAX_SAFE_INTEGER);
}

/** A heart count the way it is shown: nothing for none. */
export function heartText(count: number): string {
  return count > 0 ? String(count) : "";
}

const HEART = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 21s-7.5-4.6-9.6-9.2C.9 8.2 3 4.8 6.3 4.8c2 0 3.5 1.1 4.2 2.4h3c.7-1.3 2.2-2.4 4.2-2.4 3.3 0 5.4 3.4 3.9 7C19.5 16.4 12 21 12 21z"/></svg>';

interface Labels {
  love: string;
  unlove: string;
  save: string;
  by: string;
  open: string;
  close: string;
}

function labelsOf(root: HTMLElement): Labels {
  const d = root.dataset;
  return { love: d["love"] ?? "", unlove: d["unlove"] ?? "", save: d["save"] ?? "", by: d["by"] ?? "{name}", open: d["open"] ?? "", close: d["close"] ?? "" };
}

function tile(item: GalleryItem, root: HTMLElement, labels: Labels, download: boolean, show: (src: string, alt: string) => void): HTMLLIElement {
  const li = document.createElement("li");
  li.className = "gal-item";
  li.dataset["id"] = String(item.id);
  const open = document.createElement("button");
  open.type = "button";
  open.className = "gal-open";
  open.setAttribute("aria-label", labels.open);
  const img = document.createElement("img");
  img.loading = "lazy";
  img.alt = item.caption;
  img.src = (root.dataset["photo"] ?? "") + item.id;
  open.append(img);
  open.addEventListener("click", () => show(img.src, item.caption));
  const bar = document.createElement("div");
  bar.className = "gal-bar";
  const by = document.createElement("span");
  by.className = "gal-by";
  by.textContent = item.name ? labels.by.replace("{name}", item.name) : item.caption;
  const heart = document.createElement("button");
  heart.type = "button";
  heart.className = "gal-heart";
  const count = document.createElement("span");
  count.className = "gal-count";
  const paint = (): void => {
    heart.setAttribute("aria-pressed", String(item.mine));
    heart.setAttribute("aria-label", item.mine ? labels.unlove : labels.love);
    count.textContent = heartText(item.hearts);
  };
  heart.innerHTML = HEART;
  heart.append(count);
  paint();
  heart.addEventListener("click", () => {
    void (async () => {
      heart.disabled = true;
      try {
        const res = await fetch((root.dataset["heart"] ?? "") + item.id, { method: "POST" });
        if (!res.ok) return;
        const body: unknown = await res.json();
        if (typeof body === "object" && body !== null) {
          const b = body as { hearted?: unknown; hearts?: unknown };
          item.mine = b.hearted === true;
          item.hearts = typeof b.hearts === "number" ? b.hearts : item.hearts;
          paint();
          if (item.mine) {
            heart.classList.remove("pop");
            void heart.offsetWidth;
            heart.classList.add("pop");
          }
        }
      } catch {
        /* the heart stays as it was; the next tap tries again */
      } finally {
        heart.disabled = false;
      }
    })();
  });
  bar.append(by, heart);
  if (download) {
    const save = document.createElement("a");
    save.className = "gal-save";
    save.href = (root.dataset["original"] ?? "") + item.id;
    save.setAttribute("download", "");
    save.textContent = labels.save;
    bar.append(save);
  }
  li.append(open, bar);
  return li;
}

/** A field marked data-remember keeps what was typed in it in this browser (a guest's name), so it is there next time. */
function initRemember(): void {
  document.querySelectorAll<HTMLInputElement>("input[data-remember]").forEach((input) => {
    const key = "handoff-remember-" + (input.dataset["remember"] ?? "");
    try {
      if (input.value === "") input.value = localStorage.getItem(key) ?? "";
      input.addEventListener("input", () => {
        try {
          localStorage.setItem(key, input.value);
        } catch {
          /* it is not remembered */
        }
      });
    } catch {
      /* storage can be blocked: the field is just a field then */
    }
  });
}

/** Fills the gallery of the page, and keeps it up to date while the page is open. */
export function initGallery(): void {
  initRemember();
  const root = document.querySelector<HTMLElement>("section.gallery[data-gallery]");
  if (!root) return;
  const feedUrl = root.dataset["feed"] ?? "";
  const list = root.querySelector<HTMLUListElement>("ul.gal");
  const empty = root.querySelector<HTMLElement>(".gallery-empty");
  const more = root.querySelector<HTMLButtonElement>("button.gallery-more");
  if (!list || !feedUrl) return;
  const labels = labelsOf(root);
  let download = root.dataset["download"] !== undefined;
  let shown: GalleryItem[] = [];

  // a photo large, in a dialog; Escape and the button close it
  const box = document.createElement("dialog");
  box.className = "gal-lightbox";
  const big = document.createElement("img");
  big.alt = "";
  const close = document.createElement("button");
  close.type = "button";
  close.className = "gal-close";
  close.textContent = labels.close;
  close.addEventListener("click", () => box.close());
  box.addEventListener("click", (e) => {
    if (e.target === box) box.close();
  });
  box.append(big, close);
  document.body.append(box);
  const show = (src: string, alt: string): void => {
    big.src = src;
    big.alt = alt;
    if (typeof box.showModal === "function") box.showModal();
  };

  const ask = async (query: string): Promise<GalleryItem[] | null> => {
    try {
      const res = await fetch(feedUrl + query, { headers: { Accept: "application/json" } });
      if (!res.ok) return null;
      const feed = parseFeed(await res.json());
      if (!feed) return null;
      download = feed.download;
      return feed.items;
    } catch {
      return null; // offline or the link ended: try again at the next turn
    }
  };

  // put photos in the list in the order they have: new ones appear at the top
  const render = (fresh: Set<number>): void => {
    const have = new Map<string, HTMLLIElement>();
    list.querySelectorAll<HTMLLIElement>("li.gal-item").forEach((li) => have.set(li.dataset["id"] ?? "", li));
    let after: Element | null = null;
    for (const item of shown) {
      let li = have.get(String(item.id));
      if (!li) {
        li = tile(item, root, labels, download, show);
        if (fresh.has(item.id)) li.classList.add("new");
      }
      if (li.previousElementSibling !== after || li.parentElement !== list) list.insertBefore(li, after ? after.nextSibling : list.firstChild);
      after = li;
    }
    if (empty) empty.hidden = shown.length > 0;
  };

  void (async () => {
    const first = await ask("");
    if (first) {
      shown = mergeNewest([], first);
      render(new Set());
      if (more) more.hidden = first.length < 48;
    }
  })();

  more?.addEventListener("click", () => {
    void (async () => {
      const older = await ask("?before=" + oldestId(shown));
      if (!older) return;
      shown = mergeNewest(shown, older);
      render(new Set());
      more.hidden = older.length < 48;
    })();
  });

  setInterval(() => {
    if (document.hidden) return;
    void (async () => {
      const since = newestId(shown);
      const newer = await ask(since > 0 ? "?after=" + since : "");
      if (!newer || newer.length === 0) return;
      shown = mergeNewest(shown, newer);
      render(new Set(newer.map((i) => i.id)));
    })();
  }, 8000);
}
