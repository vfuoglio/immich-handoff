// The date above the greeting on the Overview ("Friday, 13 November"), written in the person's own language by the
// browser, so the server needs no translation of weekdays and months. Without script the line is simply empty.

import { language } from "./i18n.ts";

export function initToday(): void {
  const text = new Date().toLocaleDateString(language(), { weekday: "long", day: "numeric", month: "long" });
  document.querySelectorAll<HTMLElement>("[data-today]").forEach((el) => (el.textContent = text));
}
