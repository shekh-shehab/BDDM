// BDDM: helpers for the on-video "Download with BDDM" button (item 71).
// Pure functions only — no listeners at load — so Node tests can require this
// file. The content script uses the show/skip/pick rules; the background
// script uses overlayClickTarget to choose the job URL.
(function (root) {
  "use strict";

  const MIN_DURATION = 30; // hide on videos shorter than this (ads, bumps)
  const MIN_WIDTH = 200;
  const MIN_HEIGHT = 120;

  // Specific player/ad markers. Avoid generic "ad" so "header", "load",
  // "pad" or "badge" in a class name do not hide a real video.
  const AD_CLASS_RE = /(^|[\s_-])(ad-showing|ytp-ad-player-overlay|ytp-ad-module|ytp-ad-text|ytp-ad-overlay|video-ads|ima-ad-container|ima-ads-container|advertisement|ad-container|adcontainer|sponsored-video|sponsoredvideo)([\s_-]|$)/i;
  const AD_ID_RE = /^(player-ads|google_ads|ad_container|adContainer|ima-ad-container)$/i;
  const AD_LABEL_RE = /\b(sponsored|advertisement|promo ad|commercial)\b/i;

  function isTinyPlayer(width, height) {
    const w = +width || 0, h = +height || 0;
    return w < MIN_WIDTH || h < MIN_HEIGHT;
  }

  // Live or not-yet-known duration is not treated as short.
  function isShortVideo(duration) {
    return Number.isFinite(duration) && duration > 0 && duration < MIN_DURATION;
  }

  function looksLikeAd(nodes) {
    for (const n of nodes || []) {
      if (AD_CLASS_RE.test(n.className || "")) return true;
      if (AD_ID_RE.test(n.id || "")) return true;
      const label = [n.ariaLabel, n.title, n.role].filter(Boolean).join(" ");
      if (AD_LABEL_RE.test(label)) return true;
    }
    return false;
  }

  function isVisibleEnough(visibleRatio, width, height) {
    if (isTinyPlayer(width, height)) return false;
    return (visibleRatio || 0) >= 0.25;
  }

  // YouTube Shorts, Instagram Reels, TikTok and Facebook Reels are often
  // under 30 seconds on purpose. The short-clip skip is for ads and bumps
  // on regular pages, not for these players.
  const SHORT_FORM_TAG_RE = /^(ytd-reel-video-renderer|ytd-shorts|ytd-reel-player-overlay-renderer)$/i;
  function isShortFormVideo(v) {
    const url = String((v && v.pageUrl) || "");
    if (/\/shorts\//i.test(url)) return true;
    if (/tiktok\.com\//i.test(url)) return true;
    if (/\/(reel|reels)\//i.test(url)) return true;
    for (const n of (v && v.ancestors) || []) {
      if (SHORT_FORM_TAG_RE.test(n.tagName || n.tag || "")) return true;
    }
    return false;
  }

  // A button is offered on a visible, reasonably large video that is not
  // an ad and not a very short clip (unless it is a Short / Reel).
  function shouldShowButton(v) {
    if (!v) return false;
    if (v.isAd || looksLikeAd(v.ancestors)) return false;
    if (isTinyPlayer(v.width, v.height)) return false;
    if (isShortVideo(v.duration) && !isShortFormVideo(v)) return false;
    if (!isVisibleEnough(v.visibleRatio, v.width, v.height) && !v.hovered) return false;
    if (v.hidden) return false;
    return true;
  }

  // In a scrolling feed, follow the hovered video, then the playing one,
  // and stick to the current choice so the button does not flicker.
  function pickActiveVideo(videos, state) {
    const list = (videos || []).filter((v) => v && v.eligible && (v.visible || v.hovered));
    if (!list.length) return null;
    const hovered = list.find((v) => v.hovered);
    if (hovered) return hovered.id;
    const stickyId = state && state.stickyId;
    if (stickyId) {
      const cur = list.find((v) => v.id === stickyId);
      if (cur && (cur.playing || (cur.visibleRatio || 0) >= 0.4)) return cur.id;
    }
    const playing = list.filter((v) => v.playing).sort((a, b) => (b.visibleRatio || 0) - (a.visibleRatio || 0));
    if (playing[0]) return playing[0].id;
    return list.slice().sort((a, b) => (b.visibleRatio || 0) - (a.visibleRatio || 0))[0].id;
  }

  function isHttpUrl(u) {
    return /^https?:\/\//i.test(u || "");
  }

  function isBlobOrMediaSrc(u) {
    return /^(blob:|mediasource:|data:)/i.test(u || "");
  }

  // Choose the URL sent to BDDM when the on-video button is clicked.
  // Page-flow sites (YouTube, Facebook, Instagram, ...) always get the
  // page (or embed/permalink) URL, not the media fragment. Other sites
  // get a detected HLS/DASH/file stream, or the video's own http(s) src.
  function overlayClickTarget(input, isVideoPage) {
    const info = typeof isVideoPage === "function" ? isVideoPage
      : (typeof videoPageInfo === "function" ? videoPageInfo : function () { return false; });
    const tab = (input && input.tabUrl) || "";
    const frame = (input && input.frameUrl) || "";
    const candidates = [frame, tab, (input && input.canonicalUrl) || "", (input && input.nearbyUrl) || ""];
    for (const u of candidates) {
      if (u && info(u)) return { url: u, reason: "page", page: true };
    }
    const list = (input && input.list) || [];
    const src = (input && input.videoSrc) || "";
    if (src && !isBlobOrMediaSrc(src)) {
      const hit = list.find((x) => x && x.url === src);
      if (hit) return { url: hit.url, kind: hit.kind || "", reason: "detected" };
    }
    const stream = list.find((x) => x && (x.kind === "hls" || x.kind === "dash"))
      || list.find((x) => x && x.kind === "file");
    if (stream) return { url: stream.url, kind: stream.kind, reason: "detected" };
    if (src && isHttpUrl(src) && !isBlobOrMediaSrc(src)) {
      return { url: src, kind: "file", reason: "src", dialog: true };
    }
    return { url: "", reason: "none" };
  }

  function safeThumbnail(raw, maxLen) {
    const s = String(raw || "").trim();
    const cap = maxLen || 512 * 1024;
    if (!s || s.length > cap) return "";
    if (/^data:image\/(png|jpe?g|webp|gif);/i.test(s)) return s;
    if (/^https?:\/\//i.test(s) && !/\s/.test(s)) return s;
    return "";
  }

  const api = {
    MIN_DURATION,
    MIN_WIDTH,
    MIN_HEIGHT,
    AD_CLASS_RE,
    isTinyPlayer,
    isShortVideo,
    isShortFormVideo,
    looksLikeAd,
    isVisibleEnough,
    shouldShowButton,
    pickActiveVideo,
    isHttpUrl,
    isBlobOrMediaSrc,
    overlayClickTarget,
    safeThumbnail,
  };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  else {
    for (const k of Object.keys(api)) root[k] = api[k];
  }
})(typeof self !== "undefined" ? self : this);
