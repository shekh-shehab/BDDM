// savepdf-bg.js: the background half of "Save as PDF" (savepdf.js). It checks
// whether a Drive file or Google Doc can be downloaded, reads its real name,
// fetches sharper page images, and passes the captured pages to BDDM, which
// writes the PDF (or PNG images) into the folder the user chose.
/* global api, BDDM */

const VIEW_ONLY_RE = /can(?:'|&#39;|&#x27;|’)?t download (?:this )?file|cannot download (?:this )?file|disabled download|turned off download|download, print,? and copy|only the owner can download|has disabled downloading/i;

// The page images Drive's viewer asked for, per tab: page number -> URL.
// (Drive's viewer loads them as /viewer/img?...&page=N&w=800.)
const viewerImgs = new Map();
api.webRequest.onBeforeRequest.addListener((d) => {
  if (d.tabId < 0) return;
  let u;
  try { u = new URL(d.url); } catch (e) { return; }
  if (!/\/img$/.test(u.pathname)) return;
  const page = u.searchParams.get("page");
  if (page === null || !/^\d+$/.test(page)) return;
  let m = viewerImgs.get(d.tabId);
  if (!m) { m = new Map(); viewerImgs.set(d.tabId, m); }
  m.set(Number(page), d.url);
  if (m.size > 3000) m.delete(m.keys().next().value);
}, { urls: ["https://drive.google.com/*", "https://docs.google.com/*"] });
api.tabs.onRemoved.addListener((tabId) => viewerImgs.delete(tabId));

const checkCache = new Map(); // "kind:id:authuser" -> {state, at}

// Reads only the start of a response: enough to tell a file from a web page.
async function peek(url, ms = 15000) {
  const ctrl = new AbortController();
  const t = setTimeout(() => ctrl.abort(), ms);
  try {
    const r = await fetch(url, { credentials: "include", redirect: "follow", signal: ctrl.signal });
    const type = (r.headers.get("content-type") || "").toLowerCase();
    if (!type.includes("text/html")) {
      ctrl.abort(); // a real file: do not download it
      return { status: r.status, type, html: "", finalUrl: r.url };
    }
    const html = (await r.text()).slice(0, 200000);
    return { status: r.status, type, html, finalUrl: r.url };
  } finally {
    clearTimeout(t);
  }
}

// "viewonly": downloading is turned off; "ok": it can be downloaded;
// "unknown": could not tell (not signed in, network...).
async function checkDownload(kind, id, authuser) {
  const key = kind + ":" + id + ":" + authuser;
  const c = checkCache.get(key);
  if (c && Date.now() - c.at < 10 * 60e3) return c.state;
  let state = "unknown";
  try {
    const au = authuser !== "" ? "&authuser=" + encodeURIComponent(authuser) : "";
    const editor = { gdoc: "document", sheet: "spreadsheets", slides: "presentation" }[kind];
    if (editor) {
      const r = await peek("https://docs.google.com/" + editor + "/d/" + encodeURIComponent(id) + "/export?format=pdf" + au);
      if (r.status === 200 && r.type.includes("pdf")) state = "ok";
      else if (VIEW_ONLY_RE.test(r.html) || r.status === 403) state = "viewonly";
    } else {
      const r = await peek("https://drive.usercontent.google.com/download?id=" + encodeURIComponent(id) + "&export=download" + au);
      if (r.status === 200 && !r.html) state = "ok";
      else if (VIEW_ONLY_RE.test(r.html)) state = "viewonly";
      else if (/download-form|id="uc-download-link"|virus scan|name="confirm"/i.test(r.html)) state = "ok"; // big file: Google's virus-scan note
    }
  } catch (e) {
    state = "unknown";
  }
  checkCache.set(key, { state, at: Date.now() });
  return state;
}

// Names that are Drive's own buttons and labels, never a file name.
const UI_NAMES = /^(ask gemini|gemini|google drive|google docs|my drive|shared with me|home|recent|starred|trash|details|activity|share|download|print|open with|more actions|close|sign in|sign-in|untitled|preview|showing viewer\.?)$/i;

function decodeEntities(s) {
  return s.replace(/&#(\d+);/g, (m, n) => String.fromCodePoint(Number(n)))
    .replace(/&#x([0-9a-f]+);/gi, (m, n) => String.fromCodePoint(parseInt(n, 16)))
    .replace(/&quot;/g, '"').replace(/&apos;|&#39;/g, "'").replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");
}

// The file's real name, from its own Drive page (og:title), not from the
// labels around the preview.
async function fileName(kind, id, authuser) {
  const au = authuser !== "" ? "?authuser=" + encodeURIComponent(authuser) : "";
  const url = kind === "gdoc"
    ? "https://docs.google.com/document/d/" + encodeURIComponent(id) + "/edit" + au
    : "https://drive.google.com/file/d/" + encodeURIComponent(id) + "/view" + au;
  try {
    const { html } = await peek(url, 15000);
    let m = html.match(/<meta[^>]+property="og:title"[^>]+content="([^"]*)"/i) ||
      html.match(/<meta[^>]+content="([^"]*)"[^>]+property="og:title"/i);
    let name = m ? decodeEntities(m[1]).trim() : "";
    if (!name) {
      m = html.match(/<title>([^<]*)<\/title>/i);
      name = m ? decodeEntities(m[1]).replace(/\s+-\s+Google (Drive|Docs)\s*$/i, "").trim() : "";
    }
    return UI_NAMES.test(name) ? "" : name;
  } catch (e) {
    return "";
  }
}

// Base64 of bytes, in chunks (a service worker has no FileReader limits to
// worry about, but String.fromCharCode cannot take millions of arguments).
function bytesToBase64(bytes) {
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) {
    bin += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000));
  }
  return btoa(bin);
}

