// BDDM Download Catcher - background script (one codebase for Chrome, Edge,
// Brave and Firefox; browser differences are handled with feature checks).
//
// What it does:
//  1. Catches downloads that start in Chrome and sends them to BDDM
//     (http://127.0.0.1:9614/add) with the page address and cookies.
//  2. Files smaller than 10 KB stay in Chrome.
//  3. If BDDM is not running, Chrome keeps the download. If BDDM later fails
//     a download we sent, Chrome downloads it instead.
//  4. If BDDM already downloaded the file, asks "Download again?".
//  5. Adds "Download with BDDM" to the right-click menu.

// Chrome/Edge/Brave run this as a service worker and load the shim here;
// Firefox loads compat.js first from the manifest.
if (typeof api === "undefined" && typeof importScripts === "function") importScripts("compat.js");

const BDDM = "http://127.0.0.1:9614";
const MIN_SIZE = 10 * 1024;            // smaller files stay in the browser
const SEND_TIMEOUT = 3000;             // ms to wait for BDDM to answer
const PROBE_TIMEOUT = 4000;            // ms for the quick size check
const DECIDE_TIMEOUT = 9000;           // never hold a browser download longer
const HANDED_BACK_TTL = 24 * 3600e3;   // do not capture a handed-back link again for a day
const TRACK_TTL = 7 * 24 * 3600e3;     // stop watching a job after a week
const SKIP_WINDOW = 8000;              // Alt+click skip lasts this long
const POLL_ALARM = "bddm-poll";

const DEFAULTS = { enabled: true, excluded: [] };

// ---------------------------------------------------------------- helpers

async function getSettings() {
  const s = await api.storage.local.get(DEFAULTS);
  return { enabled: s.enabled !== false, excluded: Array.isArray(s.excluded) ? s.excluded : [] };
}

function hostOf(u) {
  try { return new URL(u).hostname.toLowerCase(); } catch (e) { return ""; }
}

function isExcluded(host, excluded) {
  if (!host) return false;
  return excluded.some((ex) => {
    ex = String(ex || "").trim().toLowerCase().replace(/^\*\./, "");
    return ex && (host === ex || host.endsWith("." + ex));
  });
}

function isWebUrl(u) {
  return /^https?:\/\//i.test(u || "");
}

function baseName(p) {
  if (!p) return "";
  const parts = String(p).split(/[\\/]/);
  return parts[parts.length - 1] || "";
}

async function fetchTimeout(url, opts, ms) {
  const ctrl = new AbortController();
  const t = setTimeout(() => ctrl.abort(), ms);
  try {
    return await fetch(url, { ...opts, signal: ctrl.signal });
  } finally {
    clearTimeout(t);
  }
}

// Cookies for the link, joined into one Cookie header.
// Firefox: also works with First-Party Isolation (firstPartyDomain: null),
// Total Cookie Protection (partitioned cookies of the page's site) and
// containers (the download's cookieStoreId).
async function cookieHeader(url, opts = {}) {
  const jar = new Map();
  const add = (list) => { for (const c of list || []) if (!jar.has(c.name)) jar.set(c.name, c.value); };
  const base = { url };
  if (opts.storeId) base.storeId = opts.storeId;
  try {
    if (IS_FIREFOX) {
      add(await api.cookies.getAll({ ...base, firstPartyDomain: null }));
      const top = siteOf(opts.referer);
      if (top && top !== siteOf(url)) {
        try { add(await api.cookies.getAll({ ...base, firstPartyDomain: null, partitionKey: { topLevelSite: top } })); } catch (e) { /* old Firefox */ }
      }
    } else {
      add(await api.cookies.getAll(base));
      // Chrome 119+: cookies a site keeps per top-level site (CHIPS).
      const top = siteOf(opts.referer);
      if (top) {
        try { add(await api.cookies.getAll({ ...base, partitionKey: { topLevelSite: top } })); } catch (e) { /* older Chrome */ }
      }
    }
  } catch (e) {
    try { add(await api.cookies.getAll({ url })); } catch (e2) { /* no cookies */ }
  }
  return [...jar].map(([k, v]) => k + "=" + v).join("; ");
}

// ---------------------------------------------------------------- browser details (1.4.1)
//
// BDDM's requests should look like the browser's own: the same User-Agent,
// languages, Referer, Origin, player headers and cookies. The headers the
// browser sends are remembered here for a few minutes, in memory only
// (never stored), and only given to BDDM on this computer.

const BROWSER_UA = (typeof navigator !== "undefined" && navigator.userAgent) || "";
function browserLanguages() {
  const langs = (typeof navigator !== "undefined" && (navigator.languages || [navigator.language])) || [];
  return langs.filter(Boolean).slice(0, 6)
    .map((l, i) => (i === 0 ? l : l + ";q=" + Math.max(0.1, 1 - i * 0.1).toFixed(1))).join(",");
}
const SEEN_MAX = 400;
const SEEN_TTL = 15 * 60e3;
const seenRequests = new Map(); // url -> { headers, at }
const frameUrls = new Map();    // "tabId:frameId" -> address of the frame

