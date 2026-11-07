// The administrator's "Download backup" button. The file is the app's own data (every stored key, still encrypted, and
// the names and emails), so it is never a plain link: htmx asks in the page's dialog first, and then this script
// sends the request itself, with the header that a page from another site cannot add, and saves the answer as a file.

import { t } from "./i18n.ts";

/** The file name the server suggested, or a plain one. */
export function fileName(disposition: string | null): string {
  const found = /filename="([^"\\/]+)"/.exec(disposition ?? "");
  return found?.[1] ?? "handoff-backup.db";
}

function say(text: string): void {
  const flash = document.getElementById("flash");
  if (flash) flash.textContent = text;
}

async function download(button: HTMLButtonElement): Promise<void> {
  const url = button.dataset["backupDownload"];
  if (!url) return;
  button.disabled = true;
  try {
    const response = await fetch(url, { method: "POST", headers: { "X-Requested-With": "immich-handoff" }, credentials: "same-origin" });
    if (!response.ok) {
      say((await response.text()).trim() || t("The backup could not be downloaded."));
      return;
    }
    const blob = await response.blob();
    const link = document.createElement("a");
    link.href = URL.createObjectURL(blob);
    link.download = fileName(response.headers.get("Content-Disposition"));
    document.body.append(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(link.href), 10_000);
    say(t("The backup was downloaded."));
  } catch {
    say(t("The backup could not be downloaded."));
  } finally {
    button.disabled = false;
  }
}

export function initBackup(): void {
  // htmx has asked (the dialog was answered with OK) and is about to send: take over, because the answer is a file
  document.addEventListener("htmx:beforeRequest", (e) => {
    const elt = e.detail.elt;
    if (elt instanceof HTMLButtonElement && elt.dataset["backupDownload"]) {
      e.preventDefault();
      void download(elt);
    }
  });
}