// Any page image -> PNG on a white background. Firefox's background page has
// a document; Chrome, Edge and Brave run this in a service worker, which has
// OffscreenCanvas instead.
async function blobToPngDataUrl(blob) {
  const bmp = await createImageBitmap(blob);
  const w = bmp.width, h = bmp.height;
  const offscreen = typeof document === "undefined" || typeof document.createElement !== "function";
  const c = offscreen ? new OffscreenCanvas(w, h) : document.createElement("canvas");
  c.width = w;
  c.height = h;
  const g = c.getContext("2d");
  g.fillStyle = "#fff";
  g.fillRect(0, 0, w, h);
  g.drawImage(bmp, 0, 0);
  bmp.close();
  if (!offscreen) return { data: c.toDataURL("image/png"), w, h };
  const png = await c.convertToBlob({ type: "image/png" });
  return { data: "data:image/png;base64," + bytesToBase64(new Uint8Array(await png.arrayBuffer())), w, h };
}

// A sharper copy of page n: the viewer's own image link with a larger w=.
async function hiResPage(tabId, n, width) {
  const m = viewerImgs.get(tabId);
  const url = m && m.get(n);
  if (!url) return null;
  const u = new URL(url);
  if (!u.searchParams.has("w")) return null;
  u.searchParams.set("w", String(width));
  const r = await fetch(u.toString(), { credentials: "include" });
  if (!r.ok) return null;
  const type = r.headers.get("content-type") || "";
  if (!type.startsWith("image/")) return null;
  return blobToPngDataUrl(await r.blob());
}

async function imageUrlToPng(url) {
  const r = await fetch(url, { credentials: "include" });
  if (!r.ok) throw new Error("the page image could not be loaded");
  return blobToPngDataUrl(await r.blob());
}

async function bddmPost(path, body, ms = 60000) {
  const ctrl = new AbortController();
  const t = setTimeout(() => ctrl.abort(), ms);
  let r;
  try {
    r = await fetch(BDDM + path, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body || {}), signal: ctrl.signal,
    });
  } catch (e) {
    throw new Error("BDDM is not running. Start BDDM and try again.");
  } finally {
    clearTimeout(t);
  }
  let d = {};
  try { d = await r.json(); } catch (e) { /* not JSON */ }
  if (!r.ok || d.ok === false) throw new Error(d.error || "BDDM answered with an error (" + r.status + ").");
  return d;
}

const PDF_MESSAGES = {
  pdfCheck: (m) => checkDownload(m.kind, m.id, m.authuser || "").then((state) => ({ state })),
  pdfName: (m) => fileName(m.kind, m.id, m.authuser || "").then((name) => ({ name })),
  pdfHiRes: (m, sender) => hiResPage(sender.tab ? sender.tab.id : -1, m.page, m.width).then((r) => r || {}),
  pdfFetchImage: (m) => imageUrlToPng(m.url),
  pdfSaveDir: (m) => bddmPost("/ext/pdf/savedir", { name: m.name, format: m.format }),
  pdfPickFolder: (m) => bddmPost("/ext/pdf/pickfolder", { start: m.start || "" }, 600000),
  pdfStart: (m) => bddmPost("/ext/pdf/start", { name: m.name, url: m.url, pages: m.pages }),
  pdfPage: (m) => bddmPost("/ext/pdf/" + encodeURIComponent(m.id) + "/page", { n: m.n, data: m.data }),
  pdfFinish: (m) => bddmPost("/ext/pdf/" + encodeURIComponent(m.id) + "/finish", m.options || {}, 300000),
  pdfCancel: (m) => bddmPost("/ext/pdf/" + encodeURIComponent(m.id) + "/cancel", {}),
  pdfShowBDDM: () => bddmPost("/ext/show", {}),
  // A preview frame asks the Drive page around it which file was opened.
  pdfWhichFile: async (m, sender) => {
    if (!sender.tab) return { id: "" };
    const r = await api.tabs.sendMessage(sender.tab.id, { type: "pdfOpenedFile" }, { frameId: 0 }).catch(() => null);
    return { id: (r && r.id) || "" };
  },
};

api.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  if (!msg || sender.id !== api.runtime.id || !PDF_MESSAGES[msg.type]) return false;
  PDF_MESSAGES[msg.type](msg, sender).then(
    (r) => sendResponse({ ok: true, ...r }),
    (e) => sendResponse({ ok: false, error: e && e.message ? e.message : String(e) }));
  return true;
});
