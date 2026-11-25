// Node tests for disguised HLS capture helpers. Run: node hls_capture_test.js
"use strict";
const t = require("./hls_capture");
let failed = 0;
function eq(name, got, want) {
  if (got !== want) {
    console.error("FAIL " + name + ": got " + JSON.stringify(got) + " want " + JSON.stringify(want));
    failed++;
  }
}
function ok(name, cond) {
  if (!cond) {
    console.error("FAIL " + name);
    failed++;
  }
}

function d(over) {
  return Object.assign({
    url: "https://watch.movielink.tv/m/token123",
    method: "GET",
    type: "xmlhttprequest",
    tabId: 7,
    statusCode: 200,
    responseHeaders: [
      { name: "Content-Type", value: "image/jpeg" },
      { name: "Content-Length", value: "312" },
    ],
  }, over);
}

// --- classify / maybe-hls (movielink-style disguised playlist)
eq("movielink /m/ jpeg is maybe-hls", t.classify(d()), "maybe-hls");
eq("movielink /hls/ jpeg is maybe-hls", t.classify(d({ url: "https://watch.movielink.tv/hls/token123" })), "maybe-hls");
eq("plain .m3u8 is hls, not maybe", t.classify(d({
  url: "https://cdn.example.com/index.m3u8",
  responseHeaders: [{ name: "Content-Type", value: "application/vnd.apple.mpegurl" }],
})), "hls");
eq("dash mpd", t.classify(d({
  url: "https://cdn.example.com/manifest.mpd",
  responseHeaders: [{ name: "Content-Type", value: "application/dash+xml" }],
})), "dash");
eq("mp4 file is file", t.classify(d({
  url: "https://cdn.example.com/movie.mp4",
  type: "media",
  responseHeaders: [
    { name: "Content-Type", value: "video/mp4" },
    { name: "Content-Length", value: String(20 * 1024 * 1024) },
  ],
})), "file");
eq("tiny jpeg image is not maybe-hls (has .jpg)", t.classify(d({
  url: "https://cdn.example.com/poster.jpg",
  responseHeaders: [
    { name: "Content-Type", value: "image/jpeg" },
    { name: "Content-Length", value: "800" },
  ],
})), "");

// --- filter scoping: only disguised maybe-hls, never YouTube/Facebook/files/Drive
eq("filter movielink /m/", t.shouldFilterDisguisedPlaylist(d()), true);
eq("filter /hls/", t.shouldFilterDisguisedPlaylist(d({ url: "https://watch.movielink.tv/hls/abc" })), true);
eq("no filter on YouTube googlevideo", t.shouldFilterDisguisedPlaylist(d({
  url: "https://rr1.googlevideo.com/videoplayback?id=1",
  responseHeaders: [{ name: "Content-Type", value: "video/mp4" }, { name: "Content-Length", value: "999" }],
})), false);
eq("no filter on facebook fbcdn", t.shouldFilterDisguisedPlaylist(d({
  url: "https://scontent.xx.fbcdn.net/v/t42.123/video.mp4",
  responseHeaders: [{ name: "Content-Type", value: "video/mp4" }, { name: "Content-Length", value: "999" }],
})), false);
eq("no filter on youtube.com itself", t.shouldFilterDisguisedPlaylist(d({
  url: "https://www.youtube.com/api/manifest/hls_playlist/x",
  responseHeaders: [{ name: "Content-Type", value: "application/vnd.apple.mpegurl" }],
})), false);
eq("no filter on instagram", t.shouldFilterDisguisedPlaylist(d({
  url: "https://scontent.cdninstagram.com/v/t50.2886-16/a.mp4",
  type: "xmlhttprequest",
  responseHeaders: [{ name: "Content-Type", value: "video/mp4" }, { name: "Content-Length", value: "5000" }],
})), false);
eq("no filter on proper m3u8", t.shouldFilterDisguisedPlaylist(d({
  url: "https://cdn.example.com/index.m3u8",
  responseHeaders: [{ name: "Content-Type", value: "application/vnd.apple.mpegurl" }],
})), false);
eq("no filter on type=media", t.shouldFilterDisguisedPlaylist(d({ type: "media" })), false);
eq("no filter on type=main_frame file", t.shouldFilterDisguisedPlaylist(d({
  url: "https://cdn.example.com/file.zip",
  type: "main_frame",
  responseHeaders: [{ name: "Content-Type", value: "application/zip" }, { name: "Content-Length", value: "90000" }],
})), false);
eq("no filter on Google Drive", t.shouldFilterDisguisedPlaylist(d({
  url: "https://drive.google.com/uc?id=abc&export=download",
  type: "xmlhttprequest",
  responseHeaders: [{ name: "Content-Type", value: "application/octet-stream" }, { name: "Content-Length", value: "90000" }],
})), false);
eq("no filter on tabId -1", t.shouldFilterDisguisedPlaylist(d({ tabId: -1 })), false);
eq("no filter on HTTP 403", t.shouldFilterDisguisedPlaylist(d({ statusCode: 403 })), false);
eq("no filter on own peek", t.shouldFilterDisguisedPlaylist(d(), true), false);
eq("no filter when ownPeek(url) is true", t.shouldFilterDisguisedPlaylist(d(), (u) => u.indexOf("/m/") >= 0), false);
eq("filter when ownPeek(url) is false", t.shouldFilterDisguisedPlaylist(d(), () => false), true);