function ownRequest(d) {
  const from = d.initiator || d.originUrl || "";
  return /^(chrome|moz|ms-browser|safari-web)-extension:\/\//.test(from);
}

function rememberRequest(d) {
  if (!isWebUrl(d.url) || ownRequest(d)) return;
  const headers = {};
  for (const h of d.requestHeaders || []) if (h && h.name && typeof h.value === "string") headers[h.name] = h.value;
  seenRequests.delete(d.url);
  seenRequests.set(d.url, { headers, at: Date.now() });
  while (seenRequests.size > SEEN_MAX) seenRequests.delete(seenRequests.keys().next().value);
  if ((d.type === "main_frame" || d.type === "sub_frame") && d.tabId >= 0) {
    frameUrls.delete(d.tabId + ":" + d.frameId);
    frameUrls.set(d.tabId + ":" + d.frameId, d.url);
    while (frameUrls.size > 500) frameUrls.delete(frameUrls.keys().next().value);
  }
}

if (api.webRequest && api.webRequest.onBeforeSendHeaders) {
  const opts = ["requestHeaders"];
  // Chrome hides Referer, Origin and Cookie without "extraHeaders".
  if (!IS_FIREFOX) opts.push("extraHeaders");
  try {
    api.webRequest.onBeforeSendHeaders.addListener(rememberRequest, { urls: ["http://*/*", "https://*/*"] }, opts);
  } catch (e) {
    api.webRequest.onBeforeSendHeaders.addListener(rememberRequest, { urls: ["http://*/*", "https://*/*"] }, ["requestHeaders"]);
  }
}

// The headers the browser sent for the newest of these links.
function seenHeaders(urls) {
  let best = null;
  for (const u of urls) {
    const s = u && seenRequests.get(u);
    if (s && Date.now() - s.at < SEEN_TTL && (!best || s.at > best.at)) best = s;
  }
  return best ? { ...best.headers } : {};
}

// The address of the page or frame that made a request (full address, not
// just its origin as Chrome's "initiator").
function frameAddress(tabId, frameId) {
  return frameUrls.get(tabId + ":" + frameId) || "";
}

// Every cookie (with its domain) of these links and their sites, so BDDM
// can give each host -- also a CDN a link redirects to -- its own cookies.
async function cookieList(urls, opts = {}) {
  const out = new Map();
  const add = (list) => {
    for (const c of list || []) {
      const key = c.domain + "|" + c.path + "|" + c.name;
      if (!out.has(key) && out.size < 400) {
        out.set(key, { name: c.name, value: c.value, domain: c.domain, path: c.path || "/", hostOnly: !!c.hostOnly, secure: !!c.secure });
      }
    }
  };
  const base = {};
  if (opts.storeId) base.storeId = opts.storeId;
  const top = siteOf(opts.referer);
  for (const u of [...new Set(urls.filter(isWebUrl))]) {
    const q = { ...base, url: u };
    try {
      if (IS_FIREFOX) {
        add(await api.cookies.getAll({ ...q, firstPartyDomain: null }));
        if (top) { try { add(await api.cookies.getAll({ ...q, firstPartyDomain: null, partitionKey: { topLevelSite: top } })); } catch (e) { /* old Firefox */ } }
      } else {
        add(await api.cookies.getAll(q));
        if (top) { try { add(await api.cookies.getAll({ ...q, partitionKey: { topLevelSite: top } })); } catch (e) { /* older Chrome */ } }
      }
    } catch (e) { /* no cookies */ }
  }
  return [...out.values()];
}

// Adds the browser's details to a request for BDDM.
//   urls: the link (and where it redirected to); referer: the page.
async function withBrowserDetails(req, urls, opts = {}) {
  const headers = seenHeaders(urls);
  // The Cookie header the browser really sent wins over the one we built.
  const sentCookie = headers.Cookie || headers.cookie || "";
  delete headers.Cookie; delete headers.cookie;
  if (sentCookie) {
    const have = new Set(sentCookie.split(";").map((p) => p.trim().split("=")[0]));
    const more = (req.cookie || "").split(";").map((p) => p.trim()).filter((p) => p && !have.has(p.split("=")[0]));
    req.cookie = [sentCookie, ...more].join("; ");
  }
  // A referer that is only an origin ("https://site.com/"): the browser's
  // full one is better.
  const sentRef = headers.Referer || headers.referer || "";
  if (isWebUrl(sentRef) && (!req.referer || /^https?:\/\/[^/]+\/?$/.test(req.referer))) req.referer = sentRef;
  req.userAgent = BROWSER_UA;
  req.acceptLanguage = browserLanguages();
  if (Object.keys(headers).length) req.headers = headers;
  const cookies = await cookieList([...urls, req.referer], { referer: opts.referer || req.referer, storeId: opts.storeId });
  if (cookies.length) req.cookies = cookies;
  return req;
}

