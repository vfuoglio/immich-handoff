// The legacy TV page, for browsers from about 2016: the same job as tv.ts (show a code, then the photos the person chose, and do what
// they tell it from their phone) in plain old JavaScript: no promises, no modules, no classes, no newer syntax. It only fades. The
// build turns it into ES5 and a test checks that no newer syntax got in.

(function legacyTV(): void {
  var root = document.getElementById("tv");
  if (!root) return;
  var codeEl = root.querySelector(".tv-code") as HTMLElement | null;
  var hintEl = root.querySelector(".tv-hint") as HTMLElement | null;
  var codeBox = root.querySelector(".tv-pair") as HTMLElement | null;
  var layers = root.querySelectorAll(".tvl-layer");
  if (!codeEl || !hintEl || !codeBox || layers.length < 2) return;
  var address = location.host + location.pathname.replace(/\/tv(\/legacy)?\/?$/, "/");
  hintEl.textContent = (hintEl.getAttribute("data-hint") || "").replace("{address}", address);

  var secret = "";
  var items: string[] | null = null;
  var shown: string[] = [];
  var history: string[] = [];
  var seq = 0;
  var paused = false;
  var holdMs = 15000;
  var ordered = false;
  var cursor = -1;
  var front = 0;
  var slideTimer: number | undefined;
  var busy = false;
  var feeding = false;

  function get(url: string, done: (status: number, body: string) => void): void {
    var x = new XMLHttpRequest();
    x.open("GET", url, true);
    x.onreadystatechange = function (): void {
      if (x.readyState === 4) done(x.status, x.responseText);
    };
    x.send();
  }
  function post(url: string, done: (status: number, body: string) => void): void {
    var x = new XMLHttpRequest();
    x.open("POST", url, true);
    x.onreadystatechange = function (): void {
      if (x.readyState === 4) done(x.status, x.responseText);
    };
    x.send("");
  }
  function parse(body: string): any {
    try {
      return JSON.parse(body);
    } catch (e) {
      return null;
    }
  }
  function contains(list: string[], id: string): boolean {
    for (var i = 0; i < list.length; i++) if (list[i] === id) return true;
    return false;
  }
  function chooseNext(): string {
    if (!items || items.length === 0) return "";
    if (ordered) { // a story is played in the order it was made
      cursor = (cursor + 1) % items.length;
      return items[cursor] as string;
    }
    var pool: string[] = [];
    var i: number;
    for (i = 0; i < items.length; i++) if (!contains(shown, items[i] as string)) pool.push(items[i] as string);
    if (pool.length === 0) {
      shown = [];
      for (i = 0; i < items.length; i++) if (items.length === 1 || items[i] !== history[history.length - 1]) pool.push(items[i] as string);
    }
    var id = pool[Math.floor(Math.random() * pool.length)] as string;
    shown.push(id);
    return id;
  }
  function arm(): void {
    if (slideTimer !== undefined) clearTimeout(slideTimer);
    slideTimer = undefined;
    if (paused) return;
    slideTimer = window.setTimeout(function (): void { step("next"); }, holdMs);
  }
  function show(id: string, done: (ok: boolean) => void): void {
    var img = new Image();
    var timeout = window.setTimeout(function (): void { img.onload = img.onerror = null; done(false); }, 30000);
    img.onload = function (): void {
      clearTimeout(timeout);
      var back = (front + 1) % 2;
      var layer = layers[back] as HTMLElement;
      layer.style.backgroundImage = "url(" + img.src + ")";
      layer.className = "tvl-layer in";
      (layers[front] as HTMLElement).className = "tvl-layer";
      front = back;
      done(true);
    };
    img.onerror = function (): void { clearTimeout(timeout); done(false); };
    img.src = "tv/photo?s=" + secret + "&id=" + id;
  }
  function step(way: string, tries?: number): void {
    if (busy || !items) return;
    busy = true;
    var id: string;
    if (way === "prev" && history.length > 1) {
      history.pop();
      id = history[history.length - 1] as string;
    } else {
      id = chooseNext();
      if (id) history.push(id);
    }
    if (!id) { busy = false; return; }
    show(id, function (ok: boolean): void {
      busy = false;
      if (!ok && (tries || 0) < 2) { step(way, (tries || 0) + 1); return; }
      arm();
    });
  }
  function feed(reset: boolean): void {
    get("tv/feed?s=" + secret, function (status: number, body: string): void {
      var data = status === 200 ? parse(body) : null;
      var wait = 20000;
      if (data && data.items) {
        var ids: string[] = [];
        for (var i = 0; i < data.items.length; i++) ids.push(data.items[i].id);
        var first = items === null || reset;
        items = ids;
        ordered = data.ordered === true;
        if (reset) { shown = []; history = []; cursor = -1; }
        wait = Math.max(5, data.refresh || 120) * 1000;
        if (first) step("next");
      }
      window.setTimeout(function (): void { feed(false); }, wait);
    });
  }
  function begin(): void {
    items = null; shown = []; history = []; seq = 0; feeding = false;
    if (slideTimer !== undefined) clearTimeout(slideTimer);
    codeBox!.style.display = "";
    post("tv/new", function (status: number, body: string): void {
      var d = status === 200 ? parse(body) : null;
      if (!d) { window.setTimeout(begin, 30000); return; }
      secret = d.s;
      codeEl!.textContent = String(d.code).toUpperCase();
      window.setTimeout(poll, 2000);
    });
  }
  function poll(): void {
    get("tv/state?s=" + secret, function (status: number, body: string): void {
      var st = status === 200 ? parse(body) : null;
      if (!st) { window.setTimeout(poll, 8000); return; }
      if (st.state === "expired" || st.state === "ended") { begin(); return; }
      if (st.state === "live") {
        codeBox!.style.display = "none";
        holdMs = Math.max(5, st.seconds || 15) * 1000;
        var was = paused;
        paused = st.paused === true;
        if (!feeding) {
          feeding = true;
          seq = st.seq || 0;
          feed(true);
        } else if ((st.seq || 0) !== seq) {
          seq = st.seq || 0;
          if (st.cmd === "next") step("next");
          else if (st.cmd === "prev") step("prev");
          else if (st.cmd === "source") { items = null; shown = []; history = []; cursor = -1; get("tv/feed?s=" + secret, function (s2: number, b2: string): void { var d2 = s2 === 200 ? parse(b2) : null; if (d2 && d2.items) { var ids2: string[] = []; for (var j = 0; j < d2.items.length; j++) ids2.push(d2.items[j].id); items = ids2; ordered = d2.ordered === true; step("next"); } }); }
        }
        if (was !== paused) arm();
      }
      window.setTimeout(poll, 3000);
    });
  }
  begin();
})();
