// The first-run tour, the parts that need no page: which stops can be shown, and where the card goes. tour.ts draws it.

export interface Stop {
  target: string;
  title: string;
  text: string;
  url?: string;
}

export interface Target {
  selector?: string;
  view?: string;
  seg?: string;
  cat?: string;
}

/** The stops whose thing is on this page (a stop with nothing to point at is left out, so the count is true: Settings is only there when something is on). */
export function plan(stops: readonly Stop[], targets: Readonly<Record<string, Target>>, present: (selector: string) => boolean): Stop[] {
  return stops.filter((s) => {
    const selector = targets[s.target]?.selector;
    return !!selector && present(selector);
  });
}

export interface Box {
  left: number;
  top: number;
  width: number;
  height: number;
}

export interface Size {
  width: number;
  height: number;
}

const GAP = 12;
const EDGE = 8;

/** Where the card goes (in the window, not the page): under the thing it points at when there is room, else above it, else over the middle; kept inside the window. */
export function place(target: Box, card: Size, view: Size): { left: number; top: number } {
  const clamp = (v: number, lo: number, hi: number): number => Math.max(lo, Math.min(v, Math.max(lo, hi)));
  const left = clamp(target.left + target.width / 2 - card.width / 2, EDGE, view.width - card.width - EDGE);
  const below = target.top + target.height + GAP;
  if (below + card.height + EDGE <= view.height) return { left, top: below };
  const above = target.top - GAP - card.height;
  if (above >= EDGE) return { left, top: above };
  return { left: clamp((view.width - card.width) / 2, EDGE, view.width - card.width - EDGE), top: clamp((view.height - card.height) / 2, EDGE, view.height - card.height - EDGE) };
}

/** The next step of a tour with `count` steps, from `at`: null when it is over. */
export function step(at: number, by: 1 | -1, count: number): number | null {
  const next = at + by;
  if (next < 0) return 0;
  return next >= count ? null : next;
}