// Google Drive links: BDDM later talks to drive.google.com, docs.google.com
// and Drive's download server (drive.usercontent.google.com) with the same
// cookies, so send the cookies of all three (some are set only for one host).
const DRIVE_COOKIE_URLS = ["https://drive.google.com/", "https://drive.usercontent.google.com/download", "https://docs.google.com/"];
async function linkCookieHeader(url, opts = {}) {
  const own = await cookieHeader(url, opts);
  if (!/^https:\/\/(drive|docs|drive\.usercontent)\.google\.com\//.test(url)) return own;
  const jar = new Map();
  const add = (h) => { for (const part of h.split("; ")) { const i = part.indexOf("="); if (i > 0 && !jar.has(part.slice(0, i))) jar.set(part.slice(0, i), part.slice(i + 1)); } };
  add(own);
  for (const u of DRIVE_COOKIE_URLS) if (!url.startsWith(u)) add(await cookieHeader(u, opts));
  return [...jar].map(([k, v]) => k + "=" + v).join("; ");
}

// "https://example.com" for https://www.dl.example.com/... (good enough for
// Firefox partition keys; it uses scheme + registrable domain).
function siteOf(u) {
  try {
    const x = new URL(u);
    if (!/^https?:$/.test(x.protocol)) return "";
    const parts = x.hostname.split(".");
    const isIP = /^[\d.]+$/.test(x.hostname) || x.hostname.includes(":");
    const host = isIP || parts.length <= 2 ? x.hostname : parts.slice(-2).join(".");
    return x.protocol + "//" + host;
  } catch (e) {
    return "";
  }
}

async function activeTabUrl() {
  try {
    const [tab] = await api.tabs.query({ active: true, lastFocusedWindow: true });
    return tab && isWebUrl(tab.url) ? tab.url : "";
  } catch (e) {
    return "";
  }
}

// The title of the page a download came from (1.4.2): BDDM names a file
// after it when the link has no useful name ("stream.mp4", "download").
async function pageTitleFor(page, tab) {
  if (tab && tab.title && (!page || tab.url === page)) return tab.title;
  if (!isWebUrl(page)) return "";
  const noHash = (u) => String(u || "").split("#")[0];
  try {
    const tabs = await api.tabs.query({});
    const hit = tabs.find((t) => t.url === page) || tabs.find((t) => noHash(t.url) === noHash(page));
    if (hit && hit.title) return hit.title;
  } catch (e) { /* no tabs */ }
  return tab && tab.title && hostOf(tab.url) === hostOf(page) ? tab.title : "";
}

// Send Anywhere (1.4.2): its download links work once (the transfer ends
// when they are read), so they are always left to the browser.
const BROWSER_ONLY_HOSTS = ["send-anywhere.com", "sendanywhe.re", "send-anywhere.net"];
function isBrowserOnly(...links) {
  return links.some((u) => isExcluded(hostOf(u), BROWSER_ONLY_HOSTS));
}

function notify(title, message) {
  api.notifications.create({
    type: "basic",
    iconUrl: "icons/icon128.png",
    title,
    message: message || "",
  }).catch(() => {});
}

// The global badge shows OFF when catching is switched off; tabs with videos
// show their own count on top of it (see videos.js).
function setBadge(enabled) {
  api.action.setBadgeText({ text: enabled ? "" : "OFF" }).catch(() => {});
  api.action.setBadgeBackgroundColor({ color: "#6b7280" }).catch(() => {});
}

function isHtml(r) {
  return /^text\/html/i.test(r.headers.get("content-type") || "");
}

// Google Drive / Docs downloads (Drive's own "Download" menu and button, and
// its zipped multi-file downloads). Their size cannot be checked with a quick
// request, so they always go to BDDM (BDDM itself works out the real link).
function isGoogleDriveDownload(u) {
  let x;
  try { x = new URL(u); } catch (e) { return false; }
  const h = x.hostname.toLowerCase();
  if (h === "drive.usercontent.google.com") return true;
  if (h.endsWith(".googleusercontent.com") && /(^|[.-])(drive|docs)/.test(h)) return true; // doc-..-drive-data-export, doc-..-docs
  if (h === "drive.google.com" && /^\/(u\/\d+\/)?uc$/.test(x.pathname)) return true;
  if (h === "docs.google.com" && /\/export$/.test(x.pathname)) return true;
  return false;
}

// Quick size check for downloads whose size Chrome does not know yet.
// Returns the size in bytes, or -1 when it is still unknown.
async function probeSize(url) {
  try {
    const r = await fetchTimeout(url, { method: "HEAD", credentials: "include", redirect: "follow" }, PROBE_TIMEOUT);
    const len = parseInt(r.headers.get("content-length") || "", 10);
    // Some servers (Google Drive) answer HEAD with "Content-Length: 0" or a
    // web page: that is not the file's size, so ask with a ranged GET.
    if (r.ok && len > 0 && !isHtml(r)) return len;
  } catch (e) { /* try the ranged GET */ }
  const ctrl = new AbortController();
  const t = setTimeout(() => ctrl.abort(), PROBE_TIMEOUT);
  try {
    const r = await fetch(url, { headers: { Range: "bytes=0-0" }, credentials: "include", signal: ctrl.signal });
    if (isHtml(r)) return -1; // a web page, not the file: size unknown
    const cr = r.headers.get("content-range") || "";
    const m = cr.match(/\/(\d+)\s*$/);
    if (r.status === 206 && m) return parseInt(m[1], 10);
    const len = parseInt(r.headers.get("content-length") || "", 10);
    if (r.status === 200 && len >= 0) return len;
  } catch (e) {
    /* unknown */
  } finally {
    clearTimeout(t);
    ctrl.abort(); // do not download the body
  }
  return -1;
}

// Sends one link to BDDM. Returns:
//   {kind:"ok", job} | {kind:"duplicate", name} | {kind:"offline"} | {kind:"error", error}
async function sendToBDDM(req) {
  let r;
  try {
    r = await fetchTimeout(BDDM + "/add", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(req),
    }, SEND_TIMEOUT);
  } catch (e) {
    return { kind: "offline" };
  }
  let data = {};
  try { data = await r.json(); } catch (e) { /* not JSON */ }
  if (r.status === 200 && data.ok && data.job && data.refreshed) {
    // The link of an unfinished download expired; this is its new link.
    notify("Download continues in BDDM", (data.job.name || req.filename || "") + ": got a new link, continuing where it stopped.");
  }
  if (r.status === 200 && data.ok && data.job) return { kind: "ok", job: data.job };
  // A Google Drive folder or document: BDDM opened a window to choose in.
  if (r.status === 200 && data.ok) return { kind: "window", message: data.message || "Choose what to download in the BDDM window." };
  if (r.status === 409) return { kind: "duplicate", name: data.name || req.filename || "" };
  if (r.status === 403) return { kind: "offline" }; // something else is on the port
  return { kind: "error", error: data.error || ("BDDM answered " + r.status) };
}

