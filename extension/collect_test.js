// node collect_test.js: checks collect.js helpers.
const assert = require("assert");
const c = require("./collect.js");
const { bestFromSrcset, POST_RE } = c;
assert.strictEqual(bestFromSrcset("a.jpg 320w, b.jpg 1080w, c.jpg 640w"), "b.jpg");
assert.strictEqual(bestFromSrcset("x.jpg"), "x.jpg");
assert.strictEqual(bestFromSrcset(""), "");
for (const p of ["/p/Cabc123/", "/reel/Cxyz/", "/@a/video/7312", "/post/9", "/status/123", "/watch/abc"]) assert.ok(POST_RE.test(p), p);
for (const p of ["/", "/explore/", "/accounts/login/", "/@soloshinee"]) assert.ok(!POST_RE.test(p), p);

// Site chrome (bug 92): logos, icons, SVGs, avatars of the UI.
for (const u of ["https://fansly.com/assets/images/logo.png", "https://fansly.com/assets/fansly_light.svg", "https://x.test/static/icons/heart.png",
  "https://x.test/favicon.ico", "data:image/svg+xml;base64,AAAA", "https://x.test/img/brand-logo_2x.webp", "https://x.test/emoji/1f600.png"]) {
  assert.ok(c.chromeUrl(u), "chrome: " + u);
}
for (const u of ["https://cdn3.fansly.com/785/123456.jpeg?ngsw-bypass=true&Policy=x", "https://cdn.test/photos/beach.jpg", "https://i.test/iconic-view.jpg"]) {
  assert.ok(!c.chromeUrl(u), "media: " + u);
}
for (const s of ["site-logo", "nav-bar", "user-avatar small", "sidebar", "app-header", "menu_item"]) assert.ok(c.chromeWords(s), s);
for (const s of ["post-media", "feed-item-content", "image-container", "navigation-free", "avatarless"]) assert.ok(!c.chromeWords(s), s);

// Fansly pages and media hosts.
assert.deepStrictEqual(c.fanslyPath("/creator1/posts"), { creator: "creator1", wall: "", byId: false });
assert.deepStrictEqual(c.fanslyPath("/creator1/posts/wall/785496081252233221"), { creator: "creator1", wall: "785496081252233221", byId: false });
assert.deepStrictEqual(c.fanslyPath("/785496081252233221/posts"), { creator: "785496081252233221", wall: "", byId: true });
assert.deepStrictEqual(c.fanslyPath("/post/123"), { post: "123" });
assert.strictEqual(c.fanslyPath("/explore"), null);
assert.strictEqual(c.fanslyPath("/messages"), null);
assert.ok(c.fanslyMediaHost("cdn3.fansly.com"));
assert.ok(!c.fanslyMediaHost("fansly.com"));
assert.ok(!c.fanslyMediaHost("www.fansly.com"));
assert.ok(!c.fanslyMediaHost("evilfansly.com"));
assert.strictEqual(c.minId([{ id: "785496081252233229" }, { id: "785496081252233221" }]), "785496081252233221");

// A Fansly timeline answer: a photo post, a bundle (carousel) with one
// media missing from the answer, a locked (paid) video and a video that
// only has a signed playlist.
const loc = (u, meta) => meta ? [{ location: u, metadata: meta }] : [{ location: u }];
const resp = {
  posts: [
    { id: "100", content: "Beach  day", attachments: [{ contentId: "m1" }] },
    { id: "101", content: "Set", attachments: [{ contentId: "b1" }] },
    { id: "102", content: "Paid video", attachments: [{ contentId: "m4" }] },
    { id: "103", content: "Clip", attachments: [{ contentId: "m5" }] },
  ],
  accountMedia: [
    { id: "m1", media: { type: 1, mimetype: "image/jpeg", width: 2000, locations: loc("https://cdn3.fansly.com/a/full.jpeg?sig=1"),
      variants: [{ mimetype: "image/jpeg", width: 360, locations: loc("https://cdn3.fansly.com/a/360.jpeg") }] } },
    { id: "m2", media: { type: 1, mimetype: "image/png", width: 800, locations: loc("https://cdn3.fansly.com/b/2.png") } },
    { id: "m4", media: { type: 2, mimetype: "video/mp4", width: 1080, locations: [], variants: [{ mimetype: "video/mp4", locations: [] }] },
      preview: { mimetype: "image/jpeg", width: 360, locations: loc("https://cdn3.fansly.com/p/prev.jpeg") } },
    { id: "m5", media: { type: 2, mimetype: "video/mp4", width: 1920, metadata: "{\"duration\": 12.4}", locations: [],
      variants: [{ mimetype: "application/vnd.apple.mpegurl", width: 1920, locations: loc("https://cdn3.fansly.com/v/master.m3u8", { "Key-Pair-Id": "K", Signature: "S", Policy: "P" }) },
        { mimetype: "image/jpeg", width: 360, locations: loc("https://cdn3.fansly.com/v/thumb.jpeg") }] } },
  ],
  accountMediaBundles: [{ id: "b1", bundleContent: [{ accountMediaId: "m3", pos: 1 }, { accountMediaId: "m2", pos: 0 }] }],
};
const joined = c.fanslyPosts(resp);
assert.deepStrictEqual(joined.posts.map((p) => p.media), [["m1"], ["m2", "m3"], ["m4"], ["m5"]]);
assert.deepStrictEqual([...joined.missing], ["m3"]);
const r = c.fanslyItems(joined);
assert.strictEqual(r.items.length, 3, JSON.stringify(r.items));
assert.strictEqual(r.locked, 2); // m3 (not readable) and the paid m4
const [p1, p2, v] = r.items;
assert.strictEqual(p1.kind, "photo");
assert.strictEqual(p1.media, "https://cdn3.fansly.com/a/full.jpeg?sig=1"); // the full size, not the 360 one
assert.strictEqual(p1.thumb, "https://cdn3.fansly.com/a/360.jpeg");
assert.strictEqual(p1.post, "fs:100");
assert.strictEqual(p1.page, "https://fansly.com/post/100");
assert.strictEqual(p1.title, "Beach day");
assert.strictEqual(p1.ext, "jpg");
assert.strictEqual(p2.media, "https://cdn3.fansly.com/b/2.png");
assert.strictEqual(p2.index, 1);
assert.strictEqual(v.kind, "video");
assert.strictEqual(v.stream, "hls");
assert.strictEqual(v.cookie, "CloudFront-Key-Pair-Id=K; CloudFront-Signature=S; CloudFront-Policy=P");
assert.strictEqual(v.thumb, "https://cdn3.fansly.com/v/thumb.jpeg");
assert.strictEqual(v.duration, 12);
assert.ok(!r.items.some((it) => /prev\.jpeg/.test(it.media)), "a locked video's preview is not downloaded");
// A direct mp4 wins over the playlist.
const f = c.fanslyFile({ type: 2, mimetype: "video/mp4", locations: [], variants: [
  { mimetype: "application/vnd.apple.mpegurl", width: 1920, locations: loc("https://cdn3.fansly.com/v/m.m3u8", { Policy: "P" }) },
  { mimetype: "video/mp4", width: 720, locations: loc("https://cdn3.fansly.com/v/720.mp4") },
  { mimetype: "video/mp4", width: 1080, locations: loc("https://cdn3.fansly.com/v/1080.mp4") }] });
assert.deepStrictEqual(f, { kind: "video", media: "https://cdn3.fansly.com/v/1080.mp4", mime: "video/mp4" });
assert.strictEqual(c.fanslyFile({ type: 1, mimetype: "image/jpeg", locations: [] }), null);
console.log("collect_test.js: ok");
