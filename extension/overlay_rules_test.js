// Node tests for the on-video Download button (item 71). Run: node overlay_rules_test.js
"use strict";
const t = require("./overlay_rules");
const sites = require("./hls_capture");
let failed = 0;
function eq(name, got, want) {
  if (got !== want) {
    console.error("FAIL " + name + ": got " + JSON.stringify(got) + " want " + JSON.stringify(want));
    failed++;
  }
}
function ok(name, cond) {
  if (!cond) { console.error("FAIL " + name); failed++; }
}

function vis(over) {
  return Object.assign({
    id: "v1", width: 640, height: 360, duration: 120, visibleRatio: 1,
    playing: true, visible: true, hovered: false, hidden: false, isAd: false, ancestors: [],
  }, over);
}

// --- skip tiny players, short clips, ads
eq("tiny width skipped", t.isTinyPlayer(160, 200), true);
eq("tiny height skipped", t.isTinyPlayer(400, 90), true);
eq("normal player is not tiny", t.isTinyPlayer(640, 360), false);
eq("15s is short", t.isShortVideo(15), true);
eq("29.9s is short", t.isShortVideo(29.9), true);
eq("30s is kept", t.isShortVideo(30), false);
eq("movie length is kept", t.isShortVideo(7200), false);
eq("unknown duration is not short", t.isShortVideo(NaN), false);
eq("live Infinity is not short", t.isShortVideo(Infinity), false);
eq("zero is not short", t.isShortVideo(0), false);

eq("show playing movie", t.shouldShowButton(vis()), true);
eq("hide short clip", t.shouldShowButton(vis({ duration: 12 })), false);
eq("hide tiny player", t.shouldShowButton(vis({ width: 120, height: 80 })), false);
eq("hide known ad flag", t.shouldShowButton(vis({ isAd: true })), false);
eq("hide YouTube ad-showing", t.shouldShowButton(vis({
  ancestors: [{ className: "html5-video-player ad-showing", id: "movie_player" }],
})), false);
eq("hide ytp-ad-module", t.shouldShowButton(vis({
  ancestors: [{ className: "ytp-ad-module", id: "" }],
})), false);
eq("hide ima ad container", t.shouldShowButton(vis({
  ancestors: [{ className: "ima-ad-container", id: "" }],
})), false);
eq("hide sponsored aria", t.shouldShowButton(vis({
  ancestors: [{ className: "", id: "", ariaLabel: "Sponsored" }],
})), false);
eq("do not treat 'header' as an ad", t.shouldShowButton(vis({
  ancestors: [{ className: "header-container load-badge", id: "player" }],
})), true);
eq("hide hidden video", t.shouldShowButton(vis({ hidden: true })), false);
eq("hide mostly off-screen", t.shouldShowButton(vis({ visibleRatio: 0.1, visible: true, hovered: false })), false);
eq("hovered mostly off-screen can still show (feed)", t.shouldShowButton(vis({ visibleRatio: 0.1, visible: true, hovered: true })), true);

// --- feed: follow hovered / playing, no flicker (sticky)
const feed = [
  vis({ id: "a", playing: false, visibleRatio: 0.9, hovered: false }),
  vis({ id: "b", playing: true, visibleRatio: 0.6, hovered: false }),
  vis({ id: "c", playing: false, visibleRatio: 0.3, hovered: false }),
];
feed.forEach((v) => { v.eligible = t.shouldShowButton(v); });
eq("feed picks playing video", t.pickActiveVideo(feed, null), "b");
const hovered = feed.map((v) => Object.assign({}, v, { hovered: v.id === "c", eligible: true, visible: true }));
eq("feed hover wins over playing", t.pickActiveVideo(hovered, { stickyId: "b" }), "c");
// sticky: current still playing stays even if another is slightly more visible
const still = [
  vis({ id: "a", playing: true, visibleRatio: 0.5, eligible: true }),
  vis({ id: "b", playing: true, visibleRatio: 0.55, eligible: true }),
];
eq("sticky keeps current playing to avoid flicker", t.pickActiveVideo(still, { stickyId: "a" }), "a");
eq("ineligible videos are ignored", t.pickActiveVideo([vis({ id: "x", eligible: false, playing: true })], null), null);
eq("short ad in feed is not picked", t.pickActiveVideo([
  Object.assign(vis({ id: "ad", duration: 8, playing: true }), { eligible: false }),
  Object.assign(vis({ id: "main", playing: false, visibleRatio: 0.8 }), { eligible: true }),
], null), "main");

