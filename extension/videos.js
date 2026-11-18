// BDDM Download Catcher - video detection (loaded after background.js).
//
// Finds videos on the open pages and lists them in the popup:
//  - video sites (YouTube, Facebook, Instagram, TikTok, Vimeo, X, ...) by the
//    page address;
//  - HLS (.m3u8) and DASH (.mpd) streams and direct video files that the page
//    loads (seen with webRequest).
// The toolbar badge shows how many videos the current tab has.
// "Download with BDDM" sends {url, referer, cookie, kind:"video", title} to
// BDDM, which opens its "Download Video" window (quality, audio only, ...).

const VIDEO_SITES = [
  [/(^|\.)youtube\.com$/, /^\/(watch|shorts\/|live\/|playlist|embed\/)/],
  [/^youtu\.be$/, /^\/[\w-]{6,}/],
  [/(^|\.)facebook\.com$/, /^\/(watch|reel\/|share\/[vr]\/|[^/]+\/videos\/|video\.php|videos\/)/],
  [/^fb\.watch$/, /^\/./],
  [/(^|\.)instagram\.com$/, /^\/(p|reel|reels|tv)\//],
  [/(^|\.)tiktok\.com$/, /^\/(@[^/]+\/video\/|t\/|v\/)/],
  [/(^|\.)vimeo\.com$/, /^\/(\d+|channels\/|showcase\/)/],
  [/(^|\.)dailymotion\.com$/, /^\/video\//],
  [/^(x|twitter|mobile\.twitter|mobile\.x)\.com$/, /^\/[^/]+\/status\/\d+/],
  [/(^|\.)twitch\.tv$/, /^\/(videos\/|[^/]+\/clip\/)/],
  [/(^|\.)reddit\.com$/, /^\/r\/[^/]+\/comments\//],
  [/(^|\.)bilibili\.com$/, /^\/video\//],
];
// Sites whose players load many small pieces: the page link is enough.
const PAGE_ONLY_HOSTS = /(^|\.)(youtube\.com|googlevideo\.com|ytimg\.com|facebook\.com|fbcdn\.net|instagram\.com|cdninstagram\.com|tiktok\.com|tiktokcdn\.com|tiktokcdn-us\.com|tiktokv\.com|byteoversea\.com|ibyteimg\.com|twimg\.com|x\.com|twitter\.com)$/;
const MAX_PER_TAB = 20;
const MIN_FILE = 1024 * 1024; // direct video files smaller than this are skipped
const SEGMENT_RE = /\.(ts|m4s|m4v|m4a|aac|cmfv|cmfa|vtt|webvtt|key|jpg|png)(\?|$)|\/seg(ment)?[-_]?\d+|[?&](range|bytestart)=|\/(chunk|frag(ment)?)[-_]?\d+/i;

const tabVideos = new Map(); // tabId -> {page: url, list: [{url, kind, label, title, master}]}
let tabsLoaded = null;

function videoPageInfo(u) {
  let x;
  try { x = new URL(u); } catch (e) { return false; }
  if (!/^https?:$/.test(x.protocol)) return false;
  const host = x.hostname.toLowerCase().replace(/^(www|m|mobile|web)\./, "");
  return VIDEO_SITES.some(([h, p]) => h.test(host) && p.test(x.pathname));
}

function pageKey(u) {
  try { const x = new URL(u); x.hash = ""; return x.href; } catch (e) { return u || ""; }
}

async function loadTabs() {
  if (!tabsLoaded) {
    tabsLoaded = (async () => {
      try {
        const s = await api.storage.session.get({ tabVideos: {} });
        for (const [k, v] of Object.entries(s.tabVideos || {})) if (!tabVideos.has(+k)) tabVideos.set(+k, v);
      } catch (e) { /* no session storage */ }
    })();
  }
  return tabsLoaded;
}

let saveTimer = 0;
function saveTabs() {
  clearTimeout(saveTimer);
  saveTimer = setTimeout(() => {
    api.storage.session.set({ tabVideos: Object.fromEntries(tabVideos) }).catch(() => {});
  }, 300);
}

function updateBadge(tabId) {
  const t = tabVideos.get(tabId);
  const n = t ? t.list.length : 0;
  api.action.setBadgeText({ tabId, text: n ? String(n) : "" }).catch(() => {});
  if (n) api.action.setBadgeBackgroundColor({ tabId, color: "#2563eb" }).catch(() => {});
  if (n && api.action.setBadgeTextColor) api.action.setBadgeTextColor({ tabId, color: "#ffffff" }).catch(() => {});
}

function entryFor(tabId, page) {
  let t = tabVideos.get(tabId);
  if (!t || t.page !== pageKey(page)) {
    t = { page: pageKey(page), list: [] };
    tabVideos.set(tabId, t);
  }
  return t;
}

function addVideo(tabId, page, v) {
  const t = entryFor(tabId, page);
  if (t.list.some((x) => x.url === v.url)) return;
  if (t.list.length >= MAX_PER_TAB) return;
  if (v.kind === "page") t.list.unshift(v); else t.list.push(v);
  saveTabs();
  updateBadge(tabId);
}

function removeVideo(tabId, url) {
  const t = tabVideos.get(tabId);
  if (!t) return;
  const before = t.list.length;
  t.list = t.list.filter((x) => x.url !== url);
  if (t.list.length !== before) { saveTabs(); updateBadge(tabId); }
}

function streamLabel(u, kind) {
  let name = "";
  try {
    const x = new URL(u);
    name = decodeURIComponent(x.pathname.split("/").filter(Boolean).pop() || x.hostname);
  } catch (e) { name = u; }
  if (name.length > 60) name = name.slice(0, 57) + "...";
  const what = kind === "hls" ? "HLS stream" : kind === "dash" ? "DASH stream" : "Video file";
  return what + " \u00b7 " + name;
}

// Page addresses (also single-page apps like YouTube that change the
// address without loading a new page).
async function checkTabPage(tabId, tab) {
  await loadTabs();
  if (!tab || !isWebUrl(tab.url)) return;
  const t = entryFor(tabId, tab.url); // a new address clears the old list
  if (videoPageInfo(tab.url)) {
    const old = t.list.find((x) => x.kind === "page");
    if (old) {
      if (tab.title && old.title !== tab.title) { old.title = tab.title; saveTabs(); }
    } else {
      addVideo(tabId, tab.url, { url: tab.url, kind: "page", title: tab.title || "", label: "" });
    }
  }
  updateBadge(tabId);
}

api.tabs.onUpdated.addListener((tabId, info, tab) => {
  if (info.url || info.title || info.status === "complete") checkTabPage(tabId, tab).catch(() => {});
});
api.tabs.onRemoved.addListener((tabId) => {
  loadTabs().then(() => { if (tabVideos.delete(tabId)) saveTabs(); });
});

// Streams and video files the pages load.
function headerOf(details, name) {
  const h = (details.responseHeaders || []).find((x) => x.name.toLowerCase() === name);
  return h ? String(h.value || "") : "";
}

function classify(details) {
  const u = details.url;
  let path = "";
  try { path = new URL(u).pathname.toLowerCase(); } catch (e) { return ""; }
  const ct = headerOf(details, "content-type").toLowerCase().split(";")[0].trim();
  if (path.endsWith(".m3u8") || ct === "application/vnd.apple.mpegurl" || ct === "application/x-mpegurl" || ct === "audio/mpegurl") return "hls";
  if (path.endsWith(".mpd") || ct === "application/dash+xml") return "dash";
  if (ct.startsWith("video/") && ct !== "video/mp2t" && ct !== "video/iso.segment" && !SEGMENT_RE.test(u)) {
    let size = -1;
    const cr = headerOf(details, "content-range").match(/\/(\d+)\s*$/);
    if (cr) size = parseInt(cr[1], 10);
    else if (details.statusCode === 200) size = parseInt(headerOf(details, "content-length") || "-1", 10);
    if (size >= MIN_FILE) return "file";
  }
  return "";
}

// HLS pages load a master playlist and then one playlist per quality; only
// the master is worth listing (BDDM offers all its qualities).
// Some sites put extra bytes before "#EXTM3U", and some encrypt the whole
// playlist (then it has no "#EXTM3U" at all and is marked "encrypted").
async function hlsIsVariant(url) {
  try {
    const r = await fetchTimeout(url, { credentials: "include" }, 5000);
    // Many video servers refuse the extension's own request (their check
    // wants the player's Referer): then guess from the addresses.
    if (!r.ok) return { variant: false, children: [], encrypted: false, failed: true };
    let text = (await r.text()).slice(0, 200000);
    const at = text.indexOf("#EXTM3U");
    if (at < 0) return { variant: false, children: [], encrypted: looksEncrypted(text) };
    text = text.slice(at);
    if (/#EXT-X-STREAM-INF/i.test(text)) return { variant: false, children: hlsChildren(url, text), encrypted: false };
    return { variant: /#EXTINF/i.test(text), children: [], encrypted: false };
  } catch (e) {
    return { variant: false, children: [], encrypted: false, failed: true };
  }
}

// The folder of a link ("https://cdn/x/y/" for https://cdn/x/y/index.m3u8?t=1).
function folderOf(u) {
  try { const x = new URL(u); return x.origin + x.pathname.replace(/[^/]*$/, ""); } catch (e) { return ""; }
}

function isMasterName(u) {
  try { return /(^|\/)master[^/]*\.m3u8$/i.test(new URL(u).pathname); } catch (e) { return false; }
}

// Base64 text or binary data instead of a playlist.
function looksEncrypted(text) {
  const t = String(text || "").trim();
  if (t.length < 32) return false;
  if (/^[A-Za-z0-9+/=_\-\s]+$/.test(t)) return true;
  let bin = 0;
  for (let i = 0; i < Math.min(t.length, 2000); i++) { const c = t.charCodeAt(i); if (c < 9 || (c > 126 && c < 160)) bin++; }
  return bin * 10 > Math.min(t.length, 2000);
}

// The page (or embedded player frame) that loaded a request: sites often
// check that the stream is asked for by their own player.
function frameOf(details, tabUrl) {
  if (isWebUrl(details.documentUrl)) return details.documentUrl; // Firefox
  // Chromium: the full address of the frame (seen when it loaded), then the
  // Referer the browser sent; the "initiator" is only an origin.
  const frame = details.frameId > 0 ? frameAddress(details.tabId, details.frameId) : "";
  if (isWebUrl(frame)) return frame;
  if (details.frameId === 0 && isWebUrl(tabUrl)) return tabUrl;
  const sent = seenHeaders([details.url]);
  if (isWebUrl(sent.Referer || sent.referer)) return sent.Referer || sent.referer;
  if (details.initiator && /^https?:\/\//.test(details.initiator)) return details.initiator + "/";
  if (isWebUrl(details.originUrl)) return details.originUrl;
  return tabUrl;
}

function hlsChildren(base, text) {
  const out = [];
  for (const line of text.split(/\r?\n/)) {
    const s = line.trim();
    if (!s || s.startsWith("#")) continue;
    try { out.push(new URL(s, base).href); } catch (e) { /* skip */ }
  }
  return out;
}

async function onMediaResponse(details) {
  if (details.tabId < 0 || details.statusCode >= 400) return;
  if (details.type === "sub_frame") {
    // A YouTube/Vimeo/... player embedded in another page.
    if (!videoPageInfo(details.url)) return;
    await loadTabs();
    const tab = await api.tabs.get(details.tabId).catch(() => null);
    if (!tab || !isWebUrl(tab.url) || pageKey(tab.url) === pageKey(details.url)) return;
    addVideo(details.tabId, tab.url, { url: details.url, kind: "embed", title: tab.title || "", label: "Embedded player" });
    return;
  }
  const kind = classify(details);
  if (!kind) return;
  let host = "";
  try { host = new URL(details.url).hostname.toLowerCase(); } catch (e) { return; }
  if (PAGE_ONLY_HOSTS.test(host)) return;
  await loadTabs();
  let tab;
  try { tab = await api.tabs.get(details.tabId); } catch (e) { return; }
  if (!tab || !isWebUrl(tab.url)) return;
  const t = entryFor(details.tabId, tab.url);
  if (PAGE_ONLY_HOSTS.test(hostOf(tab.url)) && videoPageInfo(tab.url)) return;
  if (isBrowserOnly(details.url, tab.url)) return; // Send Anywhere: one-time links
  if (t.list.some((x) => x.url === details.url)) return;
  if (kind === "hls") {
    if ((t.hlsChildren || []).includes(details.url)) return; // a quality of a listed master
    const r = await hlsIsVariant(details.url);
    if (r.failed) {
      // The playlist could not be read here. A "master" playlist is listed and
      // replaces the playlists in its folder; any other playlist in the folder
      // of a listed master is one of its qualities or audio tracks.
      const folder = folderOf(details.url);
      if (isMasterName(details.url)) {
        for (const x of [...t.list]) {
          if (x.kind === "hls" && x.url !== details.url && folder && x.url.startsWith(folder)) removeVideo(details.tabId, x.url);
        }
        r.variant = false;
      } else if (t.list.some((x) => x.kind === "hls" && x.master && folder.startsWith(folderOf(x.url)))) {
        return;
      }
    }
    if (r.children.length) {
      t.hlsChildren = [...new Set([...(t.hlsChildren || []), ...r.children])].slice(-200);
      for (const c of r.children) removeVideo(details.tabId, c);
    }
    if (r.variant && t.list.some((x) => x.kind === "hls" && x.master)) return;
    addVideo(details.tabId, tab.url, { url: details.url, kind, master: !r.variant, title: tab.title || "",
      label: streamLabel(details.url, kind) + (r.encrypted ? " (encrypted)" : ""), encrypted: !!r.encrypted,
      frame: frameOf(details, tab.url) });
    return;
  }
  addVideo(details.tabId, tab.url, { url: details.url, kind, title: tab.title || "", label: streamLabel(details.url, kind),
    frame: frameOf(details, tab.url) });
}

if (api.webRequest && api.webRequest.onHeadersReceived) {
  api.webRequest.onHeadersReceived.addListener(
    (d) => { onMediaResponse(d).catch(() => {}); },
    { urls: ["http://*/*", "https://*/*"], types: ["xmlhttprequest", "media", "other", "object", "sub_frame"] },
    ["responseHeaders"]
  );
}

// ---------------------------------------------------------------- popup and menu

async function videosForTab(tabId) {
  await loadTabs();
  try {
    const tab = await api.tabs.get(tabId);
    if (tab && isWebUrl(tab.url)) await checkTabPage(tabId, tab);
  } catch (e) { /* tab gone */ }
  const t = tabVideos.get(tabId);
  return t ? t.list : [];
}

async function sendVideo(url, tab, title) {
  const tabUrl = tab && isWebUrl(tab.url) ? tab.url : "";
  // A stream seen on this tab: send the player frame that loaded it as the
  // referer (BDDM also sends its Origin), and say which kind of stream it is.
  let v = null;
  if (tab) {
    await loadTabs();
    const t = tabVideos.get(tab.id);
    v = t ? t.list.find((x) => x.url === url) : null;
  }
  const referer = v && isWebUrl(v.frame) ? v.frame : tabUrl;
  const req = {
    // 1.4.2: the player frame and the tab's page, so BDDM can try the other
    // Referers when the video server refuses the first one (HTTP 403).
    frame: v && isWebUrl(v.frame) ? v.frame : "", page: tabUrl,
    // A video file itself ("file") goes to BDDM's fast engine, not yt-dlp.
    url, referer, kind: v && v.kind === "file" ? "media" : "video", title: title || (tab && tab.title) || "",
    stream: v && (v.kind === "hls" || v.kind === "dash") ? v.kind : "",
    cookie: await cookieHeader(url, { referer: tabUrl, storeId: tab && tab.cookieStoreId }),
  };
  await withBrowserDetails(req, [url], { referer: tabUrl, storeId: tab && tab.cookieStoreId });
  return sendToBDDM(req);
}

api.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  if (!msg || sender.id !== api.runtime.id) return false;
  if (msg.type === "listVideos") {
    videosForTab(msg.tabId).then((list) => sendResponse({ ok: true, list }), () => sendResponse({ ok: true, list: [] }));
    return true;
  }
  if (msg.type === "sendVideo") {
    (async () => {
      const tab = await api.tabs.get(msg.tabId).catch(() => null);
      const res = await sendVideo(msg.url, tab, msg.title);
      if (res.kind === "ok") return { ok: true };
      if (res.kind === "offline") return { ok: false, error: "BDDM is not running. Start it first, then try again." };
      return { ok: false, error: res.error || "BDDM could not add this video." };
    })().then(sendResponse, (e) => sendResponse({ ok: false, error: e.message }));
    return true;
  }
  return false;
});

function createVideoMenu() {
  api.contextMenus.create({
    id: "bddm-video-page", title: "Download this video with BDDM", contexts: ["page", "video"],
    documentUrlPatterns: ["*://*.youtube.com/*", "*://youtu.be/*", "*://*.facebook.com/*", "*://*.instagram.com/*",
      "*://*.tiktok.com/*", "*://*.vimeo.com/*", "*://vimeo.com/*", "*://*.dailymotion.com/*", "*://x.com/*",
      "*://twitter.com/*", "*://*.twitch.tv/*", "*://*.reddit.com/*", "*://*.bilibili.com/*"],
  }, () => void api.runtime.lastError);
}

api.contextMenus.onClicked.addListener(async (info, tab) => {
  if (info.menuItemId !== "bddm-video-page") return;
  const url = info.pageUrl || (tab && tab.url) || "";
  if (!videoPageInfo(url)) {
    notify("BDDM", "Open the video itself first (its own page), then try again.");
    return;
  }
  const res = await sendVideo(url, tab, tab && tab.title);
  if (res.kind === "ok") notify("Sent to BDDM", "Choose the quality in the BDDM window.");
  else if (res.kind === "offline") notify("BDDM is not running", "Start BDDM first, then try again.");
  else notify("BDDM could not add this video", res.error || "");
});
