// Telling the person that what they sent has arrived (or has not): one message when the queue has emptied, however many
// photos it held, and only what ended since the last one. The decision and the words are here, free of the browser, so they can
// be tested; the worker and the page both use them (they show the message through the worker's registration).

import type { Item } from "./send-core.ts";

/** What the person asked to be told: everything, only trouble, or nothing. */
export type Tell = "all" | "problems" | "none";

export function isTell(v: unknown): v is Tell {
  return v === "all" || v === "problems" || v === "none";
}

/** The messages, as templates for each plural category with {n} for the count, in the person's language (the page stores them,
 *  because the worker cannot read the page's catalog). */
export interface Texts {
  lang: string;
  title: string;
  arrived: Record<string, string>;
  failed: Record<string, string>;
  signin: Record<string, string>;
}

export interface Counts {
  arrived: number;
  failed: number;
  signin: number;
}

/** What to say after a run, or null for nothing. Nothing while any file is still on its way (the message comes once, at the
 *  end), nothing for what was already told (`told` is when the last message was made), and nothing for a person who asked for
 *  none. A file that waits for a sign-in is trouble. */
export function plan(items: readonly Item[], told: number, tell: Tell): Counts | null {
  if (tell === "none") return null;
  if (items.some((i) => i.state === "waiting" || i.state === "offline" || i.state === "sending")) return null;
  const c: Counts = { arrived: 0, failed: 0, signin: 0 };
  for (const i of items) {
    if (i.state === "done" && i.ended > told) c.arrived++;
    else if (i.state === "failed" && i.ended > told) c.failed++;
    else if (i.state === "signin") c.signin++;
  }
  if (tell === "problems") c.arrived = 0;
  return c.arrived + c.failed + c.signin === 0 ? null : c;
}

/** The text for a count in the right plural form of the language. */
export function form(forms: Record<string, string>, lang: string, count: number): string {
  let category = "other";
  try {
    category = new Intl.PluralRules(lang).select(count);
  } catch {
    /* an unknown language: the plain forms */
  }
  const text = forms[category] ?? forms["other"] ?? forms["one"] ?? "{n}";
  return text.split("{n}").join(String(count));
}

/** The lines of the message, one for each thing that happened. */
export function lines(texts: Texts, c: Counts): string[] {
  const out: string[] = [];
  if (c.arrived > 0) out.push(form(texts.arrived, texts.lang, c.arrived));
  if (c.failed > 0) out.push(form(texts.failed, texts.lang, c.failed));
  if (c.signin > 0) out.push(form(texts.signin, texts.lang, c.signin));
  return out;
}
