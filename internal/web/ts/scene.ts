// The live scene at the bottom of the Overview: for each service a stage with one lane per sender; a token flies along
// the lane into the receiver's gate for every photo handed over. It is driven only by the live events the server sends
// (see live.ts), never by anything made up, and it shows no photo and no file name: a token is just a coloured square.
//
// The logic that decides what a lane shows and what the log says is in plain functions, tested with Node; the part that
// touches the page is at the bottom. Without motion (the browser asks for reduced motion, or the tab is hidden) the same
// information is shown as text: lane labels, the arrived counter and a short log, all in a live region.

import type { LiveEvent } from "./live.ts";
import { language, n, t } from "./i18n.ts";
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
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleTimeString(language(), { hour: "2-digit", minute: "2-digit" });
}

/** The words under a sender's name; colour is never the only signal. */
export function stateText(f: LaneFlags): string {
  switch (laneState(f)) {
    case "broken":
      return t("Connection lost");
    case "paused":
      return f.pausedService ? t("Paused by the owner") : t("Paused by the sender");
    case "capped": {
      const when = clock(f.until);
      const waits = f.waiting > 0 ? n("{n} photo waits", "{n} photos wait", f.waiting) : t("Photos wait");
      return when ? t("Daily limit reached: {waits} until {when}", { waits, when }) : t("Daily limit reached: {waits}", { waits });
    }
  }
  return f.sending > 0 ? t("Sending {n}", { n: f.sending }) : t("Flowing");
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

function photos(count: number): string {
  return n("{n} photo", "{n} photos", count);
}

/** One sentence for the log and for screen readers. */
export function describe(ev: LiveEvent): string {
  const count = ev.count && ev.count > 0 ? ev.count : 1;
  const who = ev.sender ?? t("A sender");
  const service = ev.service ?? t("a service");
  const p = photos(count);
  switch (ev.kind) {
    case "started":
      return t("{who} is handing over {photos} to {service}", { who, photos: p, service });
    case "handed":
      return ev.reason === "duplicate"
        ? t("{who}: {photos} already there in {service}", { who, photos: p, service })
        : t("{who} handed over {photos} to {service}", { who, photos: p, service });
    case "refused":
      return t("{who}: {photos} refused, the receiver has them in the trash; the originals were kept", { who, photos: p });
    case "failed":
      return t("{who}: {photos} could not be handed over; the originals were kept and it will be retried", { who, photos: p });
    case "held": {
      const when = clock(ev.until);
      return when
        ? t("{who}: {photos} wait for the daily limit, until {when}", { who, photos: p, when })
        : t("{who}: {photos} wait for the daily limit", { who, photos: p });
    }
    case "limit":
      return t("{who} reached the daily limit for {service}", { who, service });
    case "paused":
      return ev.reason === "service" ? t("{service} was paused by its owner", { service }) : t("{who} paused sending to {service}", { who, service });
    case "resumed":
      return ev.reason === "service" ? t("{service} was resumed", { service }) : t("{who} resumed sending to {service}", { who, service });
    case "lost":
      return ev.reason === "service"
        ? t("{service} lost its connection", { service })
        : t("{who}'s connection to {service} was lost; no photo was touched", { who, service });
    case "restored":
      return t("{service} was reconnected", { service });
    case "resync":
      return t("Some updates were missed; refreshing");
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
  out: boolean; // the person sends here: tokens fly from the right edge to the left, away from "You"
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
    stages.push({ el, service, lanes, counter, landed, out: el.dataset["dir"] === "out" });
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
  li.textContent = `${new Date().toLocaleTimeString(language(), { hour: "2-digit", minute: "2-digit" })} ${text}`;
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
  // a ring spreads from the gate where photos land
  const gate = stage.el.querySelector<HTMLElement>(".gate");
  if (gate && !reduced() && !document.hidden) {
    const accent = getComputedStyle(document.documentElement).getPropertyValue("--accent").trim() || "#98a6ff";
    gate.animate([{ boxShadow: `0 0 0 0 ${accent}` }, { boxShadow: "0 0 0 22px transparent" }], { duration: 900, easing: "ease-out" });
  }
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
    token.className = `token t${hue}${stage.out ? " out" : ""}`;
    token.setAttribute("aria-hidden", "true");
    lane.track.appendChild(token);
    lane.tokens++;
    tokensInFlight++;
    const width = Math.max(0, lane.track.clientWidth - 40);
    const stop = outcome === "failed" ? width * 0.55 : outcome === "refused" ? width * 0.8 : width + 14;
    const dir = stage.out ? -1 : 1; // a token that leaves "You" at the right edge moves toward the left
    const frames: Keyframe[] = [
      { transform: "translateX(0) scale(.6)", opacity: 0 },
      { transform: `translateX(${dir * width * 0.08}px) scale(1)`, opacity: 1, offset: 0.12 },
      { transform: `translateX(${dir * stop}px) scale(1)`, opacity: 1, offset: 0.9 },
      { transform: `translateX(${dir * (outcome === "handed" ? stop + 24 : outcome === "refused" ? stop * 0.5 : stop)}px) scale(${outcome === "failed" ? 0.2 : 0.5})`, opacity: 0 },
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
