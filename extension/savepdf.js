// savepdf.js: "Save as PDF" for Google Drive previews whose owner turned off
// downloading (view only). Drive's viewer shows each page as an image; this
// scrolls through the preview so every page loads, captures the pages and
// hands them to BDDM (through savepdf-bg.js), which writes a PDF, PNG images
// or a ZIP of PNG images into the folder you choose (Save to), like other downloads.
// Works on: PDF, Word (.docx) and PowerPoint (.pptx) previews in Drive, and
// Google Docs documents. Not on Google Sheets, Slides or image previews.
/* global api */
(() => {
  const HOST = location.hostname;
  if (HOST !== "drive.google.com" && HOST !== "docs.google.com") return;
  if (window.__bddmSavePdf) return;
  window.__bddmSavePdf = true;

  const ID_RE = /^[A-Za-z0-9_-]{10,}$/;
  const PAGE_WAIT = 20000;   // ms to wait for one page to load
  const PASSES = 3;          // first pass + 2 retries for missing pages
  const HIRES_WIDTH = 1600;  // ask Drive for page images this wide
  const IMAGE_EXT = /\.(jpe?g|png|gif|webp|bmp|tiff?|heic|svg)$/i;
  const UNSUPPORTED_EXT = /\.(xlsx?|ods|csv|tsv)$/i;
  const UNSUPPORTED_MSG = "This preview can't be saved. Use File > Download in the editor.";

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const send = (msg) => new Promise((resolve) => {
    try {
      Promise.resolve(api.runtime.sendMessage(msg)).then(
        (r) => resolve(r || { ok: false, error: "No answer from the BDDM extension." }),
        (e) => resolve({ ok: false, error: (e && e.message) || "The BDDM extension was reloaded. Reload this page." }));
    } catch (e) {
      resolve({ ok: false, error: "The BDDM extension was reloaded. Reload this page." });
    }
  });
  const call = async (msg) => {
    const r = await send(msg);
    if (!r || !r.ok) throw new Error((r && r.error) || "Something went wrong.");
    return r;
  };

  function authuser() {
    const m = location.pathname.match(/\/u\/(\d+)\//);
    if (m) return m[1];
    const q = new URLSearchParams(location.search).get("authuser");
    return q && /^\d+$/.test(q) ? q : "";
  }

  // The file the preview shows. On /file/d/<id>/ pages it is in the address;
  // in the Drive file list it is the item that was opened (double-click or
  // Enter) or else the selected one.
  let openedId = "";
  const rememberOpened = (e) => {
    for (let el = e.target; el && el !== document.documentElement; el = el.parentElement) {
      const id = el.getAttribute && el.getAttribute("data-id");
      if (id && ID_RE.test(id)) { openedId = id; return; }
    }
  };
  window.addEventListener("dblclick", rememberOpened, true);
  window.addEventListener("keydown", (e) => { if (e.key === "Enter") rememberOpened(e); }, true);

  function fileIdFromPage() {
    let m = location.pathname.match(/\/(?:file|document|spreadsheets|presentation)\/d\/([A-Za-z0-9_-]{10,})/);
    if (m) return m[1];
    m = location.search.match(/[?&]id=([A-Za-z0-9_-]{10,})/);
    if (m) return m[1];
    if (window !== window.top) return askTopFrame();
    return listFileId();
  }

  // The file opened or selected in the Drive file list (top frame only).
  function listFileId() {
    if (openedId) return openedId;
    const sel = document.querySelector('[data-id][aria-selected="true"]:not([role="treeitem"])');
    const id = sel && sel.getAttribute("data-id");
    return id && ID_RE.test(id) ? id : "";
  }

  // A preview opened from the Drive file list is shown in a frame
  // (drive.google.com/viewer/main) whose address has no file id: ask the
  // page around it, through the background script, which file was opened.
  let topAnswer = { id: "", at: 0 };
  function askTopFrame() {
    if (Date.now() - topAnswer.at > (topAnswer.id ? 4000 : 1000)) {
      topAnswer.at = Date.now();
      send({ type: "pdfWhichFile" }).then((r) => {
        const id = r && r.ok && r.id && ID_RE.test(r.id) ? r.id : "";
        if (id !== topAnswer.id) { topAnswer.id = id; fileName = ""; refresh(); }
      });
    }
    return topAnswer.id;
  }
  if (window === window.top) {
    try {
      api.runtime.onMessage.addListener((msg, sender, sendResponse) => {
        if (!msg || msg.type !== "pdfOpenedFile") return false;
        sendResponse({ id: listFileId() });
        return false;
      });
    } catch (e) {
      // The extension was reloaded; nothing to do.
    }
  }

  // --- What is on the page --------------------------------------------------

  const visible = (el) => {
    if (!el || !el.getBoundingClientRect) return false;
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0 && getComputedStyle(el).visibility !== "hidden";
  };

  // A page image of Drive's viewer: a blob: image (or the viewer's own /img
  // link) of a reasonable size.
  const isPageImg = (img) => img && img.tagName === "IMG" && img.src &&
    (img.src.startsWith("blob:") || /\/viewer\w*\/img\?|drive-viewer/.test(img.src));

  // The element that holds the pages, with one child per page (Drive keeps an
  // empty, sized box for every page that has not loaded yet).
  function driveViewer() {
    const imgs = [...document.images].filter((i) => isPageImg(i) && i.getBoundingClientRect().width >= 120);
    if (!imgs.length) return null;
    let root = imgs[0].closest('[role="document"]');
    let pageOf = (img) => {
      let e = img;
      while (e && e.parentElement !== root) e = e.parentElement;
      return e;
    };
    if (!root) {
      // Unknown layout: the ancestor with the most page-sized children.
      let best = null, bestN = 0;
      for (let e = imgs[0].parentElement, d = 0; e && e.parentElement && d < 8; e = e.parentElement, d++) {
        const w = e.getBoundingClientRect().width;
        const n = [...e.parentElement.children].filter((c) => Math.abs(c.getBoundingClientRect().width - w) < w * 0.15).length;
        if (n > bestN) { bestN = n; best = e.parentElement; }
      }
      root = best;
      if (!root) return null;
    }
    const pages = [...root.children].filter((c) => {
      const r = c.getBoundingClientRect();
      return r.width >= 100 && r.height >= 100;
    });
    if (!pages.length) return null;
    // "Page 1 of 128" in the toolbar.
    let total = 0;
    for (const el of document.querySelectorAll("[aria-label]")) {
      const m = (el.getAttribute("aria-label") || "").match(/^Page \d+ of (\d+)$/i);
      if (m) { total = Number(m[1]); break; }
    }
    return { root, pages: total > 0 && pages.length > total ? pages.slice(0, total) : pages, total: total || pages.length, pageOf };
  }

  function downloadButtonVisible() {
    for (const el of document.querySelectorAll('[aria-label="Download"], [data-tooltip="Download"]')) {
      if (visible(el) && el.getAttribute("aria-disabled") !== "true") return true;
    }
    return false;
  }

  // What can be done here: {mode: "drive"|"gdoc"|"unsupported", kind, id}
  function context() {
    const path = location.pathname;
    if (HOST === "docs.google.com") {
      if (/\/document\/(u\/\d+\/)?d\//.test(path) && document.querySelector(".kix-page-paginated")) return { mode: "gdoc", kind: "gdoc", id: fileIdFromPage() };
      const m = path.match(/\/(spreadsheets|presentation)\/(u\/\d+\/)?d\//);
      if (m) return { mode: "unsupported", kind: m[1] === "spreadsheets" ? "sheet" : "slides", id: fileIdFromPage() };
      return null;
    }
    const v = driveViewer();
    if (v) return { mode: "drive", kind: "file", id: fileIdFromPage(), viewer: v };
    return null;
  }

  // --- The button and panel (Shadow DOM, so Drive's styles do not reach it) --

  let host = null, shadow = null, panelOpen = false, busy = false, cancelled = false, session = "";
  let ctxNow = null, fileName = "", nameFor = "", nameEdited = false, dirEdited = false;

  const CSS = `
    :host { all: initial; }
    .wrap { position: fixed; top: 104px; right: 24px; z-index: 2147483647; font: 13px/1.4 system-ui, "Segoe UI", sans-serif; color: #202124; }
    button { font: inherit; cursor: pointer; border-radius: 6px; border: 1px solid #c4c7c5; background: #fff; color: #202124; padding: 6px 12px; }
    button:hover { background: #f1f3f4; }
    button.main { background: #1a73e8; color: #fff; border-color: #1a73e8; font-weight: 600; box-shadow: 0 2px 6px rgba(0,0,0,.25); }
    button.main:hover { background: #1765cc; }
    button:disabled { opacity: .5; cursor: default; }
    .panel { margin-top: 8px; width: 300px; background: #fff; border: 1px solid #dadce0; border-radius: 10px; padding: 14px; box-shadow: 0 4px 16px rgba(0,0,0,.25); }
    .panel h3 { margin: 0 0 10px; font-size: 14px; }
    label { display: block; margin: 8px 0 3px; color: #5f6368; font-size: 12px; }
    input, select { font: inherit; width: 100%; box-sizing: border-box; padding: 5px 7px; border: 1px solid #c4c7c5; border-radius: 6px; background: #fff; color: #202124; }
    .row { display: flex; gap: 8px; }
    .row > div { flex: 1; }
    .line { display: flex; gap: 8px; align-items: stretch; }
    .line input { flex: 1; min-width: 0; }
    .line button { flex: 0 0 auto; }
    .buttons { display: flex; gap: 8px; justify-content: flex-end; margin-top: 12px; }
    .status { margin-top: 10px; white-space: pre-wrap; }
    .bar { height: 6px; background: #e8eaed; border-radius: 3px; margin-top: 6px; overflow: hidden; }
    .bar > div { height: 100%; width: 0; background: #1a73e8; transition: width .2s; }
    .err { color: #c5221f; }
    .ok { color: #188038; }
    .note { color: #5f6368; font-size: 12px; margin-top: 8px; }
    [hidden] { display: none !important; }
  `;

  function ui() {
    if (host && host.isConnected) return shadow;
    host = document.createElement("bddm-save-pdf");
    shadow = host.attachShadow({ mode: window.__bddmTest ? "open" : "closed" });
    // Built with DOM calls, not innerHTML (Drive enforces Trusted Types).
    const h = (tag, attrs = {}, ...kids) => {
      const e = document.createElement(tag);
      for (const [k, v] of Object.entries(attrs)) {
        if (k === "text") e.textContent = v;
        else if (k === "hidden") e.hidden = v;
        else e.setAttribute(k, v);
      }
      for (const k of kids) e.append(k);
      return e;
    };
    const opt = (value, text) => h("option", { value, text });
    shadow.append(
      h("style", { text: CSS }),
      h("div", { class: "wrap" },
        h("div", { style: "text-align:right" }, h("button", { class: "main", id: "open", title: "Save this preview with BDDM", text: "Save as PDF" })),
        h("div", { class: "panel", id: "panel", hidden: true },
          h("h3", { text: "Save preview with BDDM" }),
          h("div", { id: "form" },
            h("label", { for: "name", text: "File name" }),
            h("input", { id: "name", spellcheck: "false" }),
            h("label", { for: "format", text: "Save as" }),
            h("select", { id: "format" }, opt("pdf", "PDF"), opt("png", "PNG images (one per page)"), opt("zip", "ZIP of PNG images")),
            h("div", { class: "row", id: "pdfopts" },
              h("div", {}, h("label", { for: "size", text: "Page size" }),
                h("select", { id: "size" }, opt("fit", "Fit to image"), opt("a4", "A4"), opt("a3", "A3"), opt("a5", "A5"),
                  opt("letter", "Letter"), opt("legal", "Legal"), opt("tabloid", "Tabloid"))),
              h("div", {}, h("label", { for: "orient", text: "Orientation" }),
                h("select", { id: "orient" }, opt("portrait", "Portrait"), opt("landscape", "Landscape")))),
            h("div", { id: "qualrow" }, h("label", { for: "quality", text: "Quality" }),
              h("select", { id: "quality" }, opt("high", "High (sharper, bigger file)"), opt("normal", "Normal (as shown on screen)"))),
            h("label", { for: "dir", text: "Save to" }),
            h("div", { class: "line" },
              h("input", { id: "dir", spellcheck: "false", autocomplete: "off" }),
              h("button", { id: "browse", type: "button", text: "Change..." })),
            h("div", { class: "note", id: "note", hidden: true })),
          h("div", { class: "status", id: "status" }),
          h("div", { class: "bar", id: "bar", hidden: true }, h("div")),
          h("div", { class: "buttons" },
            h("button", { id: "show", hidden: true, text: "Show in BDDM" }),
            h("button", { id: "anyway", hidden: true, text: "Save anyway" }),
            h("button", { id: "cancel", text: "Close" }),
            h("button", { class: "main", id: "start", text: "Save" })))));
    const $ = (id) => shadow.getElementById(id);
    $("open").addEventListener("click", () => togglePanel());
    $("cancel").addEventListener("click", () => {
      if (busy) { cancelled = true; setStatus("Cancelling..."); } else togglePanel(false);
    });
    $("start").addEventListener("click", () => start());
    $("name").addEventListener("input", () => { nameEdited = true; });
    $("show").addEventListener("click", () => send({ type: "pdfShowBDDM" }));
    $("format").addEventListener("change", () => { syncOptions(); loadSaveDir(); });
    $("size").addEventListener("change", syncOptions);
    $("dir").addEventListener("input", () => { dirEdited = true; });
    $("browse").addEventListener("click", () => browseFolder());
    // Keys typed in the panel are not Drive's shortcuts.
    for (const ev of ["keydown", "keyup", "keypress"]) shadow.addEventListener(ev, (e) => e.stopPropagation());
    (document.body || document.documentElement).appendChild(host);
    return shadow;
  }

  const $ = (id) => shadow.getElementById(id);

  function syncOptions() {
    const pdf = $("format").value === "pdf";
    $("pdfopts").hidden = !pdf;
    $("orient").disabled = $("size").value === "fit";
  }

  async function loadSaveDir() {
    if (!shadow || dirEdited || !$("dir")) return;
    try {
      const r = await send({ type: "pdfSaveDir", name: $("name").value, format: $("format").value });
      if (r && r.ok && r.dir && !dirEdited) $("dir").value = r.dir;
    } catch (e) { /* Browse still works if BDDM did not answer */ }
  }

  async function browseFolder() {
    const b = $("browse");
    if (b) b.disabled = true;
    try {
      const r = await call({ type: "pdfPickFolder", start: $("dir").value });
      if (r && r.dir) { $("dir").value = r.dir; dirEdited = true; }
    } catch (e) {
      setStatus("Couldn't open the folder picker: " + ((e && e.message) || e), "err");
    } finally {
      if (b) b.disabled = false;
    }
  }

  function setStatus(text, cls = "") {
    const s = $("status");
    s.textContent = text;
    s.className = "status " + cls;
  }

  function setProgress(done, total) {
    $("bar").hidden = !(total > 0);
    $("bar").firstElementChild.style.width = total > 0 ? Math.round((100 * done) / total) + "%" : "0";
  }

  function stem(n) {
    return String(n || "").replace(/\s+-\s+Google (Drive|Docs)\s*$/i, "").replace(/\.(pdf|docx?|pptx?|odt|odp|rtf|txt)$/i, "").trim();
  }

  async function togglePanel(open = !panelOpen) {
    ui();
    panelOpen = open;
    $("panel").hidden = !open;
    if (!open || busy) return;
    const c = ctxNow;
    $("show").hidden = true;
    $("anyway").hidden = true;
    setProgress(0, 0);
    const unsupported = !c || c.mode === "unsupported" || isUnsupportedName(fileName);
    $("form").hidden = unsupported;
    $("start").hidden = unsupported;
    $("cancel").textContent = "Close";
    if (unsupported) {
      setStatus(UNSUPPORTED_MSG);
      return;
    }
    $("note").hidden = c.mode !== "gdoc";
    $("qualrow").hidden = c.mode !== "drive";
    $("note").textContent = "Google Docs pages are saved at screen sharpness. Zoom the browser in (Ctrl +) first for sharper pages.";
    if (!$("name").value || nameFor !== c.id) {
      $("name").value = stem(fileName) || "Google Drive document";
      nameFor = c.id;
      nameEdited = false;
    }
    syncOptions();
    loadSaveDir();
    setStatus("");
  }

  function isUnsupportedName(n) {
    return IMAGE_EXT.test(n || "") || UNSUPPORTED_EXT.test(n || "");
  }

  // --- Show the button only where downloading is turned off ----------------

  const checks = new Map(); // id -> "viewonly" | "ok" | "unknown" | "pending"
  const names = new Map();

  function checkState(kind, id) {
    if (!id) return "unknown";
    const key = kind + ":" + id;
    if (!checks.has(key)) {
      checks.set(key, "pending");
      send({ type: "pdfCheck", kind, id, authuser: authuser() }).then((r) => {
        checks.set(key, (r && r.state) || "unknown");
        refresh();
      });
    }
    return checks.get(key);
  }

  function realName(kind, id) {
    if (!id) return "";
    const key = kind + ":" + id;
    if (!names.has(key)) {
      names.set(key, "");
      send({ type: "pdfName", kind, id, authuser: authuser() }).then((r) => {
        if (r && r.name) { names.set(key, r.name); refresh(); }
      });
    }
    return names.get(key) || "";
  }

  // The name from this page itself, only where it is reliable: the tab title
  // of a /file/d/ or Docs page (never labels or buttons around the preview).
  function titleName() {
    if (!/\/(file|document)\/(u\/\d+\/)?d\//.test(location.pathname)) return "";
    const t = document.title.replace(/\s+-\s+Google (Drive|Docs)\s*$/i, "").trim();
    return /^(google drive|google docs|ask gemini)$/i.test(t) ? "" : t;
  }

  function refresh() {
    if (busy) return;
    const c = context();
    ctxNow = c;
    let show = false;
    if (c) {
      const state = checkState(c.kind, c.id);
      fileName = realName(c.kind === "gdoc" ? "gdoc" : "file", c.id) || titleName();
      if (c.mode === "drive") show = state === "viewonly" || (state !== "ok" && state !== "pending" && !downloadButtonVisible());
      else if (c.mode === "gdoc") show = state === "viewonly";
      else show = false; // Sheets and Slides: only when the owner turned off downloading
      if (c.mode === "unsupported") show = state === "viewonly";
    }
    // The real name arrived after the panel was opened.
    if (panelOpen && shadow && fileName && !nameEdited && c && (nameFor === c.id || !nameFor) && $("name").value !== stem(fileName)) {
      $("name").value = stem(fileName);
      nameFor = c.id;
    }
    if (show || panelOpen) {
      ui();
      host.hidden = false;
      $("open").textContent = "Save as PDF";
    } else if (host) {
      host.hidden = true;
      panelOpen = false;
      $("panel").hidden = true;
    }
  }

  // --- Capturing ------------------------------------------------------------

  function canvasPng(draw, w, h) {
    const c = document.createElement("canvas");
    c.width = w;
    c.height = h;
    const g = c.getContext("2d");
    g.fillStyle = "#fff";
    g.fillRect(0, 0, w, h);
    draw(g);
    return c.toDataURL("image/png"); // throws if the image is from another site
  }

  async function imgToPng(img) {
    try {
      return { data: canvasPng((g) => g.drawImage(img, 0, 0), img.naturalWidth, img.naturalHeight), w: img.naturalWidth, h: img.naturalHeight };
    } catch (e) {
      if (img.src.startsWith("blob:")) {
        // Read the page's own image data and draw that instead.
        const bmp = await createImageBitmap(await (await fetch(img.src)).blob());
        const out = { data: canvasPng((g) => g.drawImage(bmp, 0, 0), bmp.width, bmp.height), w: bmp.width, h: bmp.height };
        bmp.close();
        return out;
      }
      // An image from another Google host: the extension loads it itself.
      const r = await call({ type: "pdfFetchImage", url: img.src });
      return { data: r.data, w: r.w, h: r.h };
    }
  }

  function scroller(el) {
    for (let e = el.parentElement; e; e = e.parentElement) {
      const s = getComputedStyle(e).overflowY;
      if ((s === "auto" || s === "scroll") && e.scrollHeight > e.clientHeight + 10) return e;
    }
    return document.scrollingElement || document.documentElement;
  }

  const loadedImg = (pageEl) => {
    const img = [...pageEl.querySelectorAll("img")].find((i) => isPageImg(i) && i.complete && i.naturalWidth > 0);
    return img || null;
  };

  async function waitFor(fn, ms) {
    const end = Date.now() + ms;
    for (;;) {
      if (cancelled) return null;
      const v = fn();
      if (v) return v;
      if (Date.now() > end) return null;
      await sleep(150);
    }
  }

  // Drive preview (PDF, Word, PowerPoint): every page is an image.
  async function captureDrive(c, upload, hires) {
    const v = c.viewer;
    const total = v.pages.length;
    const sc = scroller(v.root);
    const startTop = sc.scrollTop;
    const got = new Set();
    try {
      for (let pass = 0; pass < PASSES && got.size < total && !cancelled; pass++) {
        for (let i = 0; i < total && !cancelled; i++) {
          if (got.has(i)) continue;
          const pageEl = driveViewer() ? driveViewer().pages[i] || v.pages[i] : v.pages[i];
          setStatus(`Capturing page ${got.size + 1} of ${total}...` + (pass ? ` (retry ${pass})` : ""));
          if (pass) { sc.scrollTop = Math.max(0, pageEl.offsetTop - 3000); await sleep(400); }
          pageEl.scrollIntoView({ block: "start" });
          const img = await waitFor(() => loadedImg(pageEl), PAGE_WAIT * (pass + 1) / 2);
          if (!img) continue;
          let page = null;
          if (hires && img.naturalWidth < HIRES_WIDTH) {
            const r = await send({ type: "pdfHiRes", page: i, width: HIRES_WIDTH });
            // Use it only when it really is this page (same shape).
            if (r && r.ok && r.data && Math.abs(r.w / r.h - img.naturalWidth / img.naturalHeight) < 0.03) page = r;
          }
          if (!page) page = await imgToPng(img);
          await upload(i, page.data);
          got.add(i);
          setProgress(got.size, total);
        }
      }
    } finally {
      sc.scrollTop = startTop;
    }
    return { total, got };
  }

  // Google Docs: pages are drawn on <canvas> and only the pages near the
  // screen exist, so scroll down the document page by page.
  async function captureDoc(upload) {
    const sc = document.querySelector(".kix-appview-editor") || scroller(document.querySelector(".kix-page-paginated"));
    const startTop = sc.scrollTop;
    const pageIndex = (p) => {
      const z = parseInt(p.style.zIndex, 10);
      return Number.isFinite(z) ? z : -1;
    };
    const first = document.querySelector(".kix-page-paginated");
    const pitch = first.offsetHeight + 10;
    let total = Math.max(1, Math.round((sc.scrollHeight - first.offsetTop) / pitch));
    const got = new Set();
    try {
      for (let pass = 0; pass < PASSES && got.size < total && !cancelled; pass++) {
        sc.scrollTop = 0;
        await sleep(500);
        for (let guard = 0; guard < 5000 && !cancelled; guard++) {
          const pages = [...document.querySelectorAll(".kix-page-paginated")];
          for (const p of pages) {
            const n = pageIndex(p);
            if (n < 0 || got.has(n)) continue;
            const r = p.getBoundingClientRect(), sr = sc.getBoundingClientRect();
            if (r.bottom < sr.top || r.top > sr.bottom) continue; // not drawn yet
            const canvases = [...p.querySelectorAll("canvas")].filter((x) => x.width > 0);
            if (!canvases.length) continue;
            // Wait until Docs has finished drawing the page (no change for a moment).
            let prev = "";
            const stable = await waitFor(() => {
              const now = canvases.map((x) => x.toDataURL("image/png").length).join(",");
              const ok = now === prev;
              prev = now;
              return ok;
            }, 4000);
            if (!stable && cancelled) break;
            const scale = canvases[0].width / canvases[0].getBoundingClientRect().width || 1;
            const pr = p.getBoundingClientRect();
            const data = canvasPng((g) => {
              for (const cv of canvases) {
                const cr = cv.getBoundingClientRect();
                g.drawImage(cv, (cr.left - pr.left) * scale, (cr.top - pr.top) * scale, cr.width * scale, cr.height * scale);
              }
            }, Math.round(pr.width * scale), Math.round(pr.height * scale));
            await upload(n, data);
            got.add(n);
            total = Math.max(total, n + 1);
            setStatus(`Capturing page ${got.size} of ${total}...` + (pass ? ` (retry ${pass})` : ""));
            setProgress(got.size, total);
          }
          if (sc.scrollTop + sc.clientHeight >= sc.scrollHeight - 2) {
            // At the end: the real page count is known now.
            total = Math.max(got.size, ...[...got].map((n) => n + 1), 1);
            await sleep(600);
            const last = [...document.querySelectorAll(".kix-page-paginated")].map(pageIndex);
            total = Math.max(total, ...last.map((n) => n + 1));
            if (last.every((n) => got.has(n))) break;
            continue;
          }
          sc.scrollTop += Math.max(200, Math.round(sc.clientHeight * 0.8));
          await sleep(350);
        }
      }
    } finally {
      sc.scrollTop = startTop;
    }
    return { total, got };
  }

  async function start() {
    if (busy) return;
    const c = ctxNow || context();
    if (!c || c.mode === "unsupported") return;
    let dir = ($("dir") && $("dir").value.trim()) || "";
    if (!dir) {
      await browseFolder();
      dir = ($("dir") && $("dir").value.trim()) || "";
    }
    if (!dir) {
      setStatus("Choose a folder to save to.", "err");
      return;
    }
    busy = true;
    cancelled = false;
    session = "";
    $("start").disabled = true;
    $("form").querySelectorAll("input,select,button").forEach((e) => { if (e.id !== "cancel") e.disabled = true; });
    $("cancel").textContent = "Cancel";
    $("show").hidden = true;
    $("anyway").hidden = true;
    const options = {
      name: $("name").value.trim() || stem(fileName) || "Google Drive document",
      format: $("format").value, pageSize: $("size").value, orientation: $("orient").value,
      dir,
    };
    try {
      setStatus("Starting...");
      const pages = c.mode === "drive" ? c.viewer.pages.length : 0;
      const src = c.mode === "drive" && c.id ? "https://drive.google.com/file/d/" + c.id + "/view" : location.href;
      const st = await call({ type: "pdfStart", name: options.name, url: src, pages });
      session = st.id;
      const upload = (n, data) => call({ type: "pdfPage", id: session, n, data });
      const res = c.mode === "gdoc" ? await captureDoc(upload) : await captureDrive(c, upload, $("quality").value === "high");
      if (cancelled) throw new Error("cancelled");
      const missing = res.total - res.got.size;
      if (!res.got.size) throw new Error("No page could be loaded. Scroll through the preview once and try again.");
      if (missing > 0) {
        setStatus(`${missing} of ${res.total} pages could not be loaded.`, "err");
        const go = await askAnyway();
        if (!go) throw new Error("cancelled");
        options.allowMissing = true;
      }
      setStatus(options.format === "pdf" ? "Building the PDF..." : "Saving the images...");
      const fin = await call({ type: "pdfFinish", id: session, options });
      session = "";
      setStatus("Saved to " + (fin.dir ? fin.dir + ": " : "BDDM: ") + fin.name + " (" + fin.pages + " pages)", "ok");
      $("show").hidden = false;
    } catch (e) {
      if (session) send({ type: "pdfCancel", id: session });
      session = "";
      if (e.message === "cancelled") setStatus("Cancelled.");
      else setStatus("Couldn't save: " + e.message, "err");
    } finally {
      busy = false;
      cancelled = false;
      $("start").disabled = false;
      $("form").querySelectorAll("input,select,button").forEach((el) => { el.disabled = false; });
      syncOptions();
      $("cancel").textContent = "Close";
      setProgress(0, 0);
    }
  }

  function askAnyway() {
    return new Promise((resolve) => {
      const a = $("anyway");
      a.hidden = false;
      const done = (v) => { a.hidden = true; a.onclick = null; clearInterval(t); resolve(v); };
      a.onclick = () => done(true);
      const t = setInterval(() => { if (cancelled) done(false); }, 200);
    });
  }

  setInterval(refresh, 1500);
  refresh();
})();
