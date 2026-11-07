// The live scene at the bottom of the Overview: for each service a stage with one lane per sender; a token flies along
// the lane into the receiver's gate for every photo handed over. It is driven only by the live events the server sends
// (see live.ts), never by anything made up, and it shows no photo and no file name: a token is just a coloured square.
//
// The logic that decides what a lane shows and what the log says is in plain functions, tested with Node; the part that
// touches the page is at the bottom. Without motion (the browser asks for reduced motion, or the tab is hidden) the same
// information is shown as text: lane labels, the arrived counter and a short log, all in a live region.

import type { LiveEvent } from "./live.ts";
import { setOdometer } from "./odometer.ts";

export type LaneState = "flow" | "paused" | "capped" | "broken";

/** Everything that decides how one sender's lane looks. */
export interface LaneFlags {
  pausedSelf: boolean; // the sender paused their own sending
  pausedService: boolean; // the owner paused the service
  capped: boolean; // the daily limit holds photos back
  broken: boolean; // the connection was cut
  waiting: number; // photos waiting for the limit
  until?: string; // when they can go (RFC 3339)
  sending: number; // photos being handed over right now
}

export function newFlags(): LaneFlags {
  return { pausedSelf: false, pausedService: false, capped: false, broken: false, waiting: 0, sending: 0 };
}

/** What is wrong most urgently wins: a cut connection, then a pause, then the limit. */
export function laneState(f: LaneFlags): LaneState {
  if (f.broken) return "broken";
  if (f.pausedSelf || f.pausedService) return "paused";
  if (f.capped) return "capped";
  return "flow";
}

