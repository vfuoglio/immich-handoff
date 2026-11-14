// A question in the page's own dialog (the one htmx confirmations use), for a script that has to ask before it goes on.

/** Asks `text` with a title and the words of the two buttons. Resolves true for the second (going ahead), false for the first
 *  or for closing the dialog. Without the dialog (a page that has none) it goes ahead. */
export function ask(title: string, text: string, no: string, yes: string): Promise<boolean> {
  const dialog = document.getElementById("confirm");
  const heading = document.getElementById("confirm-title");
  const body = document.getElementById("confirm-text");
  const ok = document.getElementById("confirm-ok");
  const cancel = dialog?.querySelector<HTMLButtonElement>('button[value="cancel"]');
  if (!(dialog instanceof HTMLDialogElement) || !heading || !body || !ok || !cancel) return Promise.resolve(true);
  const was = { title: heading.textContent, ok: ok.textContent, cancel: cancel.textContent };
  heading.textContent = title;
  body.textContent = text;
  ok.textContent = yes;
  cancel.textContent = no;
  ok.classList.add("primary");
  ok.classList.remove("danger");
  dialog.returnValue = "cancel";
  return new Promise((resolve) => {
    dialog.addEventListener(
      "close",
      () => {
        heading.textContent = was.title;
        ok.textContent = was.ok;
        cancel.textContent = was.cancel;
        resolve(dialog.returnValue === "ok");
      },
      { once: true },
    );
    dialog.showModal();
  });
}
