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