function clock(iso: string | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

/** The words under a sender's name; colour is never the only signal. */
export function stateText(f: LaneFlags): string {
  switch (laneState(f)) {
    case "broken":
      return "Connection lost";
    case "paused":
      return f.pausedService ? "Paused by the owner" : "Paused by the sender";
    case "capped": {
      const when = clock(f.until);
      const n = f.waiting > 0 ? `${f.waiting} ${f.waiting === 1 ? "photo waits" : "photos wait"}` : "Photos wait";
      return when ? `Daily limit reached: ${n} until ${when}` : `Daily limit reached: ${n}`;
    }
  }
  return f.sending > 0 ? `Sending ${f.sending}` : "Flowing";
}

/** Whether an event is about the whole service (every lane) rather than one sender. */
export function wholeService(ev: LiveEvent): boolean {
  return ev.reason === "service" || ev.sender === undefined;
}

/** The lane's flags after an event that concerns it. Pure: the old flags are not changed. */
export function applyEvent(old: LaneFlags, ev: LiveEvent): LaneFlags {
  const f: LaneFlags = { ...old };
  const n = ev.count && ev.count > 0 ? ev.count : 1;
  switch (ev.kind) {
    case "started":
      f.sending += n;
      break;
    case "handed":
      f.sending = Math.max(0, f.sending - n);
      f.capped = false; // photos went, so the limit let go
      f.waiting = 0;
      delete f.until;
      break;
    case "refused":
    case "failed":
      f.sending = Math.max(0, f.sending - n);
      break;
    case "held":
      f.capped = true;
      f.waiting = ev.count ?? f.waiting;
      if (ev.until !== undefined) f.until = ev.until;
      break;
    case "limit":
      f.capped = true;
      if (ev.count !== undefined) f.waiting = ev.count;
      if (ev.until !== undefined) f.until = ev.until;
      break;
    case "paused":
      if (ev.reason === "service") f.pausedService = true;
      else f.pausedSelf = true;
      break;
    case "resumed":
      if (ev.reason === "service") f.pausedService = false;
      else f.pausedSelf = false;
      break;
    case "lost":
      f.broken = true;
      f.sending = 0;
      break;
    case "restored":
      f.broken = false;
      break;
    case "resync":
      break;
  }
  return f;
}

function photos(n: number): string {
  return `${n} ${n === 1 ? "photo" : "photos"}`;
}

/** One sentence for the log and for screen readers. */
export function describe(ev: LiveEvent): string {
  const n = ev.count && ev.count > 0 ? ev.count : 1;
  const who = ev.sender ?? "A sender";
  const to = ev.service ? ` to ${ev.service}` : "";
  const inService = ev.service ? ` in ${ev.service}` : "";
  const service = ev.service ?? "The service";
  switch (ev.kind) {
    case "started":
      return `${who} is handing over ${photos(n)}${to}`;
    case "handed":
      return ev.reason === "duplicate" ? `${who}: ${photos(n)} already there${inService}` : `${who} handed over ${photos(n)}${to}`;
    case "refused":
      return `${who}: ${photos(n)} refused, the receiver has them in the trash; the originals were kept`;
    case "failed":
      return `${who}: ${photos(n)} could not be handed over; the originals were kept and it will be retried`;
    case "held": {
      const when = clock(ev.until);
      return `${who}: ${photos(n)} wait for the daily limit${when ? ", until " + when : ""}`;
    }
    case "limit":
      return `${who} reached the daily limit${ev.service ? " for " + ev.service : ""}`;
    case "paused":
      return ev.reason === "service" ? `${service} was paused by its owner` : `${who} paused sending${to}`;
    case "resumed":
      return ev.reason === "service" ? `${service} was resumed` : `${who} resumed sending${to}`;
    case "lost":
      return ev.reason === "service" ? `${service} lost its connection` : `${who}'s connection${to} was lost; no photo was touched`;
    case "restored":
      return `${service} was reconnected`;
    case "resync":
      return "Some updates were missed; refreshing";
  }
}

/** How many tokens to animate for a burst, and how many photos are left to count without one. */
export function flights(count: number, max: number): { shown: number; extra: number } {
  const n = Math.max(1, count);
  const shown = Math.min(n, max);
  return { shown, extra: n - shown };
}

// ---------------------------------------------------------------------------------------------------------------
// The page

const MAX_TOKENS_PER_LANE = 4;
const MAX_TOKENS_TOTAL = 10;
const HUES = 6;

interface Lane {
  el: HTMLElement;
  sender: string;
  flags: LaneFlags;
  label: HTMLElement;
  track: HTMLElement;
  tokens: number;
}

interface Stage {
  el: HTMLElement;
  service: string;
  lanes: Lane[];
  counter: HTMLElement;
  landed: HTMLElement;
}

/** How many photos arrived at each service's gate while the page was open; kept across refreshes of the scene. */
const arrived = new Map<string, number>();
let tokensInFlight = 0;
let hueTurn = 0;

function reduced(): boolean {
  return matchMedia("(prefers-reduced-motion: reduce)").matches;
}

function readStages(root: HTMLElement): Stage[] {
  const stages: Stage[] = [];
  root.querySelectorAll<HTMLElement>("[data-stage]").forEach((el) => {
    const service = el.dataset["service"] ?? "";
    const counter = el.querySelector<HTMLElement>("[data-arrived]");
    const landed = el.querySelector<HTMLElement>("[data-landed]");
    if (!counter || !landed) return;
    const lanes: Lane[] = [];
    el.querySelectorAll<HTMLElement>("[data-lane]").forEach((laneEl) => {
      const label = laneEl.querySelector<HTMLElement>("[data-lane-state]");
      const track = laneEl.querySelector<HTMLElement>("[data-track]");
      if (!label || !track) return;
      const flags = newFlags();
      // the server renders the state it knows; start from it
      const state = laneEl.dataset["state"];
      if (state === "broken") flags.broken = true;
      if (state === "paused") flags[laneEl.dataset["paused"] === "service" ? "pausedService" : "pausedSelf"] = true;
      if (state === "capped") flags.capped = true;
      lanes.push({ el: laneEl, sender: laneEl.dataset["sender"] ?? "", flags, label, track, tokens: 0 });
    });
    stages.push({ el, service, lanes, counter, landed });
  });
  return stages;
}

function paint(lane: Lane): void {
  lane.el.dataset["state"] = laneState(lane.flags);
  lane.label.textContent = stateText(lane.flags);
}

function say(root: HTMLElement, text: string): void {
  const log = root.querySelector<HTMLElement>("[data-log]");
  if (!log) return;
  log.querySelector("[data-empty]")?.remove();
  const li = document.createElement("li");
  li.textContent = `${new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })} ${text}`;
  log.prepend(li);
  while (log.children.length > 5) log.lastElementChild?.remove();
  const live = root.querySelector<HTMLElement>("[data-announce]");
  if (live) live.textContent = text; // screen readers hear the same sentence
}

function land(stage: Stage, n: number, hue: number): void {
  const total = (arrived.get(stage.service) ?? 0) + n;
  arrived.set(stage.service, total);
  setOdometer(stage.counter, total, `arrived:${stage.service}`);
  const chip = document.createElement("i");
  chip.className = `t${hue}`;
  stage.landed.prepend(chip);
  while (stage.landed.children.length > 5) stage.landed.lastElementChild?.remove();
}

function fly(stage: Stage, lane: Lane, count: number, outcome: "handed" | "refused" | "failed"): void {
  const { shown, extra } = flights(count, MAX_TOKENS_PER_LANE);
  const animate = !reduced() && !document.hidden;
  for (let i = 0; i < shown; i++) {
    const hue = hueTurn++ % HUES;
    const credit = outcome === "handed" ? 1 + (i === shown - 1 ? extra : 0) : 0;
    if (!animate || lane.tokens >= MAX_TOKENS_PER_LANE || tokensInFlight >= MAX_TOKENS_TOTAL) {
      if (credit) land(stage, credit, hue);
      continue;
    }
    const token = document.createElement("span");
    token.className = `token t${hue}`;
    token.setAttribute("aria-hidden", "true");
    lane.track.appendChild(token);
    lane.tokens++;
    tokensInFlight++;
    const width = Math.max(0, lane.track.clientWidth - 40);
    const stop = outcome === "failed" ? width * 0.55 : outcome === "refused" ? width * 0.8 : width + 14;
    const frames: Keyframe[] = [
      { transform: "translateX(0) scale(.6)", opacity: 0 },
      { transform: `translateX(${width * 0.08}px) scale(1)`, opacity: 1, offset: 0.12 },
      { transform: `translateX(${stop}px) scale(1)`, opacity: 1, offset: 0.9 },
      { transform: `translateX(${outcome === "handed" ? stop + 24 : outcome === "refused" ? stop * 0.5 : stop}px) scale(${outcome === "failed" ? 0.2 : 0.5})`, opacity: 0 },
    ];
    const run = token.animate(frames, { duration: 1500 + i * 180, delay: i * 140, easing: "cubic-bezier(.45,.05,.3,1)", fill: "backwards" });
    const done = (): void => {
      token.remove();
      lane.tokens--;
      tokensInFlight--;
      if (credit) land(stage, credit, hue);
    };
    run.onfinish = done;
    run.oncancel = done;
  }
}

function target(stage: Stage, ev: LiveEvent): Lane[] {
  if (wholeService(ev)) return stage.lanes;
  return stage.lanes.filter((l) => l.sender === ev.sender);
}

/** Sets the scene up inside its section, and wires it to the live events. Safe to call again after the section was replaced. */
export function initScene(root: HTMLElement): void {
  let stages = readStages(root);
  const reread = (): void => {
    stages = readStages(root);
    stages.forEach((s) => {
      const n = arrived.get(s.service) ?? 0;
      setOdometer(s.counter, n, `arrived:${s.service}`);
    });
  };
  reread();

  const onLive = (e: CustomEvent<LiveEvent>): void => {
    const ev = e.detail;
    if (ev.kind === "resync") {
      say(root, describe(ev));
      document.body.dispatchEvent(new CustomEvent("scene:refresh"));
      return;
    }
    const stage = stages.find((s) => s.service === ev.service);
    if (!stage) return;
    say(root, describe(ev));
    const lanes = target(stage, ev);
    for (const lane of lanes) {
      lane.flags = applyEvent(lane.flags, ev);
      paint(lane);
      if (ev.kind === "handed" || ev.kind === "refused" || ev.kind === "failed") fly(stage, lane, ev.count ?? 1, ev.kind);
    }
    if (lanes.length === 0 && ev.kind === "handed") land(stage, ev.count ?? 1, hueTurn++ % HUES); // a sender who joined after the page opened
  };
  document.addEventListener("handoff:live", onLive);

  const onStream = (e: CustomEvent<{ up: boolean }>): void => {
    root.classList.toggle("is-offline", !e.detail.up);
    const note = root.querySelector<HTMLElement>("[data-offline]");
    if (note) note.hidden = e.detail.up;
    if (e.detail.up) document.body.dispatchEvent(new CustomEvent("scene:refresh")); // catch up on what was missed
  };
  document.addEventListener("handoff:stream", onStream);

  // the section replaces itself when it is refreshed: when a new one appears, let this one go and start from the new
  const onSwap = (): void => {
    const fresh = document.getElementById("scene");
    if (!fresh || fresh === root) return;
    document.removeEventListener("handoff:live", onLive);
    document.removeEventListener("handoff:stream", onStream);
    document.body.removeEventListener("htmx:afterSwap", onSwap);
    initScene(fresh);
  };
  document.body.addEventListener("htmx:afterSwap", onSwap);
}
