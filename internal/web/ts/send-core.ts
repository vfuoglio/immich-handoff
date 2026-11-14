// What sending photos from a phone decides, kept apart from the browser so it can be tested: how a reply is read, when to
// try again, what a size is called. The network and the storage are in send-run.ts and send-store.ts.

/** What to do after a request to the server. */
export type Verdict =
  | { kind: "ok" }
  | { kind: "offset"; offset: number } // the server is somewhere else: carry on from there
  | { kind: "gone" } // the server forgot the upload (it waited too long): start the file again
  | { kind: "signin" } // not signed in to Immich in this browser (any more)
  | { kind: "retry" } // the network or the server had a bad moment: try again later
  | { kind: "fail"; text: string }; // refused, and trying again will not help

/** Reads the reply to one of the phone endpoints. `body` is the reply's text. */
export function verdict(status: number, body: string): Verdict {
  if (status >= 200 && status < 300) return { kind: "ok" };
  if (status === 409) {
    try {
      const o: unknown = JSON.parse(body);
      if (typeof o === "object" && o !== null && typeof (o as { offset?: unknown }).offset === "number") {
        return { kind: "offset", offset: (o as { offset: number }).offset };
      }
    } catch {
      /* a refusal in words, below */
    }
  }
  if (status === 404) return { kind: "gone" };
  if (status === 401 || status === 403) return { kind: "signin" };
  if (status === 408 || status === 425 || status === 429 || status >= 500) return { kind: "retry" };
  return { kind: "fail", text: body.trim().slice(0, 300) };
}

/** How long to wait before the next try, in milliseconds: it doubles from two seconds up to five minutes. */
export function backoff(tries: number): number {
  return Math.min(5 * 60_000, 2000 * 2 ** Math.max(0, tries - 1));
}

/** The size of a piece: what the server asks for, but never more than 4 MiB, so progress moves and a cut costs little. */
export function pieceSize(server: number): number {
  const cap = 4 << 20;
  return server > 0 ? Math.min(server, cap) : cap;
}

/** A size in words: 800 KB, 12.4 MB, 1.3 GB. */
export function sizeText(bytes: number): string {
  if (bytes < 1 << 20) return `${Math.max(1, Math.round(bytes / 1024))} KB`;
  if (bytes < 1 << 30) return `${(bytes / (1 << 20)).toFixed(bytes < 10 << 20 ? 1 : 0)} MB`;
  return `${(bytes / (1 << 30)).toFixed(1)} GB`;
}

/** Whether a name or type says photo or video: the server decides by the content, this only keeps the page's own list sane. */
export function looksLikeMedia(name: string, type: string): boolean {
  if (type.startsWith("image/") || type.startsWith("video/")) return true;
  return /\.(jpe?g|png|gif|webp|heic|heif|avif|tiff?|dng|cr2|cr3|nef|arw|orf|raf|rw2|mp4|m4v|mov|3gp|webm|mkv)$/i.test(name);
}

/** Percent of a file that has arrived, whole numbers. */
export function percent(sent: number, size: number): number {
  return size <= 0 ? 0 : Math.max(0, Math.min(100, Math.floor((sent / size) * 100)));
}

/** Where a file is in its life. "offline" and "signin" wait for the network or for a sign-in, and are tried again. */
export type State = "waiting" | "sending" | "offline" | "signin" | "done" | "failed" | "cancelled";

export interface Item {
  id: string;
  name: string;
  size: number;
  /** When the photo was taken (the file's own date), milliseconds since 1970. */
  taken: number;
  /** The service it is for; empty for the library only. */
  service: string;
  /** The server's upload, once it has begun. */
  serverId: string;
  sent: number;
  state: State;
  /** Why it failed, in words. */
  text: string;
  tries: number;
  photo: string;
  duplicate: boolean;
  /** When it was put in the queue (milliseconds since 1970). */
  added: number;
}

/** The next file to send: the first that is waiting for its turn or for the network. -1 when there is none. */
export function nextToSend(items: readonly Pick<Item, "state">[]): number {
  return items.findIndex((i) => i.state === "waiting" || i.state === "offline");
}

/** Whether a file is finished one way or another, so it needs nothing more. */
export function finished(s: State): boolean {
  return s === "done" || s === "failed" || s === "cancelled";
}

/** How many of each kind, for the line above the list. */
export function tally(items: readonly Pick<Item, "state">[]): { done: number; failed: number; open: number; total: number } {
  let done = 0;
  let failed = 0;
  let open = 0;
  for (const i of items) {
    if (i.state === "done") done++;
    else if (i.state === "failed") failed++;
    else if (i.state !== "cancelled") open++;
  }
  return { done, failed, open, total: done + failed + open };
}

/** A fresh item for a file. */
export function newItem(id: string, name: string, size: number, taken: number, service: string, now: number): Item {
  return { id, name, size, taken, service, serverId: "", sent: 0, state: "waiting", text: "", tries: 0, photo: "", duplicate: false, added: now };
}
