// node collect_test.js: checks collect.js helpers.
const assert = require("assert");
const { bestFromSrcset, POST_RE } = require("./collect.js");
assert.strictEqual(bestFromSrcset("a.jpg 320w, b.jpg 1080w, c.jpg 640w"), "b.jpg");
assert.strictEqual(bestFromSrcset("x.jpg"), "x.jpg");
assert.strictEqual(bestFromSrcset(""), "");
for (const p of ["/p/Cabc123/", "/reel/Cxyz/", "/@a/video/7312", "/post/9", "/status/123", "/watch/abc"]) assert.ok(POST_RE.test(p), p);
for (const p of ["/", "/explore/", "/accounts/login/", "/@soloshinee"]) assert.ok(!POST_RE.test(p), p);
console.log("collect_test.js: ok");
