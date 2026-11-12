// The guest page of a drop box: choose or drop photos (or take one), shrink big ones when the owner asked for it, send
// them one at a time with a progress bar, and carry on after a lost connection. Without this script the form still
// works as a plain form; everything here is an improvement on it.

import { n, t } from "./i18n.ts";

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
  prompt: string; // the id of the prompt of the shot list it is for ("" for none)
  li: HTMLLIElement;
  status: HTMLElement;
  bar: HTMLProgressElement;
  preview: string; // an object URL to give back when the tile goes ("" for none)
  done: boolean;
  sending: boolean;
}

/** A size people can read: 840 KB, 12.4 MB. */
export function formatSize(bytes: number): string {
  if (bytes < 1024 * 1024) return `${Math.max(1, Math.round(bytes / 1024))} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}

/** Whether a box takes this file. A file the browser cannot name the type of is let through: the server decides. */
export function takes(file: { type: string }, photosOnly: boolean): boolean {
  if (file.type === "") return true;
  return file.type.startsWith("image/") || (!photosOnly && file.type.startsWith("video/"));
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
function post(url: string, password: string | null, prompt: string, file: File, onProgress: (fraction: number) => void): Promise<Answer> {
  return new Promise((resolve, reject) => {
    const body = new FormData();
    if (password !== null) body.append("password", password); // before the files: the server reads in order
    if (prompt !== "") body.append("prompt", prompt);
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

const SVG = "http://www.w3.org/2000/svg";

/** A small picture to show for a file: shrunk by the browser when it can, the file itself when it cannot, nothing for a video. */
async function thumbnail(file: File): Promise<string> {
  if (!file.type.startsWith("image/")) return "";
  try {
    if (typeof createImageBitmap === "function") {
      const bitmap = await createImageBitmap(file, { resizeWidth: 320, resizeQuality: "low" });
      const canvas = document.createElement("canvas");
      canvas.width = bitmap.width;
      canvas.height = bitmap.height;
      canvas.getContext("2d")?.drawImage(bitmap, 0, 0);
      bitmap.close();
      const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, "image/jpeg", 0.8));
      if (blob) return URL.createObjectURL(blob);
    }
  } catch {
    /* fall through to the file itself */
  }
  return file.size < 25 * 1024 * 1024 ? URL.createObjectURL(file) : "";
}

function check(): SVGSVGElement {
  const svg = document.createElementNS(SVG, "svg");
  svg.setAttribute("viewBox", "0 0 52 52");
  svg.setAttribute("class", "drop-check");
  svg.setAttribute("aria-hidden", "true");
  const circle = document.createElementNS(SVG, "circle");
  circle.setAttribute("cx", "26");
  circle.setAttribute("cy", "26");
  circle.setAttribute("r", "23");
  const tick = document.createElementNS(SVG, "path");
  tick.setAttribute("d", "M15 27l8 8 15-17");
  svg.append(circle, tick);
  return svg;
}

export function initDrop(): void {
  const form = document.querySelector<HTMLFormElement>("form.drop-form");
  const list = form?.querySelector<HTMLUListElement>("ul.drop-list");
  if (!form || !list) return;
  const input = form.querySelector<HTMLInputElement>('input[type="file"][name="files"]');
  const camera = form.querySelector<HTMLInputElement>('input[type="file"][name="camera"]');
  const password = form.querySelector<HTMLInputElement>('input[name="password"]');
  const promptSelect = form.querySelector<HTMLSelectElement>('select[name="prompt"]');
  const submit = form.querySelector<HTMLButtonElement>("button.drop-send");
  const zone = form.querySelector<HTMLElement>(".dz");
  const summary = form.querySelector<HTMLElement>(".drop-summary");
  const count = form.querySelector<HTMLElement>(".drop-count");
  const clear = form.querySelector<HTMLButtonElement>(".drop-clear");
  const result = form.querySelector<HTMLElement>(".drop-result");
  if (!input || !submit) return;
  const key = form.dataset.key ?? "";
  const maxDim = Number(form.dataset.maxdim ?? "0") || 0;
  const photosOnly = zone?.hasAttribute("data-photos-only") ?? false;
  const label = submit.dataset.label ?? submit.textContent ?? "";
  const done = readDone(key);
  const items: Item[] = [];
  let busy = false;
  form.classList.add("enhanced"); // the plain file field stays when this script does not run
  input.required = false; // with this script, Send says what is missing itself

  const say = (text: string, kind: "ok" | "bad" | "" = ""): void => {
    if (!result) return;
    result.replaceChildren();
    result.hidden = text === "";
    result.className = "drop-result" + (kind ? " " + kind : "");
    if (text === "") return;
    if (kind === "ok") result.append(check());
    const span = document.createElement("span");
    span.textContent = text;
    result.append(span);
  };

  const refresh = (): void => {
    const total = items.reduce((sum, i) => sum + i.file.size, 0);
    list.hidden = items.length === 0;
    if (summary) summary.hidden = items.length === 0;
    if (count) count.textContent = n("{n} file", "{n} files", items.length) + " · " + formatSize(total);
    if (clear) clear.hidden = busy;
  };

  const setState = (item: Item, state: "waiting" | "sending" | "done" | "failed"): void => {
    item.li.dataset.state = state;
    item.sending = state === "sending";
  };

  const remove = (item: Item): void => {
    if (item.sending) return;
    items.splice(items.indexOf(item), 1);
    if (item.preview) URL.revokeObjectURL(item.preview);
    const gone = (): void => item.li.remove();
    item.li.classList.add("leaving");
    item.li.addEventListener("animationend", gone, { once: true });
    setTimeout(gone, 400); // when animations are off there is no animationend
    refresh();
    say("");
  };

  const add = (files: FileList | File[] | null): void => {
    const prompt = promptSelect?.value ?? "";
    const given = Array.from(files ?? []).filter((f) => f.size > 0 || f.type !== ""); // a dropped folder is no file
    if (given.length === 0) return;
    if (promptSelect?.required && prompt === "") {
      // every photo has to say what it is for: ask before queueing anything
      promptSelect.focus();
      say(t("Choose what the photo is for"), "bad");
      return;
    }
    const promptText = promptSelect && prompt !== "" ? (promptSelect.selectedOptions[0]?.textContent ?? "") : "";
    const left: string[] = [];
    say("");
    for (const file of given) {
      if (!takes(file, photosOnly)) {
        left.push(file.name);
        continue;
      }
      if (items.some((i) => fileKey(i.file) === fileKey(file))) continue;
      const li = document.createElement("li");
      li.className = "drop-item";
      const thumb = document.createElement("div");
      thumb.className = "thumb" + (file.type.startsWith("video/") ? " video" : "");
      const bar = document.createElement("progress");
      bar.max = 1;
      bar.value = 0;
      const badge = document.createElement("span");
      badge.className = "badge";
      badge.setAttribute("aria-hidden", "true");
      thumb.append(bar, badge);
      const remover = document.createElement("button");
      remover.type = "button";
      remover.className = "rm";
      remover.textContent = "×";
      remover.setAttribute("aria-label", t("Remove {name}", { name: file.name }));
      const name = document.createElement("span");
      name.className = "nm";
      name.textContent = promptText ? `${file.name} · ${promptText}` : file.name;
      const status = document.createElement("span");
      status.className = "meta st";
      li.append(thumb, remover, name, status);
      li.style.setProperty("--i", String(Math.min(items.length, 12)));
      list.append(li);
      const item: Item = { file, prompt, li, status, bar, preview: "", done: done.has(fileKey(file)), sending: false };
      remover.addEventListener("click", () => remove(item));
      status.textContent = item.done ? t("Sent already") : t("Waiting to send");
      setState(item, item.done ? "done" : "waiting");
      if (item.done) bar.value = 1;
      items.push(item);
      void thumbnail(file).then((url) => {
        if (!url) return;
        if (!items.includes(item)) {
          URL.revokeObjectURL(url);
          return;
        }
        const img = document.createElement("img");
        img.alt = "";
        img.decoding = "async";
        img.addEventListener("error", () => img.remove());
        img.src = url;
        item.preview = url;
        thumb.prepend(img);
        thumb.classList.add("shown");
      });
    }
    if (left.length > 0) say(n("Only photos are taken here: {name} was left out", "Only photos are taken here: {n} files were left out", left.length, { name: left[0] ?? "" }), "bad");
    refresh();
  };

  const send = async (item: Item): Promise<void> => {
    const url = form.action;
    let failures = 0;
    setState(item, "sending");
    for (;;) {
      await whenOnline();
      item.status.textContent = failures === 0 ? t("Sending") : t("Waiting for the connection");
      try {
        const file = failures === 0 ? await shrink(item.file, maxDim) : item.file;
        const answer = await post(url, password ? password.value : null, item.prompt, file, (f) => {
          item.bar.value = f;
        });
        const line = answer.files?.[0];
        item.bar.value = 1;
        item.status.textContent = line?.text ?? t("Received");
        if (line?.ok === false) {
          setState(item, "failed");
        } else {
          setState(item, "done");
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
          setState(item, "failed");
          return;
        }
        item.status.textContent = t("Waiting for the connection");
        await sleep(backoffMs(failures));
      }
    }
  };

  const run = async (): Promise<void> => {
    if (busy) return;
    const pending = items.filter((i) => !i.done);
    if (pending.length === 0) {
      // nothing to send: say so, and show where to put photos
      say(items.length === 0 ? t("Choose photos first") : t("Everything here was sent already"), items.length === 0 ? "bad" : "");
      zone?.classList.add("nudge");
      zone?.addEventListener("animationend", () => zone.classList.remove("nudge"), { once: true });
      return;
    }
    busy = true;
    say("");
    submit.disabled = true;
    form.classList.add("sending");
    refresh();
    let finished = 0;
    for (const item of pending) {
      submit.textContent = t("Sending {done} of {total}", { done: finished + 1, total: pending.length });
      await send(item);
      finished++;
    }
    busy = false;
    form.classList.remove("sending");
    submit.disabled = false;
    refresh();
    const failed = pending.filter((i) => !i.done).length;
    const sent = pending.length - failed;
    submit.textContent = failed > 0 ? t("Try again") : label;
    if (failed > 0) {
      say(n("{n} file could not be sent", "{n} files could not be sent", failed), "bad");
    } else {
      say(n("{n} file sent. Thank you!", "{n} files sent. Thank you!", sent), "ok");
    }
  };

  input.addEventListener("change", () => {
    add(input.files);
    input.value = "";
  });
  camera?.addEventListener("change", () => {
    add(camera.files);
    camera.value = "";
  });
  clear?.addEventListener("click", () => {
    for (const item of [...items]) remove(item);
  });

  // files can be dropped anywhere on the page; one dropped beside the form must not make the browser open it
  const hasFiles = (e: DragEvent): boolean => Array.from(e.dataTransfer?.types ?? []).includes("Files");
  let depth = 0;
  document.addEventListener("dragenter", (e) => {
    if (!hasFiles(e)) return;
    depth++;
    form.classList.add("dragging");
  });
  document.addEventListener("dragleave", (e) => {
    if (!hasFiles(e)) return;
    depth = Math.max(0, depth - 1);
    if (depth === 0) form.classList.remove("dragging");
  });
  document.addEventListener("dragover", (e) => {
    if (hasFiles(e)) e.preventDefault();
  });
  document.addEventListener("drop", (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    depth = 0;
    form.classList.remove("dragging");
    add(e.dataTransfer?.files ?? null);
  });
  window.addEventListener("beforeunload", (e) => {
    if (busy) e.preventDefault(); // what is still being sent would be lost
  });
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    void run();
  });
  refresh();
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

/** The guest book: a message, and a voice note recorded in the browser when it can. Without a script the form still sends the words. */
export function initGuestBook(): void {
  const form = document.querySelector<HTMLFormElement>("form.guestbook-form");
  if (!form) return;
  const tools = form.querySelector<HTMLElement>(".voice-tools");
  const record = form.querySelector<HTMLButtonElement>("button.voice-record");
  const preview = form.querySelector<HTMLAudioElement>("audio.voice-preview");
  const status = form.querySelector<HTMLElement>(".guestbook-status");
  let voice: Blob | null = null;
  let recorder: MediaRecorder | null = null;
  let stopTimer: ReturnType<typeof setTimeout> | undefined;
  const canRecord = typeof MediaRecorder !== "undefined" && !!navigator.mediaDevices?.getUserMedia;
  if (tools && record && preview && canRecord) {
    tools.hidden = false;
    record.addEventListener("click", () => {
      if (recorder) {
        recorder.stop();
        return;
      }
      void navigator.mediaDevices
        .getUserMedia({ audio: true })
        .then((stream) => {
          const chunks: Blob[] = [];
          const r = new MediaRecorder(stream);
          recorder = r;
          r.ondataavailable = (e) => chunks.push(e.data);
          r.onstop = () => {
            stream.getTracks().forEach((track) => track.stop());
            clearTimeout(stopTimer);
            voice = new Blob(chunks, { type: r.mimeType });
            preview.src = URL.createObjectURL(voice);
            preview.hidden = false;
            record.textContent = t("Record again");
            recorder = null;
          };
          r.start();
          record.textContent = t("Stop recording");
          stopTimer = setTimeout(() => r.stop(), 90_000); // a voice note is a greeting, not a recording of the party
        })
        .catch(() => {
          if (status) status.textContent = t("The microphone is not available");
        });
    });
  }
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    const body = new FormData(form);
    if (voice) body.append("voice", voice, "voice");
    void fetch(form.action, { method: "POST", headers: { Accept: "application/json" }, body })
      .then(async (r) => ({ ok: r.ok, answer: (await r.json()) as { ok?: boolean; text?: string } }))
      .then(({ ok, answer }) => {
        if (status) status.textContent = ok && answer.ok ? t("Thank you for your message!") : (answer.text ?? t("Could not send the message"));
        if (ok && answer.ok) {
          form.reset();
          voice = null;
          if (preview) preview.hidden = true;
          if (record) record.textContent = t("Record a voice message");
        }
      })
      .catch(() => {
        if (status) status.textContent = t("Could not send the message");
      });
  });
}

/** The address of a box's shared photos: Immich's page, on the server the browser is already talking to unless the app was told where Immich is. */
export function initShare(): void {
  document.querySelectorAll<HTMLElement>("[data-share-key]").forEach((el) => {
    const key = el.dataset.shareKey ?? "";
    const url = el.dataset.shareUrl || new URL("/share/" + key, location.origin).href;
    if (el instanceof HTMLInputElement) el.value = url;
    if (el instanceof HTMLAnchorElement) el.href = url;
  });
}
