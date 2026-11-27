// BDDM: "Download all media on this page with BDDM" (items 90 and 92).
// Collects the photos, videos and post links the page shows to the user:
// only what this browser's own session can already open. Locked, paid and
// DRM media is counted as protected, never collected.
(function () {
  "use strict";
  const MAX = 1500;
  const MIN_SIDE = 180;  // smaller pictures are icons, avatars and emojis
  const MIN_SHOWN = 100; // shown smaller than this on the page: an avatar or icon
  const FANSLY_MAX_POSTS = 500;
  const FANSLY_TIME = 90000; // ms for listing a Fansly profile

  function web(u) {
    try {
      const x = new URL(u, typeof location !== "undefined" ? location.href : undefined);
      return /^https?:$/.test(x.protocol) ? x.href : "";
    } catch (e) { return ""; }
  }
  // The largest image of a srcset ("a.jpg 320w, b.jpg 1080w").
  function bestFromSrcset(s) {
    let best = "", size = 0;
    for (const part of String(s || "").split(",")) {
      const bits = part.trim().split(/\s+/);
      if (!bits[0]) continue;
      const n = parseFloat(bits[1] || "1") || 1;
      if (n >= size) { size = n; best = bits[0]; }
    }
    return best;
  }
  // Links that open one post, clip or video on common sites.
  const POST_RE = /\/(p|reel|reels|tv|video|videos|post|posts|watch|clip|clips|status|gifs\/detail|v)\/[^/?#]+/i;

  // ------------------------------------------------------------ site chrome

  // Pictures that are part of the site, not media: logos, icons, SVGs,
  // emojis, badges, sprites, spinners.
  const CHROME_URL_RE = /(^|[/_.\-])(logo|logos|favicon|sprite|sprites|icon|icons|emoji|emojis|badge|badges|placeholder|spinner|loader)([/_.\-]|$)/i;
  function chromeUrl(u) {
    if (!u) return true;
    if (/^data:image\/svg/i.test(u)) return true;
    let p = u;
    try { p = new URL(u).pathname; } catch (e) {}
    return /\.(svg|ico)$/i.test(p) || CHROME_URL_RE.test(p);
  }
  // Words in a class, id, alt or tag name that mark site chrome.
  const CHROME_WORD_RE = /(^|[\s_\-])(logo|brand|icon|favicon|avatar|emoji|badge|sprite|navbar|nav|sidebar|header|footer|menu|toolbar|topbar|banner)([\s_\-]|$)/i;
  function chromeWords(s) { return CHROME_WORD_RE.test(String(s || "")); }
  const CHROME_SEL = "header, nav, footer, aside, [role=banner], [role=navigation], [role=contentinfo], [role=complementary], [role=menu], [role=menubar], [role=toolbar]";
  function isChromeElement(el) {
    if (!el || !el.closest) return false;
    if (el.closest(CHROME_SEL)) return true;
    if (chromeWords(el.getAttribute && el.getAttribute("alt"))) return true;
    let e = el;
    for (let i = 0; e && i < 6; i++, e = e.parentElement) {
      const cls = typeof e.className === "string" ? e.className : (e.getAttribute && e.getAttribute("class")) || "";
      if (chromeWords(cls) || chromeWords(e.id) || chromeWords(String(e.tagName || "").toLowerCase().replace(/^app-/, ""))) return true;
      if (e.tagName === "svg" || e.tagName === "SVG") return true;
    }
    return false;
  }
  function shownSize(el) {
    try {
      const r = el.getBoundingClientRect();
      return { w: r.width, h: r.height };
    } catch (e) { return { w: 0, h: 0 }; }
  }
  // Shown on the page smaller than an avatar (0 = not laid out: unknown).
  function shownSmall(el) {
    const s = shownSize(el);
    return s.w > 0 && s.h > 0 && Math.min(s.w, s.h) < MIN_SHOWN;
  }

  // ------------------------------------------------------------ any page

  function sameSite(host) {
    const own = location.host.replace(/^www\./, "");
    return host === location.host || host === own || host.endsWith("." + own);
  }
  function postLink(el) {
    const a = el && el.closest ? el.closest("a[href]") : null;
    if (!a) return "";
    const u = web(a.getAttribute("href"));
    if (!u) return "";
    try {
      const x = new URL(u);
      return sameSite(x.host) && POST_RE.test(x.pathname) ? u : "";
    } catch (e) { return ""; }
  }
  function label(el) {
    if (!el) return "";
    const a = el.closest ? el.closest("a[href]") : null;
    const t = (el.getAttribute && (el.getAttribute("alt") || el.getAttribute("title") || el.getAttribute("aria-label"))) ||
      (a && (a.getAttribute("aria-label") || a.getAttribute("title"))) || "";
    return t.replace(/\s+/g, " ").trim().slice(0, 200);
  }
  function imgSource(img) {
    const lazy = img.getAttribute("data-src") || img.getAttribute("data-lazy-src") || img.getAttribute("data-original") || "";
    return web(bestFromSrcset(img.getAttribute("srcset") || img.getAttribute("data-srcset")) || img.currentSrc || img.src || lazy);
  }

  // mediaHost(url): false for pictures this site never uses for media
  // (Fansly: everything not on its CDN).
  function collectDOM(mediaHost) {
    const out = [];
    const seen = new Set();
    let locked = 0;
    const okHost = (u) => {
      if (!mediaHost) return true;
      try { return mediaHost(new URL(u).hostname); } catch (e) { return false; }
    };
    const add = (it) => {
      const key = it.media || it.page;
      if (!key || seen.has(key) || out.length >= MAX) return;
      seen.add(key);
      out.push(it);
    };
    for (const v of document.querySelectorAll("video")) {
      if (v.mediaKeys) { locked++; continue; } // DRM: never downloaded
      if (isChromeElement(v)) continue;
      const raw = v.currentSrc || v.src || ((v.querySelector("source[src]") || {}).src) || "";
      const src = /^blob:/.test(raw) ? "" : web(raw);
      const page = postLink(v);
      const poster = web(v.poster || "");
      const thumb = poster && okHost(poster) ? poster : "";
      if (src && okHost(src)) {
        add({ kind: "video", media: src, page, thumb, title: label(v), duration: isFinite(v.duration) ? Math.round(v.duration) : 0 });
      } else if (page) {
        add({ kind: "post", page, thumb, title: label(v) });
      }
      // A blob: player is sent as the tab's detected stream instead.
    }
    for (const img of document.images) {
      const src = imgSource(img);
      if (!src) continue;
      if (chromeUrl(src) || isChromeElement(img) || shownSmall(img)) continue;
      const w = img.naturalWidth || 0, h = img.naturalHeight || 0;
      const shown = shownSize(img);
      // Not loaded yet (lazy): judge by the size it is shown at.
      const big = w && h ? Math.min(w, h) >= MIN_SIDE : Math.min(shown.w, shown.h) >= MIN_SIDE;
      const page = postLink(img);
      if (big && okHost(src)) add({ kind: "photo", media: src, page, thumb: web(img.currentSrc || img.src) || src, title: label(img) });
      else if (page && Math.min(w || shown.w, h || shown.h) >= 60) add({ kind: "post", page, thumb: okHost(src) ? src : "", title: label(img) });
    }
    // Pictures shown as CSS backgrounds.
    let n = 0;
    for (const el of document.querySelectorAll("[style*='background-image']")) {
      if (++n > 3000 || out.length >= MAX) break;
      const m = /url\(\s*['"]?([^'")]+)['"]?\s*\)/i.exec(el.style.backgroundImage || "");
      const src = m ? web(m[1]) : "";
      if (!src || chromeUrl(src) || !okHost(src) || isChromeElement(el)) continue;
      const s = shownSize(el);
      if (Math.min(s.w, s.h) < MIN_SIDE) continue;
      add({ kind: "photo", media: src, page: postLink(el), thumb: src, title: label(el) });
    }
    // Post links without a big picture (grids not loaded yet).
    for (const a of document.querySelectorAll("a[href]")) {
      if (out.length >= MAX) break;
      if (isChromeElement(a)) continue;
      const page = postLink(a);
      if (!page || seen.has(page)) continue;
      const img = a.querySelector("img");
      const t = img ? imgSource(img) : "";
      add({ kind: "post", page, thumb: t && !chromeUrl(t) && okHost(t) ? t : "", title: label(img || a) });
    }
    return { items: out, protected: locked };
  }

  // ------------------------------------------------------------ Fansly

  // fansly.com/<creator>/posts[/wall/<id>], /<creator>/media, /post/<id>
  const FANSLY_RESERVED = new Set(["home", "lists", "explore", "messages", "notifications", "settings", "search",
    "collection", "subscriptions", "wallet", "login", "signup", "tos", "privacy", "faq", "application", "live", "post"]);
  function fanslyPath(pathname) {
    let m = /^\/post\/(\d+)/.exec(pathname);
    if (m) return { post: m[1] };
    m = /^\/([A-Za-z0-9_.\-]{2,40})(?:\/(posts|media)(?:\/wall\/(\d+))?)?\/?$/.exec(pathname);
    if (!m || FANSLY_RESERVED.has(m[1].toLowerCase())) return null;
    return { creator: m[1], wall: m[3] || "", byId: /^\d{6,}$/.test(m[1]) };
  }
  // Fansly's CDN (post media), not fansly.com's own pictures.
  function fanslyMediaHost(h) {
    h = String(h || "").toLowerCase();
    return h !== "fansly.com" && h !== "www.fansly.com" && /(^|\.)fansly(cdn)?\.(com|net)$/.test(h);
  }
  function fanslyNameFromPage(info) {
    if (info && info.creator && !info.byId) return info.creator;
    const t = document.title || "";
    let m = /@([A-Za-z0-9_.\-]{2,40})/.exec(t);
    if (m) return m[1];
    for (const el of document.querySelectorAll("[class*='username'], [class*='user-name']")) {
      m = /@?([A-Za-z0-9_.\-]{2,40})/.exec((el.textContent || "").trim());
      if (m && !/^\d+$/.test(m[1])) return m[1];
    }
    return "";
  }

  // Joins Fansly's answer (posts, accountMedia, accountMediaBundles) into
  // posts with their media, like Fansly's own page does.
  function fanslyPosts(resp) {
    const media = new Map(), bundles = new Map();
    for (const m of (resp && resp.accountMedia) || []) media.set(String(m.id), m);
    for (const b of (resp && resp.accountMediaBundles) || []) bundles.set(String(b.id), b);
    const posts = [];
    const missing = new Set();
    for (const p of (resp && resp.posts) || []) {
      const atts = [];
      for (const a of p.attachments || []) {
        const cid = String(a.contentId || "");
        if (media.has(cid)) atts.push(cid);
        else if (bundles.has(cid)) {
          const content = (bundles.get(cid).bundleContent || []).slice().sort((x, y) => (x.pos || 0) - (y.pos || 0));
          for (const c of content) {
            const mid = String(c.accountMediaId);
            atts.push(mid);
            if (!media.has(mid)) missing.add(mid);
          }
        }
      }
      posts.push({ id: String(p.id), content: p.content || "", media: atts });
    }
    return { posts, media, missing };
  }

  function metaObject(m) {
    if (!m) return null;
    if (typeof m === "object") return m;
    try { return JSON.parse(m); } catch (e) { return null; }
  }
  // The best file of one Fansly media the user's account can open, or
  // null when it is locked (no file locations for this account).
  function fanslyFile(md) {
    if (!md) return null;
    const variants = (md.variants || []).filter((v) => v && v.locations && v.locations.length);
    if (md.locations && md.locations.length) variants.push(md);
    if (!variants.length) return null;
    const isVideo = /^video\//.test(md.mimetype || "") || md.type === 2;
    const wantRe = isVideo ? /^video\//i : /^image\//i;
    const direct = variants.filter((v) => wantRe.test(v.mimetype || "") && !metaObject(v.locations[0].metadata));
    direct.sort((a, b) => (b.width || 0) - (a.width || 0));
    if (direct.length) {
      const v = direct[0];
      return { kind: isVideo ? "video" : "photo", media: v.locations[0].location, mime: v.mimetype };
    }
    // Only a signed playlist (HLS/DASH): it needs the CDN's cookies that
    // Fansly gives this account for it.
    const man = variants.find((v) => metaObject(v.locations[0].metadata));
    if (man) {
      const meta = metaObject(man.locations[0].metadata) || {};
      const cookie = ["Key-Pair-Id", "Signature", "Policy"].filter((k) => meta[k]).map((k) => "CloudFront-" + k + "=" + meta[k]).join("; ");
      return { kind: "video", media: man.locations[0].location, stream: /dash/i.test(man.mimetype || "") ? "dash" : "hls", cookie };
    }
    return null;
  }
  function fanslyThumb(am) {
    const pick = (md) => {
      if (!md) return "";
      const all = (md.variants || []).concat([md]).filter((v) => v && v.locations && v.locations.length && /^image\//i.test(v.mimetype || ""));
      all.sort((a, b) => Math.abs((a.width || 0) - 360) - Math.abs((b.width || 0) - 360));
      return all.length ? all[0].locations[0].location : "";
    };
    return pick(am.preview) || pick(am.media);
  }
  const EXT = { "image/jpeg": "jpg", "image/png": "png", "image/gif": "gif", "image/webp": "webp", "video/mp4": "mp4", "video/webm": "webm", "video/quicktime": "mov" };

  // Turns joined posts into BDDM items; locked media is only counted.
  function fanslyItems(joined) {
    const items = [];
    let locked = 0;
    for (const p of joined.posts) {
      let n = 0;
      for (const mid of p.media) {
        const am = joined.media.get(mid);
        if (!am) { locked++; continue; }
        const f = fanslyFile(am.media);
        if (!f) { locked++; continue; }
        n++;
        const meta = metaObject(am.media && am.media.metadata) || {};
        const it = {
          kind: f.kind, media: f.media, page: "https://fansly.com/post/" + p.id, post: "fs:" + p.id, index: n,
          title: String(p.content || "").replace(/\s+/g, " ").trim().slice(0, 120),
          thumb: fanslyThumb(am) || (f.kind === "photo" ? f.media : ""),
        };
        if (f.stream) it.stream = f.stream;
        if (f.cookie) it.cookie = f.cookie;
        if (EXT[f.mime]) it.ext = EXT[f.mime];
        if (meta.duration) it.duration = Math.round(Number(meta.duration)) || 0;
        items.push(it);
        if (items.length >= MAX) return { items, locked };
      }
    }
    return { items, locked };
  }

  // The token of the user's own Fansly session in this browser (Fansly's
  // page keeps it in its storage and sends it with each of its own API
  // calls). It is used only here, for Fansly's own API, and never leaves
  // the browser; nothing is sent to BDDM but the media links.
  function fanslyToken() {
    try {
      const s = JSON.parse(localStorage.getItem("session_active_session") || "null");
      return (s && typeof s.token === "string" && s.token) || "";
    } catch (e) { return ""; }
  }
  async function fanslyCall(token, endpoint, params) {
    const q = new URLSearchParams(Object.assign({}, params, { "ngsw-bypass": "true" }));
    const r = await fetch("https://apiv3.fansly.com/api" + endpoint + "?" + q, {
      headers: { authorization: token, "fansly-client-ts": String(Date.now()) },
      credentials: "include",
    });
    if (!r.ok) throw new Error("Fansly answered " + r.status);
    const d = await r.json();
    if (!d || d.response === undefined) throw new Error("unexpected answer");
    return d.response;
  }
  function minId(posts) {
    let m = null;
    for (const p of posts) {
      try { const v = BigInt(p.id); if (m === null || v < m) m = v; } catch (e) {}
    }
    return m === null ? "" : m.toString();
  }
  const pause = (ms) => new Promise((r) => setTimeout(r, ms));

  // Lists a Fansly creator (or one post) through Fansly's API, as the
  // signed-in user sees it. null = not signed in or the API failed.
  async function fanslyCollect() {
    const info = fanslyPath(location.pathname);
    const token = fanslyToken();
    if (!info || !token) return null;
    const start = Date.now();
    const all = { posts: [], media: new Map(), missing: new Set() };
    const seenPosts = new Set();
    const merge = (resp) => {
      const j = fanslyPosts(resp);
      for (const [k, v] of j.media) all.media.set(k, v);
      let added = 0;
      for (const p of j.posts) if (!seenPosts.has(p.id)) { seenPosts.add(p.id); all.posts.push(p); added++; }
      for (const k of j.missing) all.missing.add(k);
      return j.posts;
    };
    let name = "", limited = false;
    try {
      if (info.post) {
        merge(await fanslyCall(token, "/v1/post", { ids: info.post }));
      } else {
        const accounts = await fanslyCall(token, "/v1/account", info.byId ? { ids: info.creator } : { usernames: info.creator });
        const account = Array.isArray(accounts) ? accounts[0] : null;
        if (!account || !account.id) return null;
        name = account.username || "";
        const walls = info.wall ? [info.wall] : ((account.walls || []).map((w) => String(w.id)));
        if (!walls.length) walls.push("");
        outer:
        for (const wall of walls) {
          let before = "0";
          for (;;) {
            if (all.posts.length >= FANSLY_MAX_POSTS || Date.now() - start > FANSLY_TIME) { limited = true; break outer; }
            const resp = await fanslyCall(token, "/v1/timelinenew/" + account.id, { before, after: "0", wallId: wall, contentSearch: "" });
            const got = merge(resp);
            if (!got.length) break;
            const next = minId(got);
            if (!next || next === before) break;
            before = next;
            await pause(250);
          }
        }
      }
      // Bundle media that was not in the timeline's answer.
      const ids = [...all.missing].filter((k) => !all.media.has(k));
      for (let i = 0; i < ids.length && Date.now() - start < FANSLY_TIME + 20000; i += 50) {
        const list = await fanslyCall(token, "/v1/account/media", { ids: ids.slice(i, i + 50).join(",") });
        for (const m of list || []) all.media.set(String(m.id), m);
      }
    } catch (e) {
      if (!all.posts.length) return null;
      limited = true;
    }
    const r = fanslyItems(all);
    return { items: r.items, protected: r.locked, name: name || fanslyNameFromPage(info), complete: true, limited, api: true };
  }

  async function collect() {
    const host = location.hostname.replace(/^www\./, "");
    if (host === "fansly.com") {
      let r = null;
      try { r = await fanslyCollect(); } catch (e) { r = null; }
      if (r && (r.items.length || r.protected)) return Object.assign(r, { title: document.title || "", url: location.href });
      const d = collectDOM(fanslyMediaHost);
      return { items: d.items, protected: d.protected, name: fanslyNameFromPage(fanslyPath(location.pathname)), title: document.title || "", url: location.href };
    }
    const d = collectDOM(null);
    return { items: d.items, protected: d.protected, title: document.title || "", url: location.href };
  }

  if (typeof module !== "undefined") {
    module.exports = { bestFromSrcset, POST_RE, chromeUrl, chromeWords, fanslyPath, fanslyMediaHost, fanslyPosts, fanslyFile, fanslyItems, minId };
  }
  if (typeof window === "undefined" || window.top !== window) return;
  if (typeof api === "undefined" || !api.runtime || !api.runtime.onMessage) return;
  api.runtime.onMessage.addListener((msg, sender, sendResponse) => {
    if (!msg || msg.type !== "bddm-collect-media") return false;
    collect().then(sendResponse, (e) => sendResponse({ items: [], protected: 0, error: String(e) }));
    return true; // answers later
  });
})();