async function pingBDDM() {
  try {
    const r = await fetchTimeout(BDDM + "/ext/ping", {}, 2000);
    const d = await r.json();
    return d && d.app === "BDDM";
  } catch (e) {
    return false;
  }
}

// ---------------------------------------------------------------- tracking

async function getTracked() {
  const { tracked } = await api.storage.local.get({ tracked: {} });
  return tracked || {};
}

async function track(job, info) {
  const tracked = await getTracked();
  tracked[job.id] = { ...info, name: job.name || info.filename || "", added: Date.now() };
  await api.storage.local.set({ tracked });
  ensurePollAlarm();
}

function ensurePollAlarm() {
  api.alarms.get(POLL_ALARM).then((a) => {
    if (!a) api.alarms.create(POLL_ALARM, { periodInMinutes: 0.5 });
  }).catch(() => {});
}

async function markHandedBack(urls) {
  const { handedBack } = await api.storage.local.get({ handedBack: {} });
  const now = Date.now();
  for (const k of Object.keys(handedBack)) {
    if (now - handedBack[k] > HANDED_BACK_TTL) delete handedBack[k];
  }
  for (const u of urls) if (u) handedBack[u] = now;
  await api.storage.local.set({ handedBack });
}

async function wasHandedBack(urls) {
  const { handedBack } = await api.storage.local.get({ handedBack: {} });
  const now = Date.now();
  return urls.some((u) => u && handedBack[u] && now - handedBack[u] < HANDED_BACK_TTL);
}

// Gives a download that BDDM could not finish back to Chrome.
async function handBack(jobId, info, reason) {
  const url = info.origUrl || info.url;
  await markHandedBack([info.url, info.origUrl]);
  try {
    const id = await api.downloads.download({ url, conflictAction: "uniquify" });
    if (typeof id === "number") ownDownloads.add(id);
    notify("BDDM could not download this file",
      (info.name || baseName(url)) + (reason ? " (" + reason + ")" : "") + ". Your browser is downloading it instead.");
  } catch (e) {
    notify("Download failed", (info.name || baseName(url)) + ": " + (reason || e.message));
  }
  try {
    await fetchTimeout(BDDM + "/ext/jobs/" + encodeURIComponent(jobId) + "/handedback", { method: "POST" }, SEND_TIMEOUT);
  } catch (e) { /* BDDM closed; fine */ }
}

let polling = false;
async function pollJobs() {
  if (polling) return;
  polling = true;
  try {
    const tracked = await getTracked();
    const ids = Object.keys(tracked);
    if (ids.length === 0) {
      await api.alarms.clear(POLL_ALARM);
      return;
    }
    let data;
    try {
      const r = await fetchTimeout(BDDM + "/ext/status?ids=" + encodeURIComponent(ids.join(",")), {}, SEND_TIMEOUT);
      data = await r.json();
    } catch (e) {
      return; // BDDM is closed; its downloads are paused, check again later
    }
    const now = Date.now();
    const done = new Set(data.missing || []);
    for (const j of data.jobs || []) {
      const info = tracked[j.id];
      if (!info) continue;
      if (j.status === "Error" && /view only/i.test(j.error || "")) {
        // The owner turned off downloading: the browser cannot get it either.
        notify("Download not possible", (j.name || info.name || "") + ": " + j.error + " Open it in Firefox and use Save as PDF from the BDDM extension.");
        done.add(j.id);
      } else if (j.status === "Error" && /link has expired/i.test(j.error || "")) {
        // Giving the old link back to the browser would fail too.
        notify("Download link expired", (j.name || info.name || "") + ": click the download again on the website. BDDM will continue where it stopped.");
        done.add(j.id);
      } else if (j.status === "Error") {
        await handBack(j.id, { ...info, name: j.name || info.name }, j.error);
        done.add(j.id);
      } else if (j.status === "Done" || j.removed) {
        done.add(j.id); // finished, or the user deleted it in BDDM
      } else if (now - info.added > TRACK_TTL) {
        done.add(j.id);
      }
    }
    if (done.size) {
      const latest = await getTracked();
      for (const id of done) delete latest[id];
      await api.storage.local.set({ tracked: latest });
    }
  } finally {
    polling = false;
  }
}

