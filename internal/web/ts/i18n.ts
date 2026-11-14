// The page's words in the person's language, for the scripts. The server puts the messages the scripts use, already in
// the person's language, in the page (`<script type="application/json" id="i18n">`), and English is the source: a message
// is written in English where it is used (t("Continue")), and that text is also its key. Anything not translated shows as
// the English, so nothing is ever blank. Without a page (the tests run these functions in Node) it is all English.

type Plural = Record<string, string>;
interface Catalog {
  lang: string;
  messages: Record<string, string | Plural>;
}

let catalog: Catalog = { lang: "en", messages: {} };

/** Reads the messages the server put in the page. Called once, as the page starts. */
export function loadCatalog(doc: Document = document): void {
  const el = doc.getElementById("i18n");
  if (!el?.textContent) return;
  try {
    const parsed: unknown = JSON.parse(el.textContent);
    if (typeof parsed === "object" && parsed !== null) {
      const o = parsed as { lang?: unknown; messages?: unknown };
      catalog = {
        lang: typeof o.lang === "string" ? o.lang : "en",
        messages: typeof o.messages === "object" && o.messages !== null ? (o.messages as Catalog["messages"]) : {},
      };
    }
  } catch {
    /* a page that cannot be read shows English */
  }
}

/** The language the page is in (for dates and times). */
export function language(): string {
  return catalog.lang;
}

function fill(text: string, args?: Record<string, string | number>): string {
  if (!args) return text;
  return Object.entries(args).reduce((out, [key, value]) => out.split(`{${key}}`).join(String(value)), text);
}

/** A message, with {name} placeholders filled from args. */
export function t(msg: string, args?: Record<string, string | number>): string {
  const entry = catalog.messages[msg];
  return fill(typeof entry === "string" ? entry : msg, args);
}

/** A message with a count: the English singular (its key) and plural; {n} is the count. */
export function n(one: string, other: string, count: number, args?: Record<string, string | number>): string {
  const entry = catalog.messages[one];
  let text = count === 1 ? one : other;
  if (entry !== undefined && typeof entry !== "string") {
    const category = new Intl.PluralRules(catalog.lang).select(count);
    text = entry[category] ?? entry["other"] ?? text;
  }
  return fill(text, { n: count, ...args });
}

/** The forms of a counted message as templates ({n} left in), by plural category, for something that has to make the text
 *  later, in another place (the worker's notification). */
export function forms(one: string, other: string): Record<string, string> {
  const entry = catalog.messages[one];
  return entry !== undefined && typeof entry !== "string" ? { ...entry } : { one, other };
}
