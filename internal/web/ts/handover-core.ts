// What the hand-over button works out that does not need a page: which photo the viewer shows, and (further down)
// which photos are selected. Kept apart so it can be tested without a browser (handover-core.test.ts).

const photoPath = /\/photos\/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\/?$/i;

/** The id of the photo in the viewer's address (/photos/<id>, also inside an album, a person or a place), or null. */
export function assetOfPath(path: string): string | null {
  return photoPath.exec(path)?.[1]?.toLowerCase() ?? null;
}

/** What the app says about one service the person sends to. */
export interface Service {
  name: string;
  copy: boolean;
  note?: string;
}

/** What the app sends when the button first shows. */
export interface Info {
  words: Record<string, string>;
  services: Service[];
}

/** The button's words: the app's, in the person's language, or English where the app did not send one. */
export function wordsOf(info: Info | null): (id: string) => string {
  return (id) => info?.words[id] || id;
}

/** The form a press or an undo sends: the service and every photo, one `asset` each. */
export function formOf(service: string, assets: string[]): URLSearchParams {
  const form = new URLSearchParams({ service });
  for (const a of assets) form.append("asset", a);
  return form;
}

/**
 * The photos selected in Immich's grid. Immich draws only the tiles near the screen, so a selected tile that is
 * scrolled away is not in the page (and "Select all" selects photos that were never drawn). The page is therefore
 * watched while the person selects, and what was seen is kept; the number Immich shows is the check: the set is
 * trusted only when it is as big as that number.
 */
export class Selection {
  private seen = new Set<string>();

  /** The tiles in the page now: a selected one is added, one that is drawn and not selected is dropped. */
  see(tiles: { id: string; selected: boolean }[]): void {
    for (const t of tiles) {
      if (t.selected) this.seen.add(t.id);
      else this.seen.delete(t.id);
    }
  }

  /** The selection is over. */
  clear(): void {
    this.seen.clear();
  }

  get size(): number {
    return this.seen.size;
  }

  /** The ids, if they are exactly the photos Immich says are selected; null when they cannot be known. */
  idsFor(counted: number | null): string[] | null {
    return counted !== null && counted > 0 && counted === this.seen.size ? [...this.seen] : null;
  }
}

/** The number Immich shows in the selection bar, from the texts of the bar's paragraphs: the one that is only a number. */
export function countIn(paragraphs: string[]): number | null {
  for (const p of paragraphs) {
    const text = p.trim();
    if (/^\d{1,7}$/.test(text)) return Number(text);
  }
  return null;
}
