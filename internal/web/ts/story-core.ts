// What a story's player decides without a browser: which scenes there are, how long each stays, and how to move between them.

export interface Doc {
  title: string;
  chapters: { title?: string; from?: string; to?: string }[];
  scenes: { id: string; chapter: number; date?: string; place?: string; people?: string[] }[];
  dates?: boolean;
  places?: boolean;
}

export type Scene =
  | { kind: "title"; ms: number; title: string; range: string }
  | { kind: "chapter"; ms: number; index: number; title: string; range: string }
  | { kind: "photo"; ms: number; id: string; caption: string; chapter: number }
  | { kind: "end"; ms: number; count: number; title: string };

/** How long each kind of scene stays, in milliseconds. */
export const DURATIONS = { title: 3500, chapter: 3000, photo: 6000, end: 5000 } as const;

/** "2 Mar 2026" to "9 Mar 2026" in words, from two YYYY-MM-DD days; one day, or nothing, when that is all there is. */
export function rangeText(from: string | undefined, to: string | undefined, fmt: (day: string) => string): string {
  if (!from) return "";
  if (!to || to === from) return fmt(from);
  return `${fmt(from)} – ${fmt(to)}`;
}

/** The whole story as a list of scenes: a title, then for each chapter a card (when the story has more than one chapter, or this
 *  one has a name) and its photos in order, then an end. `fmt` writes a day the way the reader's language does. */
export function buildTimeline(doc: Doc, fmt: (day: string) => string): Scene[] {
  const out: Scene[] = [];
  let first = "";
  let last = "";
  for (const c of doc.chapters) {
    if (c.from && (first === "" || c.from < first)) first = c.from;
    if (c.to && c.to > last) last = c.to;
  }
  out.push({ kind: "title", ms: DURATIONS.title, title: doc.title, range: rangeText(first, last, fmt) });
  let shown = -1;
  const named = doc.chapters.length > 1 || doc.chapters.some((c) => (c.title ?? "") !== "");
  doc.scenes.forEach((s) => {
    if (named && s.chapter !== shown) {
      shown = s.chapter;
      const c = doc.chapters[s.chapter];
      const range = rangeText(c?.from, c?.to, fmt);
      out.push({ kind: "chapter", ms: DURATIONS.chapter, index: s.chapter, title: c?.title ?? "", range });
    }
    const words = [s.date ? fmt(s.date) : "", s.place ?? "", (s.people ?? []).join(", ")].filter((x) => x !== "");
    out.push({ kind: "photo", ms: DURATIONS.photo, id: s.id, caption: words.join(" · "), chapter: s.chapter });
  });
  out.push({ kind: "end", ms: DURATIONS.end, count: doc.scenes.length, title: doc.title });
  return out;
}

/** The scene after i; after the end it stays at the end. */
export function after(i: number, length: number): number {
  return Math.min(length - 1, i + 1);
}

/** The scene before i; before the first it stays at the first. A second press while a scene has just begun goes to the one before. */
export function before(i: number): number {
  return Math.max(0, i - 1);
}

/** Which way a touch moved: "next" for a swipe to the left, "prev" for a swipe to the right, "" for anything shorter than 50 pixels
 *  or more up and down than sideways. */
export function swipeOf(dx: number, dy: number): "next" | "prev" | "" {
  if (Math.abs(dx) < 50 || Math.abs(dy) > Math.abs(dx)) return "";
  return dx < 0 ? "next" : "prev";
}

/** What a tap at x of width w does: the left third goes back, the rest goes on. */
export function tapOf(x: number, w: number): "next" | "prev" {
  return w > 0 && x < w / 3 ? "prev" : "next";
}

/** How far through the story the player is, 0 to 1, with `inside` the part (0 to 1) of the current scene that has passed. */
export function progressOf(i: number, length: number, inside: number): number {
  if (length <= 1) return 1;
  return Math.max(0, Math.min(1, (i + Math.max(0, Math.min(1, inside))) / length));
}