// ---------------------------------------------------------------- duplicates

async function askDuplicate(req, name, info) {
  const key = "dup_" + Date.now() + "_" + Math.random().toString(36).slice(2, 8);
  await api.storage.session.set({ [key]: { req, name, info } });
  await api.windows.create({
    url: "dup.html?k=" + encodeURIComponent(key),
    type: "popup",
    width: 460,
    height: 250,
    focused: true,
  });
}

async function answerDuplicate(key, again) {
  const got = await api.storage.session.get(key);
  const pending = got[key];
  await api.storage.session.remove(key);
  if (!pending || !again) return { ok: true };
  const res = await sendToBDDM({ ...pending.req, force: true });
  if (res.kind === "ok") {
    await track(res.job, pending.info);
    return { ok: true };
  }
  if (res.kind === "window") return { ok: true };
  if (res.kind === "offline") return { ok: false, error: "BDDM is not running. Start BDDM and try again." };
  return { ok: false, error: res.error || "BDDM could not add the download." };
}

// ---------------------------------------------------------------- capture

const ownDownloads = new Set(); // downloads this extension started (hand-backs)
let skipUntil = 0; // Alt+click: let the next download go to the browser

async function shouldSkipByAlt() {
  if (Date.now() < skipUntil) { skipUntil = 0; return true; }
  const { skipUntil: s } = await api.storage.session.get({ skipUntil: 0 });
  if (Date.now() < s) {
    await api.storage.session.set({ skipUntil: 0 });
    return true;
  }
  return false;
}

