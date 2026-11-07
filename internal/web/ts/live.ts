// What the server tells an open page as it happens (server-sent events, `event: live`). The shapes mirror
// internal/live/live.go; docs/architecture.md describes each. An event names no photo and no file.

export type LiveKind =
  | "started" // a photo is being handed over
  | "handed" // a photo was handed over (reason "duplicate": the receiver already had it)
  | "refused" // the receiver has it in their trash; the original was kept
  | "failed" // it could not be handed over; the original was kept and it is retried
  | "held" // photos wait, still tagged: reason "limit", count how many, until when they can go
  | "lost" // a connection was cut (a key was deleted in Immich)
  | "restored" // an owner reconnected a service
  | "paused" // reason "service" (the owner paused it) or "sender" (a sender paused themselves)
  | "resumed"
  | "limit" // a sender reached the daily limit
  | "resync"; // some events were dropped: ask for the current state again

export interface LiveEvent {
  kind: LiveKind;
  service?: string;
  sender?: string;
  reason?: string;
  /** How many photos this event stands for: a burst is sent as one event with a count. */
  count?: number;
  /** When held photos can go (RFC 3339). */
  until?: string;
  at: string;
}

const kinds: readonly string[] = [
  "started", "handed", "refused", "failed", "held", "lost", "restored", "paused", "resumed", "limit", "resync",
];

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

/** Reads one event from the stream, or null when it is not a well-formed event (it is then ignored). */
export function parseLive(data: string): LiveEvent | null {
  let raw: unknown;
  try {
    raw = JSON.parse(data);
  } catch {
    return null;
  }
  if (typeof raw !== "object" || raw === null) return null;
  const o = raw as Record<string, unknown>;
  const kind = o["kind"];
  const at = o["at"];
  if (typeof kind !== "string" || !kinds.includes(kind) || typeof at !== "string") return null;
  const event: LiveEvent = { kind: kind as LiveKind, at };
  const service = optionalString(o["service"]);
  const sender = optionalString(o["sender"]);
  const reason = optionalString(o["reason"]);
  const until = optionalString(o["until"]);
  if (service !== undefined) event.service = service;
  if (sender !== undefined) event.sender = sender;
  if (reason !== undefined) event.reason = reason;
  if (until !== undefined) event.until = until;
  const count = o["count"];
  if (typeof count === "number" && Number.isFinite(count)) event.count = count;
  return event;
}
