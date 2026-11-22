// Pure helpers for Firefox early download capture (webRequest). No browser
// APIs. Chrome/Edge do not use these to decide; they keep
// onDeterminingFilename. blob: and data: links are never intercepted.
(function (root) {
  const MIN_SIZE = 10 * 1024;
  const INTERCEPT_TYPES = { main_frame: true, sub_frame: true, object: true, other: true };

  function isBlobOrDataUrl(u) {
    return /^(blob|data):/i.test(u || "");
  }

  function headerValue(headers, name) {
    if (!headers) return "";
    const want = String(name || "").toLowerCase();
    for (const h of headers) {
      if (h && h.name && String(h.name).toLowerCase() === want) return h.value || "";
    }
    return "";
  }

  function mimeTypeOf(headers) {
    return headerValue(headers, "content-type").split(";")[0].trim().toLowerCase();
  }

  function contentLengthOf(headers) {
    const n = parseInt(headerValue(headers, "content-length"), 10);
    return Number.isFinite(n) && n >= 0 ? n : -1;
  }

  function isAttachmentDisposition(disp) {
    const d = disp || "";
    return /^\s*attachment\b/i.test(d) || /filename\s*\*?=/i.test(d);
  }

  function filenameFromContentDisposition(disp) {
    if (!disp) return "";
    const star = /filename\*\s*=\s*(?:UTF-8''|iso-8859-1'')?([^;]+)/i.exec(disp);
    if (star) {
      const raw = star[1].trim().replace(/^["']|["']$/g, "");
      try { return decodeURIComponent(raw); } catch (e) { return raw; }
    }
    const plain = /filename\s*=\s*("(?:\\.|[^"])*"|[^;]+)/i.exec(disp);
    if (!plain) return "";
    return plain[1].trim().replace(/^["']|["']$/g, "").replace(/\\"/g, '"');
  }

  // Types Firefox (and other browsers) would show in a tab, not save as a file.
  function isRenderableMime(ct) {
    if (!ct) return false;
    if (ct === "text/html" || ct === "application/xhtml+xml" || ct === "text/plain") return true;
    if (ct === "text/css" || ct === "text/javascript" || ct === "application/javascript" || ct === "application/x-javascript") return true;
    if (ct === "application/json" || ct === "application/xml" || ct === "text/xml" || ct === "image/svg+xml") return true;
    if (ct === "application/pdf") return true;
    if (ct.indexOf("image/") === 0) return true;
    if (ct.indexOf("audio/") === 0 || ct.indexOf("video/") === 0) return true;
    if (ct === "application/ogg") return true;
    return false;
  }

  function isSuccessfulDownloadStatus(code) {
    if (code == null || code === 0) return true;
    return (code >= 200 && code < 300);
  }

  // True when this response is a file Firefox would save (and BDDM should
  // take) rather than a page, a media stream, or a page-generated blob.
  function shouldInterceptFirefoxDownload(details) {
    if (!details || !details.url) return false;
    if (isBlobOrDataUrl(details.url)) return false;
    if (!/^https?:\/\//i.test(details.url)) return false;
    const method = String(details.method || "GET").toUpperCase();
    if (method !== "GET") return false;
    if (!isSuccessfulDownloadStatus(details.statusCode)) return false;
    const type = details.type || "";
    if (!INTERCEPT_TYPES[type]) return false;

    const disp = headerValue(details.responseHeaders, "content-disposition");
    const attachment = isAttachmentDisposition(disp);
    const ct = mimeTypeOf(details.responseHeaders);
    const size = contentLengthOf(details.responseHeaders);
    if (size >= 0 && size < MIN_SIZE) return false;

    if (attachment) return true;
    if (type === "main_frame" || type === "sub_frame") {
      if (!ct || isRenderableMime(ct)) return false;
      return true;
    }
    if ((type === "other" || type === "object") && ct && !isRenderableMime(ct)) return true;
    return false;
  }

  const api = {
    MIN_SIZE,
    isBlobOrDataUrl,
    headerValue,
    mimeTypeOf,
    contentLengthOf,
    isAttachmentDisposition,
    filenameFromContentDisposition,
    isRenderableMime,
    isSuccessfulDownloadStatus,
    shouldInterceptFirefoxDownload,
  };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  else {
    for (const k of Object.keys(api)) root[k] = api[k];
  }
})(typeof self !== "undefined" ? self : this);