// --- peek credentials: omit first, then include
eq("peek modes omit then include", t.peekCredentialModes().join(","), "omit,include");

const hdrs = t.peekRequestHeaders({ documentUrl: "https://qmr7cm.movielinkbd.at/p/player" }, "https://qmr7cm.movielinkbd.at/download18plus/x");
eq("peek Origin is the player frame", hdrs.Origin, "https://qmr7cm.movielinkbd.at");
eq("peek Referer is the player frame", hdrs.Referer, "https://qmr7cm.movielinkbd.at/p/player");

// --- 403 refetch must keep a stream the tab already received
eq("real playlist confirms", t.peekConfirmAction(d(), { ok: true, status: 200, text: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n/hls/a\n" }), "playlist");
eq("403 refetch after 200 tab keeps", t.peekConfirmAction(d(), { ok: false, status: 403, text: "#EXTM3U\n#EXT-X-ERROR:forbidden\n" }), "keep");
eq("network fail after 200 tab keeps", t.peekConfirmAction(d(), { ok: false, status: 0, text: "" }), "keep");
eq("real jpeg body after 200 drops", t.peekConfirmAction(d(), { ok: true, status: 200, text: "\xff\xd8\xff not a playlist" }), "drop");
eq("forbidden-only (tab also 403) drops", t.peekConfirmAction(d({ statusCode: 403 }), { ok: false, status: 403, text: "#EXTM3U\n#EXT-X-ERROR:forbidden\n" }), "drop");

// --- item 77: no auto-popup anywhere; the helpers are gone.
eq("no shouldAutoOfferStream export", typeof t.shouldAutoOfferStream, "undefined");
eq("no autoOfferKey export", typeof t.autoOfferKey, "undefined");

// --- context menu: YouTube/Facebook stay on the page-URL path;
// other sites send a detected stream.
eq("YouTube menu uses page flow", t.contextMenuAction("https://www.youtube.com/watch?v=abc", "hls"), "page");
eq("Facebook menu uses page flow", t.contextMenuAction("https://www.facebook.com/watch?v=1", "hls"), "page");
eq("movielink menu uses detected stream", t.contextMenuAction("https://qmr7cm.movielinkbd.at/download18plus/x", "hls"), "stream");
eq("no stream and not a video site", t.contextMenuAction("https://example.com/article", ""), "none");

// --- pass-through filter writes every original byte, including past the cap
const written = [];
const stored = [];
const mock = {
  write(data) { written.push(data instanceof Uint8Array ? data : new Uint8Array(data)); },
  close() { this.closed = true; },
  disconnect() { this.disconnected = true; },
};
t.attachPlaylistFilter(d(), mock, (details, result) => stored.push(result), { cap: 8 });
const a = new Uint8Array([35, 69, 88, 84, 77, 51, 85, 10]); // #EXTM3U\n
const b = new Uint8Array([65, 66, 67, 68]); // ABCD past cap
mock.ondata({ data: a });
mock.ondata({ data: b });
mock.onstop();
ok("filter closed", mock.closed === true);
eq("wrote both chunks", written.length, 2);
eq("first chunk unchanged", Array.from(written[0]).join(","), Array.from(a).join(","));
eq("second chunk unchanged (past cap)", Array.from(written[1]).join(","), Array.from(b).join(","));
ok("stored text starts with playlist", stored[0] && stored[0].fromFilter && stored[0].text.indexOf("#EXTM3U") === 0);
eq("stored text is capped", stored[0].text.length, 8);

eq("playlistText strips prefix", t.playlistText("xxxx#EXTM3U\n#EXTINF"), "#EXTM3U\n#EXTINF");
eq("forbidden playlist detected", t.isForbiddenPlaylist("#EXTM3U\n#EXT-X-ERROR:forbidden\n"), true);
eq("originFromURL", t.originFromURL("https://qmr7cm.movielinkbd.at/p/x?y=1"), "https://qmr7cm.movielinkbd.at");

if (failed) {
  console.error(failed + " failed");
  process.exit(1);
}
console.log("ok");
