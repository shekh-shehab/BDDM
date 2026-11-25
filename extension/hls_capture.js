// BDDM: helpers for disguised HLS/DASH capture (loaded before videos.js).
// Pure functions only — no listeners at load — so Node tests can require this
// file the same way they require firefox_capture.js.
(function (root) {
  "use strict";

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
  const MIN_FILE = 1024 * 1024;
  const SEGMENT_RE = /\.(ts|m4s|m4v|m4a|aac|cmfv|cmfa|vtt|webvtt|key|jpg|png)(\?|$)|\/seg(ment)?[-_]?\d+|[?&](range|bytestart)=|\/(chunk|frag(ment)?)[-_]?\d+/i;
  const MAYBE_HLS_PATH_RE = /\/(m|hls|playlist|manifest)\//i;
  const MAYBE_HLS_TYPES = /^(image\/|text\/plain$|application\/octet-stream$|binary\/octet-stream$|$)/;
  const MAYBE_HLS_MAX = 512 * 1024;
  const FILTER_BODY_CAP = 200000;

  function headerOf(details, name) {
    const h = (details && details.responseHeaders || []).find((x) => x.name.toLowerCase() === name);
    return h ? String(h.value || "") : "";
  }

  function videoPageInfo(u) {
    let x;
    try { x = new URL(u); } catch (e) { return false; }
    if (!/^https?:$/.test(x.protocol)) return false;
    const host = x.hostname.toLowerCase().replace(/^(www|m|mobile|web)\./, "");
    return VIDEO_SITES.some(([h, p]) => h.test(host) && p.test(x.pathname));
  }

  function maybeHidden(details, path, ct) {
    if (!details) return false;
    if (details.type !== "xmlhttprequest" && details.type !== "other") return false;
    if (!MAYBE_HLS_TYPES.test(ct)) return false;
    if (/\.(ts|m4s|mp4|m4a|aac|vtt|webvtt|key|js|css|json|html?|woff2?)(\?|$)/i.test(path)) return false;
    const len = parseInt(headerOf(details, "content-length") || "-1", 10);
    if (len > MAYBE_HLS_MAX) return false;
    if (MAYBE_HLS_PATH_RE.test(path)) return true;
    return ct.startsWith("image/") && len >= 0 && len <= 64 * 1024 && !/\.(png|jpe?g|gif|webp|svg|ico|avif|bmp)(\?|$)/i.test(path);
  }

  function classify(details) {
    if (!details) return "";
    const u = details.url || "";
    let path = "";
    try { path = new URL(u).pathname.toLowerCase(); } catch (e) { return ""; }
    const ct = headerOf(details, "content-type").toLowerCase().split(";")[0].trim();
    if (path.endsWith(".m3u8") || ct === "application/vnd.apple.mpegurl" || ct === "application/x-mpegurl" || ct === "audio/mpegurl") return "hls";
    if (maybeHidden(details, path, ct)) return "maybe-hls";
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

  function playlistText(text) {
    const at = String(text || "").indexOf("#EXTM3U");
    return at < 0 || at > 4096 ? "" : String(text).slice(at);
  }

  function isForbiddenPlaylist(text) {
    const t = playlistText(text);
    return t ? /#EXT-X-ERROR:\s*forbidden/i.test(t) : false;
  }

  function looksEncrypted(text) {
    const t = String(text || "").trim();
    if (t.length < 32) return false;
    if (/^[A-Za-z0-9+/=_\-\s]+$/.test(t)) return true;
    let bin = 0;
    for (let i = 0; i < Math.min(t.length, 2000); i++) { const c = t.charCodeAt(i); if (c < 9 || (c > 126 && c < 160)) bin++; }
    return bin * 10 > Math.min(t.length, 2000);
  }

  function folderOf(u) {
    try { const x = new URL(u); return x.origin + x.pathname.replace(/[^/]*$/, ""); } catch (e) { return ""; }
  }

  function pageKey(u) {
    try { const x = new URL(u); x.hash = ""; return x.href; } catch (e) { return u || ""; }
  }

  function originFromURL(u) {
    try {
      const x = new URL(u);
      if (x.protocol === "http:" || x.protocol === "https:") return x.origin;
    } catch (e) { /* not a URL */ }
    return "";
  }

  // Try credentials "omit" first so a CORS "*" playlist (no cookies) works
  // in the page, then "include" for cookie-gated playlists.
  function peekCredentialModes() {
    return ["omit", "include"];
  }

  function peekRequestHeaders(details, tabUrl) {
    const headers = {};
    const frame = (details && (details.documentUrl || details.originUrl)) || tabUrl || "";
    const origin = originFromURL(frame) || originFromURL(details && details.initiator);
    if (origin) headers.Origin = origin;
    if (/^https?:\/\//i.test(frame)) headers.Referer = frame;
    return headers;
  }

  // "playlist" — body is a real playlist; "keep" — the tab already got a
  // successful maybe-hls answer, but our own refetch failed or was forbidden
  // (do not drop the stream); "drop" — not a playlist.
  function peekConfirmAction(details, r) {
    if (r && playlistText(r.text) && !isForbiddenPlaylist(r.text) && (!r.status || r.status < 400)) return "playlist";
    if (r && (r.ok || (r.status && r.status < 400)) && r.text && !playlistText(r.text) && !isForbiddenPlaylist(r.text)) return "drop";
    const tabOK = details && details.statusCode >= 200 && details.statusCode < 400;
    if (tabOK && (!r || !r.ok || r.status >= 400 || isForbiddenPlaylist(r && r.text) || !r.text)) return "keep";
    return "drop";
  }

  // Firefox filterResponseData is attached only to candidate disguised
  // playlist requests. YouTube, Facebook, normal files, Google Drive and
  // ordinary .m3u8 answers must not be filtered.
  function shouldFilterDisguisedPlaylist(details, isOwnPeek) {
    if (!details || details.tabId < 0) return false;
    if (details.statusCode < 200 || details.statusCode >= 400) return false;
    if (details.type !== "xmlhttprequest" && details.type !== "other") return false;
    const u = details.url || "";
    if (/^(blob:|data:)/i.test(u)) return false;
    if (typeof isOwnPeek === "function") {
      if (isOwnPeek(u)) return false;
    } else if (isOwnPeek) {
      return false;
    }
    let host = "";
    try { host = new URL(u).hostname.toLowerCase(); } catch (e) { return false; }
    if (PAGE_ONLY_HOSTS.test(host)) return false;
    return classify(details) === "maybe-hls";
  }

  function decodeChunks(chunks) {
    let total = 0;
    for (const c of chunks || []) total += c.byteLength || c.length || 0;
    const buf = new Uint8Array(total);
    let off = 0;
    for (const c of chunks || []) {
      const u8 = c instanceof Uint8Array ? c : new Uint8Array(c);
      buf.set(u8, off);
      off += u8.byteLength;
    }
    try {
      return new TextDecoder("utf-8").decode(buf);
    } catch (e) {
      let s = "";
      for (let i = 0; i < buf.length; i++) s += String.fromCharCode(buf[i]);
      return s;
    }
  }

  // Attach a pass-through filter: every original chunk is written unchanged
  // (including bytes past the stash cap). Playback must not be altered.
  function attachPlaylistFilter(details, filter, store, opts) {
    const cap = (opts && opts.cap) || FILTER_BODY_CAP;
    const chunks = [];
    let stored = 0;
    filter.ondata = (event) => {
      const data = event.data;
      if (stored < cap) {
        const u8 = data instanceof Uint8Array ? data : new Uint8Array(data);
        const room = cap - stored;
        chunks.push(u8.byteLength > room ? u8.subarray(0, room) : u8);
        stored += Math.min(u8.byteLength, room);
      }
      filter.write(data);
    };
    filter.onstop = () => {
      try { filter.close(); } catch (e) { try { filter.disconnect(); } catch (e2) { /* ignore */ } }
      const result = { ok: true, status: details && details.statusCode || 200, text: decodeChunks(chunks), fromFilter: true };
      if (typeof store === "function") store(details, result);
    };
    filter.onerror = () => {
      try { filter.disconnect(); } catch (e) { /* ignore */ }
      if (typeof store === "function") store(details, null);
    };
    return filter;
  }

  function contextMenuAction(pageUrl, detectedKind) {
    if (videoPageInfo(pageUrl)) return "page";
    if (detectedKind === "hls" || detectedKind === "dash" || detectedKind === "file") return "stream";
    return "none";
  }

  const api = {
    VIDEO_SITES,
    PAGE_ONLY_HOSTS,
    MIN_FILE,
    SEGMENT_RE,
    MAYBE_HLS_PATH_RE,
    MAYBE_HLS_TYPES,
    MAYBE_HLS_MAX,
    FILTER_BODY_CAP,
    headerOf,
    videoPageInfo,
    maybeHidden,
    classify,
    playlistText,
    isForbiddenPlaylist,
    looksEncrypted,
    folderOf,
    pageKey,
    originFromURL,
    peekCredentialModes,
    peekRequestHeaders,
    peekConfirmAction,
    shouldFilterDisguisedPlaylist,
    decodeChunks,
    attachPlaylistFilter,
    contextMenuAction,
  };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  else {
    for (const k of Object.keys(api)) root[k] = api[k];
  }
})(typeof self !== "undefined" ? self : this);