// A Google Drive zip of a folder / several files (Drive's own "Download").
function isDriveZipDownload(item) {
  const name = baseName(item.filename || "").toLowerCase();
  const urls = [item.url, item.finalUrl].filter(Boolean);
  const fromDrive = /^https:\/\/(drive|docs)\.google\.com\//.test(item.referrer || "") ||
    urls.some((u) => /^blob:https:\/\/drive\.google\.com\//.test(u));
  if (urls.some((u) => /^https:\/\/[^/]*drive-data-export\.googleusercontent\.com\//.test(u))) return true;
  if (!name.endsWith(".zip")) return false;
  if (urls.some((u) => /^blob:https:\/\/drive\.google\.com\//.test(u))) return true;
  return fromDrive && urls.some((u) => isGoogleDriveDownload(u));
}

let notedOnce = {};
function noteOnce(key, title, message) {
  if (notedOnce[key] && Date.now() - notedOnce[key] < 10 * 60e3) return;
  notedOnce[key] = Date.now();
  notify(title, message);
}

// Asks the Google Drive tabs which folder / files the user right-clicked
// (the zip is being made for them) and sends those to BDDM. Returns true
// when BDDM took them (the browser's zip download is then cancelled).
async function takeDriveZip(item) {
  let tabs = [];
  try { tabs = await api.tabs.query({ url: "https://drive.google.com/*" }); } catch (e) { return false; }
  if (item.tabId >= 0) tabs.sort((a, b) => (b.id === item.tabId) - (a.id === item.tabId));
  else tabs.sort((a, b) => (b.active - a.active) || ((b.lastAccessed || 0) - (a.lastAccessed || 0)));
  let best = null;
  for (const tab of tabs) {
    let got = null;
    try { got = await api.tabs.sendMessage(tab.id, { type: "driveTarget", maxAge: 15 * 60e3, preferSelection: true }, { frameId: 0 }); } catch (e) { continue; }
    if (got && Array.isArray(got.items) && got.items.length && (!best || (got.at || 0) > (best.got.at || 0))) best = { tab, got };
  }
  if (!best) return false;
  const page = best.got.page || best.tab.url || "";
  const authuser = driveAuthUser(page);
  const urls = [...new Set(await Promise.all(best.got.items.map((it) => resolveDriveId(it, authuser))))];
  if (!urls.length) return false;
  const results = [];
  for (const u of urls) results.push(await sendLinkFromMenu(u, page, best.tab));
  if (!results.some((r) => r.kind === "ok" || r.kind === "window" || r.kind === "duplicate")) return false;
  await stopBrowserDownload(item);
  const what = urls.length > 1 ? "these " + urls.length + " items" : (/\/folders\//.test(urls[0]) ? "this folder" : "this file");
  notify("Sent to BDDM", "BDDM downloads " + what + " itself instead of Google's zip" +
    (/\/folders\//.test(urls.join(" ")) ? ". Choose the files in the BDDM window." : "."));
  return true;
}

async function stopBrowserDownload(item) {
  try { await api.downloads.cancel(item.id); } catch (e) { /* already gone */ }
  try {
    const [now] = await api.downloads.search({ id: item.id });
    if (now && now.state === "complete" && api.downloads.removeFile) await api.downloads.removeFile(item.id);
  } catch (e) { /* ignore */ }
  try { await api.downloads.erase({ id: item.id }); } catch (e) { /* ignore */ }
}

// Decides about one browser download. Returns true when BDDM took it (the
// browser download was cancelled), false when Chrome should keep it.
async function capture(item) {
  if (item.byExtensionId === api.runtime.id || ownDownloads.has(item.id)) return false; // we started it (hand-back)
  if (item.incognito) return false;
  if (item.state && item.state !== "in_progress") return false;
  let url = item.finalUrl || item.url;

  // Google Drive's own "Download" of a folder or several files: Drive makes a
  // zip (sometimes inside the page, as a blob: link). BDDM downloads the
  // folder itself instead, file by file (resumable, one row in BDDM).
  const zipCase = isDriveZipDownload(item);
  if (zipCase) {
    const settings0 = await getSettings();
    if (settings0.enabled && !(await shouldSkipByAlt()) && !(await wasHandedBack([item.url, url])) &&
        !isExcluded("drive.google.com", settings0.excluded)) {
      const took = await takeDriveZip(item);
      if (took) return true;
    }
  }

  if (!isWebUrl(url) || !isWebUrl(item.url)) {
    // blob:, data:, file: ... stay in the browser (they exist only inside the
    // page, so BDDM cannot download them).
    if (zipCase) noteOnce("blobzip", "BDDM could not take this Google Drive zip",
      "Drive built the zip inside the page, so only the browser can save it. To use BDDM, hold Shift, right-click the folder in Drive and choose \"Download with BDDM\".");
    return false;
  }

  const settings = await getSettings();
  if (!settings.enabled) return false;
  if (await shouldSkipByAlt()) return false;
  if (await wasHandedBack([item.url, url])) return false;

  const referer = isWebUrl(item.referrer) ? item.referrer : await activeTabUrl();
  if (isBrowserOnly(url, item.url, referer, item.finalUrl)) return false;
  if (isExcluded(hostOf(url), settings.excluded) || isExcluded(hostOf(item.url), settings.excluded) ||
      isExcluded(hostOf(referer), settings.excluded)) return false;

  let size = item.totalBytes > 0 ? item.totalBytes : (item.fileSize > 0 ? item.fileSize : -1);
  const drive = isGoogleDriveDownload(url) || isGoogleDriveDownload(item.url);
  if (size < 0 && !drive) size = await probeSize(url);
  if (size >= 0 && size < MIN_SIZE) return false;

  const filename = baseName(item.filename);
  // A Drive download started from Drive opened as a second Google account
  // (/u/1/): keep the account number, or Google would answer for account 0.
  if (drive && /^(drive\.usercontent|drive|docs)\.google\.com$/.test(hostOf(url)) &&
      !driveAuthUser(url) && driveAuthUser(referer)) {
    url = withAuthUser(url, driveAuthUser(referer));
  }
  const req = { url, referer, cookie: await linkCookieHeader(url, { referer, storeId: item.cookieStoreId }), filename,
    title: await pageTitleFor(referer) };
  await withBrowserDetails(req, [url, item.url], { referer, storeId: item.cookieStoreId });
  const info = { url, origUrl: item.url, filename };
  const res = await sendToBDDM(req);
  if (res.kind === "offline" || res.kind === "error") return false;

  // BDDM has it (or will ask): stop the browser copy and hide it from the list.
  try { await api.downloads.cancel(item.id); } catch (e) { /* already gone */ }
  try {
    // Firefox only: a very fast download may already be finished; BDDM has
    // its own copy, so remove the browser's one.
    const [now] = await api.downloads.search({ id: item.id });
    if (now && now.state === "complete" && api.downloads.removeFile) await api.downloads.removeFile(item.id);
  } catch (e) { /* ignore */ }
  try { await api.downloads.erase({ id: item.id }); } catch (e) { /* ignore */ }

  if (res.kind === "ok") {
    await track(res.job, info);
    notify("Sent to BDDM", res.job.name || filename || baseName(url));
  } else if (res.kind === "duplicate") {
    await askDuplicate(req, res.name || filename, info);
  } else if (res.kind === "window") {
    notify("Sent to BDDM", res.message);
  }
  return true;
}

if (api.downloads.onDeterminingFilename) {
  // Chrome, Edge, Brave: onDeterminingFilename runs once the browser knows the
  // file name and size, before any "Save as" box. It waits for suggest().
  api.downloads.onDeterminingFilename.addListener((item, suggest) => {
    let settled = false;
    const letBrowserHaveIt = () => {
      if (settled) return;
      settled = true;
      try { suggest(); } catch (e) { /* download already gone */ }
    };
    const timer = setTimeout(letBrowserHaveIt, DECIDE_TIMEOUT);
    capture(item).then((taken) => {
      clearTimeout(timer);
      if (taken) settled = true; else letBrowserHaveIt();
    }).catch(() => {
      clearTimeout(timer);
      letBrowserHaveIt();
    });
    return true; // we answer later
  });
} else {
  // Firefox has no onDeterminingFilename: the download has already started
  // when onCreated fires, so it keeps running while we ask BDDM, and is
  // cancelled and removed from the list only if BDDM takes it.
  api.downloads.onCreated.addListener((item) => {
    capture(item).catch(() => {});
  });
}

// ---------------------------------------------------------------- right-click menu

function createMenus() {
  api.contextMenus.removeAll(() => {
    api.contextMenus.create({ id: "bddm-link", title: "Download with BDDM", contexts: ["link"] });
    api.contextMenus.create({ id: "bddm-media", title: "Download with BDDM", contexts: ["image", "video", "audio"] });
    // Google Drive / Docs pages: send the right-clicked (or open) file,
    // folder or document.
    api.contextMenus.create({ id: "bddm-drive-page", title: "Download with BDDM", contexts: ["page", "selection"],
      documentUrlPatterns: ["https://drive.google.com/*", "https://docs.google.com/*", "https://drive.usercontent.google.com/*"] });
    if (typeof createVideoMenu === "function") createVideoMenu();
  });
}

// ---------------------------------------------------------------- Google Drive pages

const DRIVE_ID = "[A-Za-z0-9_-]{10,}";
const DRIVE_FILE_RE = new RegExp("^/file/(?:u/\\d+/)?d/(" + DRIVE_ID + ")");
const DRIVE_FOLDER_RE = new RegExp("^/drive/(?:u/\\d+/)?(?:mobile/)?folders/(" + DRIVE_ID + ")");
const DOCS_RE = new RegExp("^/(document|spreadsheets|presentation)/(?:u/\\d+/)?d/(" + DRIVE_ID + ")");

// The Google account number of a Drive page (/u/1/ or ?authuser=1), or "".
function driveAuthUser(u) {
  try {
    const x = new URL(u);
    const q = x.searchParams.get("authuser");
    if (q) return q;
    const m = x.pathname.match(/\/u\/(\d+)(?:\/|$)/);
    return m ? m[1] : "";
  } catch (e) {
    return "";
  }
}

function withAuthUser(u, authuser) {
  return authuser ? u + (u.includes("?") ? "&" : "?") + "authuser=" + encodeURIComponent(authuser) : u;
}

// The clean link of a Drive file, folder or Google document, or "" when u is
// not one (My Drive, Storage, Recent ... pages).
function canonicalDriveUrl(u, authuser) {
  let x;
  try { x = new URL(u); } catch (e) { return ""; }
  let m;
  if (x.hostname === "drive.google.com") {
    if ((m = x.pathname.match(DRIVE_FILE_RE))) return withAuthUser("https://drive.google.com/file/d/" + m[1] + "/view", authuser);
    if ((m = x.pathname.match(DRIVE_FOLDER_RE))) return withAuthUser("https://drive.google.com/drive/folders/" + m[1], authuser);
  } else if (x.hostname === "docs.google.com") {
    if ((m = x.pathname.match(DOCS_RE))) return withAuthUser("https://docs.google.com/" + m[1] + "/d/" + m[2] + "/edit", authuser);
  }
  return "";
}

// Asks Drive what an id is (file, folder or Google document): drive.google.com
// /open?id= redirects to the right page. Falls back to the page's hint.
async function resolveDriveId(item, authuser) {
  const guess = withAuthUser(item.folder
    ? "https://drive.google.com/drive/folders/" + item.id
    : "https://drive.google.com/file/d/" + item.id + "/view", authuser);
  const ctrl = new AbortController();
  const t = setTimeout(() => ctrl.abort(), PROBE_TIMEOUT);
  try {
    const r = await fetch(withAuthUser("https://drive.google.com/open?id=" + encodeURIComponent(item.id), authuser),
      { credentials: "include", redirect: "follow", signal: ctrl.signal });
    return canonicalDriveUrl(r.url, authuser) || guess;
  } catch (e) {
    return guess;
  } finally {
    clearTimeout(t);
    ctrl.abort(); // only the address was needed, not the page
  }
}

// Works out what "Download with BDDM" on a Google Drive page means.
// Returns { urls: [...] } or { error: "..." }.
async function driveMenuTargets(info, tab) {
  const page = info.pageUrl || (tab && tab.url) || "";
  if (hostOf(page) !== "drive.google.com") {
    // Google Docs / Sheets / Slides, or a drive.usercontent page: the page itself.
    return { urls: [page] };
  }
  const authuser = driveAuthUser(page);
  let got = null;
  try {
    got = await api.tabs.sendMessage(tab.id, { type: "driveTarget" }, { frameId: 0 });
  } catch (e) { /* the page was open before BDDM was installed or updated */ }
  const items = (got && Array.isArray(got.items)) ? got.items : [];
  const pageLink = canonicalDriveUrl(page, authuser);
  const isFilePage = /\/file\/(?:u\/\d+\/)?d\//.test(new URL(page).pathname);
  let pick = [];
  if (items.length && got.from === "click") pick = items;
  else if (isFilePage) return { urls: [pageLink] };
  else if (items.length) pick = items; // the selected file(s), e.g. while the preview is open
  else if (pageLink) return { urls: [pageLink] }; // a folder page: the whole folder
  if (!pick.length) {
    return { error: got
      ? "BDDM could not tell which file you right-clicked. In the Drive list, hold Shift and right-click the file itself, or open the file and try again."
      : "Reload this Google Drive page (press F5) so BDDM can see which file you right-click, then try again." };
  }
  const urls = await Promise.all(pick.map((it) => resolveDriveId(it, authuser)));
  return { urls: [...new Set(urls)] };
}

async function sendLinkFromMenu(url, referer, tab) {
  const req = { url, referer: isWebUrl(referer) ? referer : "",
    cookie: await linkCookieHeader(url, { referer, storeId: tab && tab.cookieStoreId }), filename: "",
    title: await pageTitleFor(referer, tab) };
  await withBrowserDetails(req, [url], { referer, storeId: tab && tab.cookieStoreId });
  const track_info = { url, origUrl: url, filename: "" };
  const res = await sendToBDDM(req);
  if (res.kind === "ok") await track(res.job, track_info);
  else if (res.kind === "duplicate") await askDuplicate(req, res.name, track_info);
  return res;
}

api.contextMenus.onClicked.addListener(async (info, tab) => {
  if (info.menuItemId === "bddm-video-page") return; // handled in videos.js
  let urls;
  if (info.menuItemId === "bddm-drive-page") {
    const t = await driveMenuTargets(info, tab);
    if (t.error) {
      notify("BDDM", t.error);
      return;
    }
    urls = t.urls;
  } else {
    urls = [info.linkUrl || info.srcUrl || ""];
  }
  urls = urls.filter(isWebUrl);
  if (!urls.length) {
    notify("BDDM", "This link cannot be sent to BDDM. Only http:// and https:// links work.");
    return;
  }
  const referer = info.pageUrl || (tab && tab.url) || "";
  const results = [];
  for (const url of urls) results.push(await sendLinkFromMenu(url, referer, tab));
  if (results.some((r) => r.kind === "offline")) {
    notify("BDDM is not running", "Start BDDM first, then try again.");
    return;
  }
  const errors = results.filter((r) => r.kind === "error");
  if (urls.length === 1) {
    const res = results[0];
    if (res.kind === "ok") notify("Sent to BDDM", res.job.name || baseName(urls[0]));
    else if (res.kind === "window") notify("Sent to BDDM", res.message);
    else if (res.kind === "error") notify("BDDM could not add this link", res.error);
    return;
  }
  const sent = results.length - errors.length;
  notify("Sent to BDDM", sent + " of " + results.length + " items sent." +
    (errors.length ? " Not added: " + errors[0].error : ""));
});

// ---------------------------------------------------------------- messages, startup

api.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  if (!msg || sender.id !== api.runtime.id) return false;
  if (msg.type === "skipNext") {
    skipUntil = Date.now() + SKIP_WINDOW;
    api.storage.session.set({ skipUntil }).catch(() => {});
    sendResponse({ ok: true });
    return false;
  }
  if (msg.type === "dupAnswer") {
    answerDuplicate(msg.key, !!msg.again).then(sendResponse, (e) => sendResponse({ ok: false, error: e.message }));
    return true;
  }
  if (msg.type === "ping") {
    pingBDDM().then((ok) => sendResponse({ ok }));
    return true;
  }
  if (msg.type === "settingsChanged") {
    getSettings().then((s) => setBadge(s.enabled));
    sendResponse({ ok: true });
    return false;
  }
  return false;
});

api.alarms.onAlarm.addListener((a) => {
  if (a.name === POLL_ALARM) pollJobs();
});

api.runtime.onInstalled.addListener((details) => {
  // After an update, links that the old version handed back to the browser
  // may be caught again (they may work now).
  if (details && details.reason === "update") api.storage.local.set({ handedBack: {} }).catch(() => {});
  createMenus();
  getSettings().then((s) => setBadge(s.enabled));
  ensurePollAlarm();
});

api.runtime.onStartup.addListener(() => {
  createMenus();
  getSettings().then((s) => setBadge(s.enabled));
  ensurePollAlarm();
});

// Video detection lives in its own file (Firefox loads it from the manifest).
if (typeof importScripts === "function" && typeof videoPageInfo === "undefined") importScripts("videos.js");
// "Save as PDF" for view-only Google Drive files and Google Docs.
if (typeof importScripts === "function" && typeof PDF_MESSAGES === "undefined") importScripts("savepdf-bg.js");
