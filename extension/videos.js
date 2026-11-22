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
// Disguised playlist helpers (classify, auto-offer, Firefox body filter) live
// in hls_capture.js, loaded first.

if (typeof classify !== "function" && typeof importScripts === "function") importScripts("hls_capture.js");

const MAX_PER_TAB = 20;

const tabVideos = new Map(); // tabId -> {page: url, list: [{url, kind, label, title, master}]}
let tabsLoaded = null;

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
  if (t.list.some((x) => x.url === v.url)) return false;
  if (t.list.length >= MAX_PER_TAB) return false;
  if (v.kind === "page") t.list.unshift(v); else t.list.push(v);
  saveTabs();
  updateBadge(tabId);
  return true;
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

// HLS pages load a master playlist and then one playlist per quality; only
// the master is worth listing (BDDM offers all its qualities).
// Some sites put extra bytes before "#EXTM3U", and some encrypt the whole
// playlist (then it has no "#EXTM3U" at all and is marked "encrypted").
// peekPlaylist reads the start of a playlist. First from the original
// response (Firefox filterResponseData, pass-through), then from the frame
// that loaded it (content.js: omit credentials first, then include, so
// CORS "*" playlists work and the request has the player's Origin/Referer),
// then from here with the player Origin/Referer when the browser allows it.
// Our own looks (made from the page, so the browser reports them like the
// page's requests): remembered so they are not looked at again (a loop),
// and their answers are kept for a minute.
const ownPeeks = new Map(); // url -> {at, result}
const PEEK_TTL = 60000;
const pendingBodies = new Map(); // requestId -> {promise, resolve}
const capturedBodies = new Map(); // url -> {at, result}
const offeredStreams = new Set(); // autoOfferKey values

function ownPeek(url) {
  const p = ownPeeks.get(url);
  return p && Date.now() - p.at < PEEK_TTL ? p : null;
}

function rememberFilterBody(details, result) {
  if (details && result && result.text) {
    capturedBodies.set(details.url, { at: Date.now(), result });
    while (capturedBodies.size > 80) capturedBodies.delete(capturedBodies.keys().next().value);
  }
  if (details && pendingBodies.has(details.requestId)) {
    const p = pendingBodies.get(details.requestId);
    if (p && p.resolve) p.resolve(result);
    pendingBodies.delete(details.requestId);
  }
}

function waitFilterBody(details, ms) {
  if (!details) return Promise.resolve(null);
  const known = capturedBodies.get(details.url);
  if (known && Date.now() - known.at < PEEK_TTL) return Promise.resolve(known.result);
  const pending = pendingBodies.get(details.requestId);
  if (!pending) return Promise.resolve(null);
  return Promise.race([pending.promise, new Promise((r) => setTimeout(() => r(null), ms))]);
}

function startFilterCapture(details) {
  if (!api.webRequest || !api.webRequest.filterResponseData) return;
  let resolve;
  const promise = new Promise((r) => { resolve = r; });
  pendingBodies.set(details.requestId, { promise, resolve });
  const filter = api.webRequest.filterResponseData(details.requestId);
  attachPlaylistFilter(details, filter, rememberFilterBody);
}

async function peekPlaylist(url, details) {
  const known = ownPeek(url);
  if (known) return known.result || { ok: false, text: "" };
  const entry = { at: Date.now(), result: null };
  ownPeeks.set(url, entry);
  while (ownPeeks.size > 300) ownPeeks.delete(ownPeeks.keys().next().value);
  entry.result = await peekPlaylistNow(url, details);
  entry.at = Date.now();
  return entry.result;
}

async function peekPlaylistNow(url, details) {
  const filtered = await waitFilterBody(details, 1500);
  if (filtered && playlistText(filtered.text) && !isForbiddenPlaylist(filtered.text)) return filtered;

  if (details && details.tabId >= 0) {
    try {
      const r = await Promise.race([
        api.tabs.sendMessage(details.tabId, { type: "peekPlaylist", url }, { frameId: details.frameId || 0 }),
        new Promise((res) => setTimeout(() => res(null), 7000)),
      ]);
      if (r && (r.ok || (r.status && r.status < 400)) && playlistText(r.text) && !isForbiddenPlaylist(r.text)) {
        return { ok: true, status: r.status, text: String(r.text || "") };
      }
    } catch (e) { /* no content script in that frame */ }
  }
  const headers = typeof peekRequestHeaders === "function" ? peekRequestHeaders(details, details && details.documentUrl) : {};
  const modes = typeof peekCredentialModes === "function" ? peekCredentialModes() : ["omit", "include"];
  let last = { ok: false, text: "", status: 0 };
  for (const creds of modes) {
    try {
      const opts = { credentials: creds, cache: "no-store" };
      let r;
      try {
        if (headers && Object.keys(headers).length) opts.headers = headers;
        r = await fetchTimeout(url, opts, 5000);
      } catch (e) {
        // Extensions may not set Origin/Referer; retry without them.
        if (opts.headers) {
          delete opts.headers;
          r = await fetchTimeout(url, opts, 5000);
        } else {
          throw e;
        }
      }
      const text = (await r.text()).slice(0, 200000);
      last = { ok: !!r.ok, status: r.status, text };
      if (last.status < 400 && playlistText(text) && !isForbiddenPlaylist(text)) return last;
      if (last.status < 400 && last.ok && last.text && !playlistText(text)) return last;
    } catch (e) {
      last = { ok: false, text: "", status: 0, error: String(e) };
    }
  }
  return last;
}

