// BDDM: browser fallback helpers (item 18). Pure functions only (no browser
// APIs, no listeners at load) so Node tests can require this file the same
// way they require firefox_capture.js.
//
// When a site will not let BDDM download a file (sign-in or cookies needed,
// a security / anti-bot check, HTTP 401/403/407, a web page instead of the
// file, ...) the download is given back to the browser, which downloads it
// itself. background.js asks BDDM for the state of the downloads it sent and
// uses decideHandBack() to see what to do with each one.
(function (root) {
  "use strict";

  // Checks soon after a download was sent: a blocked site fails within
  // seconds, and the 30 second alarm alone would leave the user waiting.
  const QUICK_POLL_MS = [1500, 4000, 9000, 20000];

  // job: one entry of BDDM's /ext/status answer ({status, error, handBack,
  // blocked, removed, ...}). info: what the extension saved when it sent the
  // download ({added}). Returns what to do:
  //   "handback" - give it to the browser
  //   "viewonly" - Google Drive owner turned off downloading; nothing helps
  //   "expired"  - the old link is dead; the user clicks it again
  //   "failed"   - a problem on this computer (disk, folder ...): the browser
  //                cannot fix it either, so only tell the user
  //   "forget"   - finished, deleted in BDDM, handed back already or too old
  //   "none"     - still running; look again later
  function decideHandBack(job, info, now, ttl) {
    const status = job && job.status;
    const err = (job && job.error) || "";
    if (status === "Error") {
      if (job.handedBack) return "forget";
      if (/view only/i.test(err)) return "viewonly";
      if (/link has expired/i.test(err)) return "expired";
      // BDDM says whether the browser can help. An older BDDM sends no
      // answer: then every other failure goes back to the browser.
      if (job.handBack === false) return "failed";
      return "handback";
    }
    if (status === "Done" || (job && job.removed)) return "forget";
    if (info && info.added && now - info.added > ttl) return "forget";
    return "none";
  }

  // The notification shown when a download goes back to the browser.
  function handBackNotice(name, blocked, reason) {
    const what = name || "This download";
    if (blocked) {
      return {
        title: "Site blocked BDDM - handed back to browser",
        message: what + ": the site did not let BDDM download it. Your browser is downloading it itself.",
      };
    }
    return {
      title: "BDDM could not download this file",
      message: what + (reason ? " (" + reason + ")" : "") + ". Your browser is downloading it instead.",
    };
  }

  const api = { QUICK_POLL_MS, decideHandBack, handBackNotice };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  else {
    for (const k of Object.keys(api)) root[k] = api[k];
  }
})(typeof self !== "undefined" ? self : this);