// --- click target: YouTube/Facebook/Instagram use the page URL (no media fragment)
function hit(input) { return t.overlayClickTarget(input, sites.videoPageInfo); }

const ytWatch = "https://www.youtube.com/watch?v=dQw4w9wgWcQ";
const ytBlob = "blob:https://www.youtube.com/abc";
const ytCdn = "https://rr1.googlevideo.com/videoplayback?id=1";
let r = hit({ tabUrl: ytWatch, frameUrl: ytWatch, videoSrc: ytBlob });
eq("YouTube watch uses page URL", r.url, ytWatch);
eq("YouTube reason is page", r.reason, "page");
r = hit({ tabUrl: ytWatch, videoSrc: ytCdn });
eq("YouTube ignores googlevideo src", r.url, ytWatch);
r = hit({ tabUrl: "https://example.com/blog", frameUrl: "https://www.youtube.com/embed/dQw4w9wgWcQ", videoSrc: ytBlob });
eq("YouTube embed uses frame URL", r.url, "https://www.youtube.com/embed/dQw4w9wgWcQ");
r = hit({ tabUrl: "https://www.youtube.com/", nearbyUrl: ytWatch, videoSrc: ytBlob });
eq("YouTube feed uses nearby watch link", r.url, ytWatch);

const fb = "https://www.facebook.com/someone/videos/123";
r = hit({ tabUrl: "https://www.facebook.com/", nearbyUrl: fb, videoSrc: "blob:https://www.facebook.com/x" });
eq("Facebook feed uses permalink", r.url, fb);
eq("Facebook is page flow", r.reason, "page");
r = hit({ tabUrl: "https://www.facebook.com/watch?v=1", videoSrc: ytBlob });
eq("Facebook watch uses tab URL", r.url, "https://www.facebook.com/watch?v=1");

const ig = "https://www.instagram.com/reel/abc/";
r = hit({ tabUrl: ig, videoSrc: "blob:https://www.instagram.com/x" });
eq("Instagram reel uses page URL", r.url, ig);

// movielink / disguised HLS: detected stream, not a blob
const master = "https://watch.movielink.tv/m/token123";
r = hit({
  tabUrl: "https://qmr7cm.movielinkbd.at/download18plus/x",
  frameUrl: "https://qmr7cm.movielinkbd.at/p/player",
  videoSrc: "blob:https://qmr7cm.movielinkbd.at/mse",
  list: [{ url: master, kind: "hls" }],
});
eq("movielink uses detected HLS", r.url, master);
eq("movielink kind hls", r.kind, "hls");
eq("movielink is not page flow", r.reason, "detected");

r = hit({
  tabUrl: "https://example.com/watch",
  videoSrc: "https://cdn.example.com/movie.mp4",
});
eq("direct mp4 uses src", r.url, "https://cdn.example.com/movie.mp4");
eq("direct mp4 opens the video dialog", r.dialog, true);

r = hit({ tabUrl: "https://example.com/article", videoSrc: "blob:https://example.com/x", list: [] });
eq("no useful url -> none", r.reason, "none");
eq("no useful url empty", r.url, "");

// Auto-popup rule from item 70 is unchanged: button sites still must not
// auto-open the window on YouTube/Facebook.
eq("auto-offer still false on YouTube", sites.shouldAutoOfferStream(ytWatch, { kind: "hls", url: master }), false);
eq("auto-offer still false on Facebook", sites.shouldAutoOfferStream(fb, { kind: "hls", url: master }), false);
eq("auto-offer still true on movielink", sites.shouldAutoOfferStream("https://qmr7cm.movielinkbd.at/x", { kind: "hls", url: master }), true);

eq("safe http thumb", t.safeThumbnail("https://i.ytimg.com/vi/x/hqdefault.jpg"), "https://i.ytimg.com/vi/x/hqdefault.jpg");
eq("safe jpeg data", t.safeThumbnail("data:image/jpeg;base64,AAAA").startsWith("data:image/jpeg"), true);
eq("reject javascript thumb", t.safeThumbnail("javascript:alert(1)"), "");
eq("reject oversized thumb", t.safeThumbnail("data:image/jpeg;base64," + "A".repeat(600000)), "");

if (failed) { console.error(failed + " failed"); process.exit(1); }
console.log("ok");
