// BDDM: IDM-like "Download with BDDM" button on visible videos (item 71).
// Runs in every http(s) frame. Skips ads, tiny players and very short clips.
// A click asks the background to open BDDM's compact video window (thumbnail,
// title, qualities, Download). It does not auto-open that window; item 70's
// auto-offer rules are unchanged.
(() => {
  try {
    if (location.protocol !== "http:" && location.protocol !== "https:") return;
    if (location.hostname === "127.0.0.1" && location.port === "9614") return;
  } catch (e) { return; }
  if (typeof shouldShowButton !== "function") return;

  const HOST_ID = "bddm-dl-overlay-host";
  let hoverId = "";
  let stickyId = "";
  let hoverTimer = 0;
  let host = null;
  let btn = null;
  let raf = 0;
  let lastCss = "";

  function uid(el) {
    if (!el.__bddmId) el.__bddmId = "v" + Math.random().toString(36).slice(2);
    return el.__bddmId;
  }

  function hiddenStyle(el) {
    const s = getComputedStyle(el);
    return s.display === "none" || s.visibility === "hidden" || parseFloat(s.opacity) === 0;
  }

  function ancestorsOf(el) {
    const out = [];
    let n = el, steps = 0;
    while (n && n.nodeType === 1 && steps < 16) {
      out.push({
        className: String(n.className && n.className.baseVal != null ? n.className.baseVal : n.className || ""),
        id: n.id || "",
        ariaLabel: n.getAttribute && (n.getAttribute("aria-label") || ""),
        title: n.title || "",
        role: n.getAttribute && (n.getAttribute("role") || ""),
      });
      n = n.parentElement;
      steps++;
    }
    return out;
  }

  function visibility(el, r) {
    const vw = window.innerWidth || document.documentElement.clientWidth || 0;
    const vh = window.innerHeight || document.documentElement.clientHeight || 0;
    const x = Math.max(0, Math.min(r.right, vw) - Math.max(r.left, 0));
    const y = Math.max(0, Math.min(r.bottom, vh) - Math.max(r.top, 0));
    const area = Math.max(1, r.width * r.height);
    return (x * y) / area;
  }

  function fsRoot() {
    return document.fullscreenElement || document.webkitFullscreenElement || null;
  }

  function describe(el) {
    const r = el.getBoundingClientRect();
    const ratio = visibility(el, r);
    const duration = el.duration;
    return {
      id: uid(el),
      el,
      width: r.width,
      height: r.height,
      duration: Number.isFinite(duration) ? duration : (el.seekable && el.seekable.length ? NaN : duration),
      visibleRatio: ratio,
      visible: ratio > 0 && r.width > 0 && r.height > 0 && !hiddenStyle(el),
      playing: !el.paused && !el.ended,
      hovered: uid(el) === hoverId,
      hidden: hiddenStyle(el),
      isAd: false,
      ancestors: ancestorsOf(el),
      src: el.currentSrc || el.src || "",
      poster: el.poster || el.getAttribute("poster") || "",
    };
  }

  function nearbyVideoUrl(el) {
    const root = el.closest([
      "ytd-rich-item-renderer", "ytd-video-renderer", "ytd-grid-video-renderer",
      "ytd-reel-video-renderer", "ytd-player", "ytd-watch-flexy",
      "[role='article']", "article", "[data-video-id]", "[data-e2e='feed-video']",
    ].join(",")) || el.parentElement;
    if (!root) return "";
    const a = root.querySelector('a[href*="/watch?v="], a[href*="/shorts/"], a[href*="/videos/"], a[href*="/reel/"], a[href*="/reels/"], a[href*="video.php"]');
    if (!a) return "";
    try { return new URL(a.getAttribute("href") || a.href, location.href).href; } catch (e) { return ""; }
  }

  function canonicalUrl() {
    const og = document.querySelector('meta[property="og:url"]');
    if (og && og.content) {
      try { return new URL(og.content, location.href).href; } catch (e) { /* ignore */ }
    }
    const link = document.querySelector('link[rel="canonical"]');
    if (link && link.href) {
      try { return new URL(link.href, location.href).href; } catch (e) { /* ignore */ }
    }
    return "";
  }

  function pageTitle(el) {
    const aria = el && (el.getAttribute("aria-label") || el.getAttribute("title"));
    if (aria) return aria;
    const og = document.querySelector('meta[property="og:title"]');
    if (og && og.content) return og.content;
    return document.title || "";
  }

  function captureThumb(el, poster) {
    const safe = typeof safeThumbnail === "function" ? safeThumbnail : function (s) { return s; };
    if (poster && /^https?:\/\//i.test(poster)) return safe(poster);
    try {
      if (el.videoWidth < 16 || el.videoHeight < 16) return safe(poster);
      const w = 320, h = Math.max(1, Math.round(el.videoHeight * 320 / el.videoWidth));
      const c = document.createElement("canvas");
      c.width = w; c.height = h;
      const ctx = c.getContext("2d");
      ctx.drawImage(el, 0, 0, w, h);
      return safe(c.toDataURL("image/jpeg", 0.6));
    } catch (e) {
      return safe(poster);
    }
  }

  function ensureHost(parent) {
    if (!host) {
      host = document.createElement("div");
      host.id = HOST_ID;
      host.setAttribute("data-bddm", "download-overlay");
      Object.assign(host.style, {
        position: "fixed", zIndex: "2147483646", pointerEvents: "none",
        margin: "0", padding: "0", border: "0", overflow: "visible",
      });
      const shadow = host.attachShadow({ mode: "closed" });
      const style = document.createElement("style");
      style.textContent = [
        ":host { all: initial; }",
        "button { pointer-events: auto; position: absolute; top: 10px; right: 10px;",
        "  display: inline-flex; align-items: center; gap: 5px; height: 28px; padding: 0 9px;",
        "  border: 0; border-radius: 6px; cursor: pointer; color: #fff; font: 600 12px/1 'Segoe UI', system-ui, sans-serif;",
        "  background: rgba(37, 99, 235, .94); box-shadow: 0 1px 4px rgba(0,0,0,.35);",
        "  letter-spacing: .01em; white-space: nowrap; }",
        "button:hover { background: #1d4ed8; }",
        "button:focus { outline: 2px solid #fff; outline-offset: 1px; }",
        "svg { width: 14px; height: 14px; flex: none; }",
      ].join("");
      btn = document.createElement("button");
      btn.type = "button";
      btn.setAttribute("aria-label", "Download with BDDM");
      btn.title = "Download with BDDM";
      btn.innerHTML = '<svg viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M8 2v8m0 0L5 7m3 3 3-3M3 13h10" stroke="#fff" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/></svg><span>Download with BDDM</span>';
      const stop = (e) => { e.preventDefault(); e.stopPropagation(); if (e.stopImmediatePropagation) e.stopImmediatePropagation(); };
      for (const ev of ["pointerdown", "mousedown", "mouseup", "dblclick", "contextmenu"]) {
        btn.addEventListener(ev, stop, true);
      }
      btn.addEventListener("click", (e) => { stop(e); onClick(); }, true);
      shadow.append(style, btn);
    }
    if (host.parentElement !== parent) parent.appendChild(host);
  }

  function hideHost() {
    if (host) {
      host.style.display = "none";
      lastCss = "";
    }
  }

  function place(el) {
    const r = el.getBoundingClientRect();
    const root = fsRoot();
    const parent = (root && root.contains(el)) ? root : document.documentElement;
    ensureHost(parent);
    let top = r.top, left = r.left;
    if (root && parent === root) {
      const pr = root.getBoundingClientRect();
      top = r.top - pr.top;
      left = r.left - pr.left;
    }
    const css = top.toFixed(1) + "," + left.toFixed(1) + "," + r.width.toFixed(1) + "," + r.height.toFixed(1) + "," + (parent === root ? "fs" : "p");
    if (css === lastCss && host.style.display !== "none") return;
    lastCss = css;
    Object.assign(host.style, {
      display: "block",
      top: top + "px",
      left: left + "px",
      width: r.width + "px",
      height: Math.max(0, r.height - 48) + "px", // leave the control bar free
    });
  }

  function scan() {
    const nodes = document.querySelectorAll("video");
    const described = [];
    for (const el of nodes) {
      const d = describe(el);
      d.eligible = shouldShowButton(d);
      described.push(d);
    }
    const id = pickActiveVideo(described, { stickyId: stickyId });
    stickyId = id || "";
    if (!id) { hideHost(); return; }
    const active = described.find((d) => d.id === id);
    if (!active) { hideHost(); return; }
    place(active.el);
  }

  function schedule() {
    if (raf) return;
    raf = requestAnimationFrame(() => { raf = 0; try { scan(); } catch (e) { /* leave the page alone */ } });
  }

  async function onClick() {
    const nodes = document.querySelectorAll("video");
    let active = null;
    for (const el of nodes) {
      if (uid(el) === stickyId) { active = describe(el); break; }
    }
    if (!active) {
      try { await api.runtime.sendMessage({ type: "overlayDownload", tabUrl: location.href, frameUrl: location.href }); } catch (e) {}
      return;
    }
    const msg = {
      type: "overlayDownload",
      tabUrl: window.top === window ? location.href : "",
      frameUrl: location.href,
      canonicalUrl: canonicalUrl(),
      nearbyUrl: nearbyVideoUrl(active.el),
      videoSrc: isBlobOrMediaSrc(active.src) ? "" : active.src,
      title: pageTitle(active.el),
      thumbnail: captureThumb(active.el, active.poster),
    };
    try {
      const res = await api.runtime.sendMessage(msg);
      if (res && res.ok === false && res.error) {
        btn.title = res.error;
      }
    } catch (e) { /* extension was reloaded */ }
  }

  document.addEventListener("mouseover", (e) => {
    const v = e.target && e.target.closest && e.target.closest("video");
    if (v) {
      hoverId = uid(v);
      clearTimeout(hoverTimer);
      schedule();
      return;
    }
    if (host && (e.target === host || (host.shadowRoot && host.shadowRoot.contains && host.contains(e.target)))) {
      clearTimeout(hoverTimer);
      return;
    }
    // Hovering our button (closed shadow) still counts as host.
    if (host && host.contains(e.composedPath && e.composedPath()[0] || e.target)) {
      clearTimeout(hoverTimer);
      return;
    }
    clearTimeout(hoverTimer);
    hoverTimer = setTimeout(() => { hoverId = ""; schedule(); }, 200);
  }, true);

  // Closed shadow: contain check on composed path.
  document.addEventListener("mouseover", (e) => {
    const path = e.composedPath && e.composedPath();
    if (path && host && path.indexOf(host) >= 0) {
      clearTimeout(hoverTimer);
    }
  }, true);

  document.addEventListener("scroll", schedule, { capture: true, passive: true });
  window.addEventListener("resize", schedule, { passive: true });
  document.addEventListener("fullscreenchange", schedule);
  document.addEventListener("webkitfullscreenchange", schedule);
  document.addEventListener("loadedmetadata", schedule, true);
  document.addEventListener("play", schedule, true);
  document.addEventListener("pause", schedule, true);
  document.addEventListener("durationchange", schedule, true);

  const mo = new MutationObserver(schedule);
  mo.observe(document.documentElement, { childList: true, subtree: true });
  setInterval(schedule, 800);
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", schedule, { once: true });
  } else {
    schedule();
  }
})();