async function hlsIsVariant(url, details, pre) {
  try {
    const r = pre || await peekPlaylist(url, details);
    // Many video servers refuse the extension's own request (their check
    // wants the player's Referer): then guess from the addresses.
    if (!r.ok) return { variant: false, children: [], encrypted: false, failed: true };
    let text = r.text;
    const at = text.indexOf("#EXTM3U");
    if (at < 0) return { variant: false, children: [], encrypted: looksEncrypted(text) };
    text = text.slice(at);
    if (/#EXT-X-STREAM-INF/i.test(text)) return { variant: false, children: hlsChildren(url, text), encrypted: false };
    return { variant: /#EXTINF/i.test(text), children: [], encrypted: false };
  } catch (e) {
    return { variant: false, children: [], encrypted: false, failed: true };
  }
}

function isMasterName(u) {
  try { return /(^|\/)master[^/]*\.m3u8$/i.test(new URL(u).pathname); } catch (e) { return false; }
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
    // Audio and subtitle playlists of the master: #EXT-X-MEDIA:...URI="..."
    const m = /^#EXT-X-(MEDIA|I-FRAME-STREAM-INF):.*URI="([^"]+)"/i.exec(s);
    if (m) { try { out.push(new URL(m[2], base).href); } catch (e) { /* skip */ } continue; }
    if (!s || s.startsWith("#")) continue;
    try { out.push(new URL(s, base).href); } catch (e) { /* skip */ }
  }
  return out;
}

async function onMediaResponse(details) {
  if (details.tabId < 0 || details.statusCode >= 400) return;
  if (ownPeek(details.url)) return; // our own look at it (peekPlaylist)
  if (details.type === "sub_frame") {
    // A YouTube/Vimeo/... player embedded in another page.
    if (!videoPageInfo(details.url)) return;
    await loadTabs();
    const tab = await api.tabs.get(details.tabId).catch(() => null);
    if (!tab || !isWebUrl(tab.url) || pageKey(tab.url) === pageKey(details.url)) return;
    addVideo(details.tabId, tab.url, { url: details.url, kind: "embed", title: tab.title || "", label: "Embedded player" });
    return;
  }
  let kind = classify(details);
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
  if (kind === "maybe-hls") {
    // Look at the content once per address; give up on a folder after
    // three answers that are not playlists (its pieces, real pictures).
    // A 403 or failed refetch must not drop a stream the tab already got.
    if ((t.hlsChildren || []).includes(details.url)) return;
    const folder = folderOf(details.url);
    t.notHls = t.notHls || {};
    t.peeks = (t.peeks || 0) + 1;
    if ((t.notHls[folder] || 0) >= 3 || t.peeks > 60) return;
    const r = await peekPlaylist(details.url, details);
    const action = peekConfirmAction(details, r);
    if (action === "drop") {
      t.notHls[folder] = (t.notHls[folder] || 0) + 1;
      return;
    }
    kind = "hls";
    if (action === "playlist") details = Object.assign({}, details, { peeked: r });
    else details = Object.assign({}, details, { peeked: { ok: false, text: "", keep: true } });
  }
  if (kind === "hls") {
    if ((t.hlsChildren || []).includes(details.url)) return; // a quality of a listed master
    const r = await hlsIsVariant(details.url, details, details.peeked);
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
    const v = { url: details.url, kind, master: !r.variant, title: tab.title || "",
      label: streamLabel(details.url, kind) + (r.encrypted ? " (encrypted)" : ""), encrypted: !!r.encrypted,
      frame: frameOf(details, tab.url) };
    if (addVideo(details.tabId, tab.url, v)) maybeAutoOffer(tab, v);
    return;
  }
  const v = { url: details.url, kind, title: tab.title || "", label: streamLabel(details.url, kind),
    frame: frameOf(details, tab.url) };
  if (addVideo(details.tabId, tab.url, v)) maybeAutoOffer(tab, v);
}

async function maybeAutoOffer(tab, v) {
  // YouTube, Facebook and other sites with their own flow are skipped
  // inside shouldAutoOfferStream; only disguised/HLS (and DASH) on other
  // sites are offered, once per page+stream.
  if (!tab || !shouldAutoOfferStream(tab.url, v)) return;
  const key = autoOfferKey(tab.url, v.url);
  if (offeredStreams.has(key)) return;
  offeredStreams.add(key);
  while (offeredStreams.size > 200) offeredStreams.delete(offeredStreams.keys().next().value);
  const res = await sendVideo(v.url, tab, tab.title);
  if (res.kind === "ok" || res.kind === "window" || res.kind === "duplicate") {
    notify("Sent to BDDM", "Choose the quality in the BDDM window.");
  } else if (res.kind === "offline") {
    notify("BDDM is not running", "Start BDDM first, then try again.");
  } else {
    offeredStreams.delete(key);
    notify("BDDM could not add this video", res.error || "");
  }
}

if (api.webRequest && api.webRequest.onHeadersReceived) {
  api.webRequest.onHeadersReceived.addListener(
    (d) => { onMediaResponse(d).catch(() => {}); },
    { urls: ["http://*/*", "https://*/*"], types: ["xmlhttprequest", "media", "other", "object", "sub_frame"] },
    ["responseHeaders"]
  );
  // Firefox only: read the original disguised-playlist body (the page already
  // got a 200) and pass every byte through unchanged so playback is not
  // broken. Never attached on YouTube/Facebook/normal files/Drive/ordinary
  // .m3u8 (shouldFilterDisguisedPlaylist). Chromium has no filterResponseData
  // on this MV3 path and must not get a blocking listener.
  if (typeof IS_FIREFOX !== "undefined" && IS_FIREFOX && api.webRequest.filterResponseData) {
    api.webRequest.onHeadersReceived.addListener(
      (d) => {
        try {
          if (shouldFilterDisguisedPlaylist(d, ownPeek)) startFilterCapture(d);
        } catch (e) { /* leave the response alone */ }
      },
      { urls: ["http://*/*", "https://*/*"], types: ["xmlhttprequest", "other"] },
      ["blocking", "responseHeaders"]
    );
  }
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

function ensureReqOrigin(req, frame, tabUrl) {
  const have = req.headers && (req.headers.Origin || req.headers.origin);
  if (have) {
    if (req.headers && !req.headers.Origin) req.headers.Origin = have;
    return;
  }
  const o = originFromURL(frame) || originFromURL(req.referer) || originFromURL(tabUrl);
  if (o) req.headers = Object.assign({}, req.headers || {}, { Origin: o });
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
  const frame = v && isWebUrl(v.frame) ? v.frame : "";
  const referer = (v && (v.kind === "hls" || v.kind === "dash") && frame) ? frame : (frame || tabUrl);
  const cookieRef = frame || tabUrl;
  const req = {
    // 1.4.2: the player frame and the tab's page, so BDDM can try the other
    // Referers when the video server refuses the first one (HTTP 403).
    frame, page: tabUrl,
    // A video file itself ("file") goes to BDDM's fast engine, not yt-dlp.
    url, referer, kind: v && v.kind === "file" ? "media" : "video", title: title || (tab && tab.title) || "",
    stream: v && (v.kind === "hls" || v.kind === "dash") ? v.kind : "",
    cookie: await cookieHeader(url, { referer: cookieRef, storeId: tab && tab.cookieStoreId }),
  };
  await withBrowserDetails(req, [url], { referer: cookieRef, storeId: tab && tab.cookieStoreId });
  if (v && (v.kind === "hls" || v.kind === "dash")) {
    if (frame && (!req.referer || /^https?:\/\/[^/]+\/?$/.test(req.referer))) req.referer = frame;
    ensureReqOrigin(req, frame, tabUrl);
  }
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
    documentUrlPatterns: ["http://*/*", "https://*/*"],
  }, () => void api.runtime.lastError);
}

