// The guest page of a drop box: choose or drop photos (or take one), shrink big ones when the owner asked for it, send
// them one at a time with a progress bar, and carry on after a lost connection. Without this script the form still
// works as a plain form; everything here is an improvement on it.

import { t } from "./i18n.ts";

/** What identifies a file across page loads: its name, size and last change. Used to skip what was already sent. */
export function fileKey(f: { name: string; size: number; lastModified: number }): string {
  return `${f.name}|${f.size}|${f.lastModified}`;
}

/** How long to wait before the next try after this many failures: 2 s, 4 s, 8 s ... never more than 30 s. */
export function backoffMs(failures: number): number {
  return Math.min(30_000, 1000 * 2 ** Math.max(1, failures));
}

/** The size a picture of width x height gets when its longer side may be at most max pixels (unchanged if it fits). */
export function fitWithin(width: number, height: number, max: number): { width: number; height: number; shrunk: boolean } {
  const longest = Math.max(width, height);
  if (max <= 0 || longest <= max) return { width, height, shrunk: false };
  const scale = max / longest;
  return { width: Math.max(1, Math.round(width * scale)), height: Math.max(1, Math.round(height * scale)), shrunk: true };
}

const SHRINKABLE = ["image/jpeg", "image/png", "image/webp"];
const TRIES = 8;

interface Item {
  file: File;
  li: HTMLLIElement;
  status: HTMLElement;
  bar: HTMLProgressElement;
  done: boolean;
}

function readDone(key: string): Set<string> {
  try {
    const raw = localStorage.getItem("handoff-drop-" + key);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    return new Set(Array.isArray(parsed) ? parsed.filter((x): x is string => typeof x === "string") : []);
  } catch {
    return new Set();
  }
}

function writeDone(key: string, done: Set<string>): void {
  try {
    localStorage.setItem("handoff-drop-" + key, JSON.stringify([...done].slice(-500)));
  } catch {
    /* a browser that will not remember only sends a file twice, which the library notices */
  }
}

/** Shrinks a picture whose longer side is over max pixels. Anything that cannot be decoded here is sent as it is. */
async function shrink(file: File, max: number): Promise<File> {
  if (max <= 0 || !SHRINKABLE.includes(file.type) || typeof createImageBitmap !== "function") return file;
  try {
    const bitmap = await createImageBitmap(file);
    const size = fitWithin(bitmap.width, bitmap.height, max);
    if (!size.shrunk) {
      bitmap.close();
      return file;
    }
    const canvas = document.createElement("canvas");
    canvas.width = size.width;
    canvas.height = size.height;
    const ctx = canvas.getContext("2d");
    if (!ctx) return file;
    ctx.drawImage(bitmap, 0, 0, size.width, size.height);
    bitmap.close();
    const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, "image/jpeg", 0.9));
    if (!blob || blob.size >= file.size) return file; // never make a file bigger
    return new File([blob], file.name.replace(/\.[^.]+$/, "") + ".jpg", { type: "image/jpeg", lastModified: file.lastModified });
  } catch {
    return file;
  }
}

interface Answer {
  files?: { name?: string; text?: string; ok?: boolean }[];
}

/** One try at sending one file. Resolves with the server's answer, or rejects with whether it is worth trying again. */
function post(url: string, password: string | null, file: File, onProgress: (fraction: number) => void): Promise<Answer> {
  return new Promise((resolve, reject) => {
    const body = new FormData();
    if (password !== null) body.append("password", password); // before the files: the server reads in order
    body.append("files", file, file.name);
    const xhr = new XMLHttpRequest();
    xhr.open("POST", url);
    xhr.setRequestHeader("Accept", "application/json");
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) onProgress(e.loaded / e.total);
    };
    xhr.onerror = () => reject({ retry: true });
    xhr.ontimeout = () => reject({ retry: true });
    xhr.onload = () => {
      if (xhr.status === 200) {
        try {
          resolve(JSON.parse(xhr.responseText) as Answer);
        } catch {
          reject({ retry: true });
        }
      } else {
        reject({ retry: xhr.status === 429 || xhr.status >= 500, status: xhr.status });
      }
    };
    xhr.send(body);
  });
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function whenOnline(): Promise<void> {
  if (navigator.onLine) return;
  await new Promise<void>((resolve) => window.addEventListener("online", () => resolve(), { once: true }));
}

