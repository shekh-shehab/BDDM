// Node tests for Firefox early-capture helpers. Run: node firefox_capture_test.js
"use strict";
const t = require("./firefox_capture");
let failed = 0;
function eq(name, got, want) {
  if (got !== want) {
    console.error("FAIL " + name + ": got " + JSON.stringify(got) + " want " + JSON.stringify(want));
    failed++;
  }
}

eq("blob https is skipped", t.isBlobOrDataUrl("blob:https://send-anywhere.com/abc"), true);
eq("data url is skipped", t.isBlobOrDataUrl("data:application/zip;base64,AAA"), true);
eq("http is not blob", t.isBlobOrDataUrl("https://example.com/a.zip"), false);
eq("empty is not blob", t.isBlobOrDataUrl(""), false);

eq("attachment disposition", t.isAttachmentDisposition('attachment; filename="a.zip"'), true);
eq("filename= counts as a download", t.isAttachmentDisposition('inline; filename="a.zip"'), true);
eq("plain inline is not", t.isAttachmentDisposition("inline"), false);
eq("empty disp", t.isAttachmentDisposition(""), false);

eq("filename quoted", t.filenameFromContentDisposition('attachment; filename="report.bin"'), "report.bin");
eq("filename star utf-8", t.filenameFromContentDisposition("attachment; filename*=UTF-8''na%C3%AFve.bin"), "naïve.bin");
eq("no filename", t.filenameFromContentDisposition("attachment"), "");

eq("html is renderable", t.isRenderableMime("text/html"), true);
eq("pdf is renderable", t.isRenderableMime("application/pdf"), true);
eq("png is renderable", t.isRenderableMime("image/png"), true);
eq("mp4 is renderable", t.isRenderableMime("video/mp4"), true);
eq("zip is not renderable", t.isRenderableMime("application/zip"), false);
eq("octet-stream is not renderable", t.isRenderableMime("application/octet-stream"), false);

function d(over) {
  return Object.assign({
    url: "https://cdn.example.com/file.zip",
    method: "GET",
    type: "main_frame",
    statusCode: 200,
    responseHeaders: [
      { name: "Content-Type", value: "application/zip" },
      { name: "Content-Length", value: "123456" },
    ],
  }, over);
}

eq("zip navigation is intercepted", t.shouldInterceptFirefoxDownload(d()), true);
eq("attachment is intercepted", t.shouldInterceptFirefoxDownload(d({
  responseHeaders: [
    { name: "Content-Type", value: "application/octet-stream" },
    { name: "Content-Disposition", value: 'attachment; filename="setup.exe"' },
    { name: "Content-Length", value: "20000" },
  ],
})), true);
eq("html page is not intercepted", t.shouldInterceptFirefoxDownload(d({
  url: "https://example.com/",
  responseHeaders: [{ name: "Content-Type", value: "text/html; charset=utf-8" }],
})), false);
eq("pdf in the Firefox viewer is not intercepted", t.shouldInterceptFirefoxDownload(d({
  url: "https://example.com/doc.pdf",
  responseHeaders: [{ name: "Content-Type", value: "application/pdf" }, { name: "Content-Length", value: "80000" }],
})), false);
eq("pdf attachment is intercepted", t.shouldInterceptFirefoxDownload(d({
  url: "https://example.com/doc.pdf",
  responseHeaders: [
    { name: "Content-Type", value: "application/pdf" },
    { name: "Content-Disposition", value: "attachment; filename=doc.pdf" },
    { name: "Content-Length", value: "80000" },
  ],
})), true);
eq("image navigation is not intercepted", t.shouldInterceptFirefoxDownload(d({
  url: "https://example.com/pic.png",
  responseHeaders: [{ name: "Content-Type", value: "image/png" }],
})), false);
eq("video navigation is not intercepted", t.shouldInterceptFirefoxDownload(d({
  url: "https://example.com/clip.mp4",
  responseHeaders: [{ name: "Content-Type", value: "video/mp4" }],
})), false);
eq("script is not intercepted", t.shouldInterceptFirefoxDownload(d({ type: "script", responseHeaders: [{ name: "Content-Type", value: "application/javascript" }] })), false);
eq("stylesheet is not intercepted", t.shouldInterceptFirefoxDownload(d({ type: "stylesheet" })), false);
eq("media stream is not intercepted", t.shouldInterceptFirefoxDownload(d({ type: "media" })), false);
eq("xhr/fetch stays with the page (blob/Send Anywhere style)", t.shouldInterceptFirefoxDownload(d({
  type: "xmlhttprequest",
  responseHeaders: [
    { name: "Content-Type", value: "application/octet-stream" },
    { name: "Content-Disposition", value: "attachment; filename=file.bin" },
    { name: "Content-Length", value: "50000" },
  ],
})), false);
eq("blob url is never intercepted", t.shouldInterceptFirefoxDownload(d({ url: "blob:https://send-anywhere.com/111" })), false);
eq("data url is never intercepted", t.shouldInterceptFirefoxDownload(d({ url: "data:application/zip;base64,QQ==" })), false);
eq("POST body downloads stay with the browser", t.shouldInterceptFirefoxDownload(d({ method: "POST" })), false);
eq("redirect is not intercepted", t.shouldInterceptFirefoxDownload(d({ statusCode: 302 })), false);
eq("404 is not intercepted", t.shouldInterceptFirefoxDownload(d({ statusCode: 404 })), false);
eq("tiny file stays in the browser", t.shouldInterceptFirefoxDownload(d({
  responseHeaders: [
    { name: "Content-Type", value: "application/zip" },
    { name: "Content-Length", value: "100" },
  ],
})), false);
eq("unknown size zip is intercepted", t.shouldInterceptFirefoxDownload(d({
  responseHeaders: [{ name: "Content-Type", value: "application/zip" }],
})), true);
eq("main_frame without a type (likely HTML) is not intercepted", t.shouldInterceptFirefoxDownload(d({
  url: "https://example.com/page",
  responseHeaders: [],
})), false);
eq("type=other with a zip type is intercepted", t.shouldInterceptFirefoxDownload(d({ type: "other" })), true);
eq("type=other html is not intercepted", t.shouldInterceptFirefoxDownload(d({
  type: "other",
  responseHeaders: [{ name: "Content-Type", value: "text/html" }],
})), false);
eq("missing details", t.shouldInterceptFirefoxDownload(null), false);
eq("ftp is not intercepted", t.shouldInterceptFirefoxDownload(d({ url: "ftp://example.com/a.zip" })), false);
eq("file: is not intercepted", t.shouldInterceptFirefoxDownload(d({ url: "file:///tmp/a.zip" })), false);

if (failed) {
  console.error(failed + " failed");
  process.exit(1);
}
console.log("ok");