function detectedStream(tab) {
  if (!tab) return null;
  const t = tabVideos.get(tab.id);
  if (!t || !t.list) return null;
  return t.list.find((x) => x.kind === "hls" || x.kind === "dash") || t.list.find((x) => x.kind === "file") || null;
}

api.contextMenus.onClicked.addListener(async (info, tab) => {
  if (info.menuItemId !== "bddm-video-page") return;
  const url = info.pageUrl || (tab && tab.url) || "";
  // YouTube, Facebook and other video-page sites keep the existing flow
  // (the page address is sent). Other sites send a detected stream.
  if (videoPageInfo(url)) {
    const res = await sendVideo(url, tab, tab && tab.title);
    if (res.kind === "ok") notify("Sent to BDDM", "Choose the quality in the BDDM window.");
    else if (res.kind === "offline") notify("BDDM is not running", "Start BDDM first, then try again.");
    else notify("BDDM could not add this video", res.error || "");
    return;
  }
  await loadTabs();
  const v = detectedStream(tab);
  if (v) {
    const res = await sendVideo(v.url, tab, tab && tab.title);
    if (res.kind === "ok") notify("Sent to BDDM", "Choose the quality in the BDDM window.");
    else if (res.kind === "offline") notify("BDDM is not running", "Start BDDM first, then try again.");
    else notify("BDDM could not add this video", res.error || "");
    return;
  }
  notify("BDDM", "No video was found on this page.");
});
