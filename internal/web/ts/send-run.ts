// Sends one file to the app in pieces. It works in the page and in the service worker alike (both only use fetch), and it
// can be stopped and started again at any point: what the server has is asked for first, and the file carries on from
// there. See internal/web/phone.go for the other side.

import { pieceSize, verdict } from "./send-core.ts";

export interface Job {
  name: string;
  size: number;
  /** When the photo was taken, in milliseconds since 1970 (0: unknown). */
  taken: number;
  /** The service it is for, empty for the library only. */
  service: string;
  /** The server's upload, once it has been started. */
  serverId: string;
}

export type Outcome =
  | { kind: "done"; serverId: string; photo: string; duplicate: boolean }
  | { kind: "offline"; serverId: string; sent: number } // the network went away: ask again later
  | { kind: "signin"; serverId: string; sent: number }
  | { kind: "failed"; serverId: string; text: string };

export interface Hooks {
  progress(sent: number, serverId: string): void;
  /** Stops at the next piece when it returns true (the person cancelled or left). */
  stopped(): boolean;
}

const HEADERS = { "X-Requested-With": "immich-handoff" };

async function text(r: Response): Promise<string> {
  try {
    return await r.text();
  } catch {
    return "";
  }
}

async function ask(url: string, init: RequestInit): Promise<{ status: number; body: string } | null> {
  try {
    const r = await fetch(url, { credentials: "same-origin", ...init, headers: { ...HEADERS, ...(init.headers ?? {}) } });
    return { status: r.status, body: await text(r) };
  } catch {
    return null; // no network
  }
}

/** The file's SHA-1 in hex, for a file small enough to read at once (the server compares it; larger ones are checked by size). */
export async function sha1(file: Blob): Promise<string> {
  if (file.size > 50 << 20 || !globalThis.crypto?.subtle) return "";
  const digest = await crypto.subtle.digest("SHA-1", await file.arrayBuffer());
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, "0")).join("");
}

/** Sends `file`. `base` is the address of the app's front page. Returns what happened; it never throws for the network. */
export async function sendFile(base: string, file: Blob, job: Job, hooks: Hooks, pass = 0): Promise<Outcome> {
  let id = job.serverId;
  let restarted = false;
  let offset = 0;
  let piece = pieceSize(0);

  const start = async (): Promise<Outcome | null> => {
    const r = await ask(new URL("send/start", base).href, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: job.name, size: job.size, service: job.service, taken: job.taken }),
    });
    if (!r) return { kind: "offline", serverId: "", sent: 0 };
    const v = verdict(r.status, r.body);
    if (v.kind === "ok") {
      const o = JSON.parse(r.body) as { id: string; chunk: number };
      id = o.id;
      piece = pieceSize(o.chunk);
      offset = 0;
      hooks.progress(0, id);
      return null;
    }
    if (v.kind === "signin") return { kind: "signin", serverId: "", sent: 0 };
    if (v.kind === "fail") return { kind: "failed", serverId: "", text: v.text };
    return { kind: "offline", serverId: "", sent: 0 };
  };

  if (id === "") {
    const out = await start();
    if (out) return out;
  } else {
    const r = await ask(new URL(`send/${id}`, base).href, { method: "GET" });
    if (!r) return { kind: "offline", serverId: id, sent: 0 };
    const v = verdict(r.status, r.body);
    if (v.kind === "ok") offset = (JSON.parse(r.body) as { offset: number }).offset;
    else if (v.kind === "gone") {
      id = "";
      const out = await start();
      if (out) return out;
    } else if (v.kind === "signin") return { kind: "signin", serverId: id, sent: 0 };
    else if (v.kind === "fail") return { kind: "failed", serverId: id, text: v.text };
    else return { kind: "offline", serverId: id, sent: 0 };
  }

  let slips = 0;
  while (offset < job.size) {
    if (hooks.stopped()) return { kind: "offline", serverId: id, sent: offset };
    const end = Math.min(offset + piece, job.size);
    const r = await ask(new URL(`send/${id}/chunk?offset=${offset}`, base).href, { method: "PUT", body: file.slice(offset, end) });
    if (!r) return { kind: "offline", serverId: id, sent: offset };
    const v = verdict(r.status, r.body);
    if (v.kind === "ok") {
      offset = (JSON.parse(r.body) as { offset: number }).offset;
      slips = 0;
      hooks.progress(offset, id);
    } else if (v.kind === "offset") {
      if (++slips > 5) return { kind: "failed", serverId: id, text: "the server and the phone do not agree where the file stands" };
      offset = v.offset;
    } else if (v.kind === "gone" && !restarted) {
      restarted = true;
      id = "";
      const out = await start();
      if (out) return out;
    } else if (v.kind === "signin") return { kind: "signin", serverId: id, sent: offset };
    else if (v.kind === "fail" || v.kind === "gone") return { kind: "failed", serverId: id, text: v.kind === "fail" ? v.text : "the upload was lost" };
    else return { kind: "offline", serverId: id, sent: offset };
  }

  const sum = await sha1(file);
  const r = await ask(new URL(`send/${id}/done`, base).href, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ sum }) });
  if (!r) return { kind: "offline", serverId: id, sent: offset };
  const v = verdict(r.status, r.body);
  if (v.kind === "ok") {
    const o = JSON.parse(r.body) as { photo: string; duplicate: boolean };
    return { kind: "done", serverId: id, photo: o.photo, duplicate: o.duplicate };
  }
  if (v.kind === "offset" || v.kind === "gone") {
    // the server holds less than the phone sent (or forgot the file): send it again from the start
    if (pass < 2) return sendFile(base, file, { ...job, serverId: v.kind === "gone" ? "" : id }, hooks, pass + 1);
    return { kind: "failed", serverId: id, text: "the file did not arrive" };
  }
  if (v.kind === "signin") return { kind: "signin", serverId: id, sent: offset };
  if (v.kind === "fail") return { kind: "failed", serverId: id, text: v.text };
  return { kind: "offline", serverId: id, sent: offset };
}

