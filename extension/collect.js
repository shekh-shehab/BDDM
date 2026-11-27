// BDDM: "Download all media on this page with BDDM" (item 90).
// Collects the photos, videos and post links the page shows to the user
// (only what this browser can already see; nothing locked is unlocked).
// DRM players (encrypted media) are counted, never collected.
(function () {
  "use strict";
  const MAX = 1500;
  const MIN_SIDE = 180; // smaller images are icons, avatars and emojis

  function web(u) {
    try {
      const x = new URL(u, location.href);
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
  function postLink(el) {
    const a = el && el.closest ? el.closest("a[href]") : null;
    if (!a) return "";
    const u = web(a.getAttribute("href"));
    if (!u) return "";
    try {
      const x = new URL(u);
      if (x.host !== location.host && !x.host.endsWith("." + location.host.replace(/^www\./, ""))) return "";
      return POST_RE.test(x.pathname) ? u : "";
    } catch (e) { return ""; }
  }
  function label(el) {
    const a = el.closest ? el.closest("a[href]") : null;
    const t = (el.getAttribute && (el.getAttribute("alt") || el.getAttribute("title") || el.getAttribute("aria-label"))) ||
      (a && (a.getAttribute("aria-label") || a.getAttribute("title"))) || "";
    return t.replace(/\s+/g, " ").trim().slice(0, 200);
  }

  function collect() {
    const out = [];
    const seen = new Set();
    let locked = 0;
    const add = (it) => {
      const key = it.media || it.page;
      if (!key || seen.has(key) || out.length >= MAX) return;
      seen.add(key);
      out.push(it);
    };
    for (const v of document.querySelectorAll("video")) {
      if (v.mediaKeys) { locked++; continue; } // DRM: never downloaded
      const src = web(v.currentSrc || v.src || (v.querySelector("source[src]") || {}).src || "");
      const page = postLink(v);
      const thumb = web(v.poster || "");
      if (src && !/^blob:/.test(v.currentSrc || v.src || "")) {
        add({ kind: "video", media: src, page, thumb, title: label(v), duration: isFinite(v.duration) ? Math.round(v.duration) : 0 });
      } else if (page) {
        add({ kind: "post", page, thumb, title: label(v) });
      } else if (v.currentSrc || v.src) {
        locked++; // a stream player: sent as the tab's detected streams instead
      }
    }
    for (const img of document.images) {
      const w = img.naturalWidth || img.width, h = img.naturalHeight || img.height;
      const big = Math.min(w, h) >= MIN_SIDE;
      const page = postLink(img);
      const src = web(bestFromSrcset(img.srcset) || img.currentSrc || img.src);
      if (!src || /^data:/.test(img.src || "")) continue;
      if (big) add({ kind: "photo", media: src, page, thumb: web(img.currentSrc || img.src), title: label(img) });
      else if (page && Math.min(w, h) >= 60) add({ kind: "post", page, thumb: src, title: label(img) });
    }
    // Post links without a big picture (grids that load pictures as
    // backgrounds, or not loaded yet).
    for (const a of document.querySelectorAll("a[href]")) {
      if (out.length >= MAX) break;
      const page = postLink(a);
      if (!page || seen.has(page)) continue;
      const img = a.querySelector("img");
      add({ kind: "post", page, thumb: img ? web(img.currentSrc || img.src) : "", title: label(img || a) });
    }
    return { items: out, protected: locked, title: document.title || "", url: location.href };
  }

  if (typeof module !== "undefined") module.exports = { bestFromSrcset, POST_RE };
  if (typeof window === "undefined" || window.top !== window) return;
  if (typeof api === "undefined" || !api.runtime || !api.runtime.onMessage) return;
  api.runtime.onMessage.addListener((msg, sender, sendResponse) => {
    if (!msg || msg.type !== "bddm-collect-media") return false;
    try { sendResponse(collect()); } catch (e) { sendResponse({ items: [], protected: 0, error: String(e) }); }
    return false;
  });
})();