export function initDrop(): void {
  const form = document.querySelector<HTMLFormElement>("form.drop-form");
  const list = form?.querySelector<HTMLUListElement>("ul.drop-list");
  if (!form || !list) return;
  const input = form.querySelector<HTMLInputElement>('input[type="file"][name="files"]');
  const camera = form.querySelector<HTMLInputElement>('input[type="file"][name="camera"]');
  const password = form.querySelector<HTMLInputElement>('input[name="password"]');
  const submit = form.querySelector<HTMLButtonElement>('button[type="submit"]');
  if (!input) return;
  const key = form.dataset.key ?? "";
  const maxDim = Number(form.dataset.maxdim ?? "0") || 0;
  const done = readDone(key);
  const items: Item[] = [];
  let busy = false;

  const add = (files: FileList | File[] | null): void => {
    for (const file of Array.from(files ?? [])) {
      if (items.some((i) => fileKey(i.file) === fileKey(file))) continue;
      const li = document.createElement("li");
      const name = document.createElement("span");
      name.textContent = file.name;
      const status = document.createElement("span");
      status.className = "meta";
      const bar = document.createElement("progress");
      bar.max = 1;
      bar.value = 0;
      li.append(name, " ", status, " ", bar);
      list.append(li);
      const item: Item = { file, li, status, bar, done: done.has(fileKey(file)) };
      status.textContent = item.done ? t("Sent already") : t("Waiting to send");
      if (item.done) bar.value = 1;
      items.push(item);
    }
    list.hidden = items.length === 0;
    input.required = false; // the files are kept in the list, not in the input
  };

  const send = async (item: Item): Promise<void> => {
    const url = form.action;
    let failures = 0;
    for (;;) {
      await whenOnline();
      item.status.textContent = failures === 0 ? t("Sending") : t("Waiting for the connection");
      try {
        const file = failures === 0 ? await shrink(item.file, maxDim) : item.file;
        const answer = await post(url, password ? password.value : null, file, (f) => {
          item.bar.value = f;
        });
        const line = answer.files?.[0];
        item.bar.value = 1;
        item.status.textContent = line?.text ?? t("Received");
        item.li.classList.toggle("failed", line?.ok === false);
        if (line?.ok !== false) {
          item.done = true;
          done.add(fileKey(item.file));
          writeDone(key, done);
        }
        return;
      } catch (e) {
        const retry = typeof e === "object" && e !== null && (e as { retry?: boolean }).retry === true;
        failures++;
        if (!retry || failures >= TRIES) {
          item.status.textContent = t("Could not be sent");
          item.li.classList.add("failed");
          return;
        }
        item.status.textContent = t("Waiting for the connection");
        await sleep(backoffMs(failures));
      }
    }
  };

  const run = async (): Promise<void> => {
    if (busy) return;
    busy = true;
    if (submit) submit.disabled = true;
    for (const item of items) {
      if (!item.done) await send(item);
    }
    busy = false;
    if (submit) submit.disabled = false;
  };

  input.addEventListener("change", () => {
    add(input.files);
    input.value = "";
  });
  camera?.addEventListener("change", () => {
    add(camera.files);
    camera.value = "";
  });
  form.addEventListener("dragover", (e) => {
    e.preventDefault();
    form.classList.add("dragging");
  });
  form.addEventListener("dragleave", () => form.classList.remove("dragging"));
  form.addEventListener("drop", (e) => {
    e.preventDefault();
    form.classList.remove("dragging");
    add(e.dataTransfer?.files ?? null);
  });
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    if (items.length === 0) return;
    void run();
  });
}

/** The printable card: the browser makes the address (it depends on where the page is served) and asks for its QR code. */
export function initCard(): void {
  const sheet = document.querySelector<HTMLElement>("[data-card-link]");
  if (!sheet) return;
  const url = new URL(sheet.dataset.cardLink ?? "", document.baseURI).href;
  const text = document.getElementById("card-url");
  const box = document.getElementById("card-qr");
  if (text) text.textContent = url;
  document.getElementById("card-print")?.addEventListener("click", () => window.print());
  if (!box) return;
  void fetch("qr", {
    method: "POST",
    headers: { "X-Requested-With": "immich-handoff", "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ text: url }),
  })
    .then((r) => (r.ok ? r.text() : Promise.reject(new Error(String(r.status)))))
    .then((svg) => {
      const img = document.createElement("img");
      img.alt = "";
      img.width = img.height = 320;
      img.src = "data:image/svg+xml;charset=utf-8," + encodeURIComponent(svg);
      box.replaceChildren(img);
    })
    .catch(() => {
      box.textContent = t("The QR code could not be made.");
    });
}
