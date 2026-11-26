// Node tests for the browser-fallback helpers. Run: node handback_test.js
"use strict";
const t = require("./handback");
let failed = 0;
function eq(name, got, want) {
  if (got !== want) {
    console.error("FAIL " + name + ": got " + JSON.stringify(got) + " want " + JSON.stringify(want));
    failed++;
  }
}
const now = 1e12, ttl = 7 * 24 * 3600e3, fresh = { added: now - 1000 };

// A site that blocks BDDM goes back to the browser.
eq("blocked 403", t.decideHandBack({ status: "Error", error: "The server refused the download (HTTP 403 Forbidden).", handBack: true, blocked: true }, fresh, now, ttl), "handback");
eq("web page instead of file", t.decideHandBack({ status: "Error", error: "The site sent a web page instead of the file", handBack: true, blocked: true }, fresh, now, ttl), "handback");
// An older BDDM sends no handBack answer: every ordinary failure goes back (as before).
eq("old BDDM sends no handBack", t.decideHandBack({ status: "Error", error: "download stalled" }, fresh, now, ttl), "handback");
// A problem on this computer is not handed back.
eq("disk full", t.decideHandBack({ status: "Error", error: "There is not enough space", handBack: false }, fresh, now, ttl), "failed");
eq("view only", t.decideHandBack({ status: "Error", error: "view only", handBack: true }, fresh, now, ttl), "viewonly");
eq("expired link", t.decideHandBack({ status: "Error", error: "The download link has expired. Click ...", handBack: true }, fresh, now, ttl), "expired");
// Handed back already: never again (no loop).
eq("already handed back", t.decideHandBack({ status: "Error", error: "x", handBack: true, handedBack: true }, fresh, now, ttl), "forget");
eq("running", t.decideHandBack({ status: "Downloading" }, fresh, now, ttl), "none");
eq("done", t.decideHandBack({ status: "Done" }, fresh, now, ttl), "forget");
eq("deleted in BDDM", t.decideHandBack({ status: "Paused", removed: true }, fresh, now, ttl), "forget");
eq("too old", t.decideHandBack({ status: "Queued" }, { added: now - ttl - 1 }, now, ttl), "forget");

const a = t.handBackNotice("movie.zip", true, "HTTP 403");
eq("blocked title", a.title, "Site blocked BDDM - handed back to browser");
eq("blocked body names the file", a.message.indexOf("movie.zip") === 0, true);
const b = t.handBackNotice("movie.zip", false, "download stalled");
eq("other title", b.title, "BDDM could not download this file");
eq("other body has reason", b.message.indexOf("download stalled") > 0, true);
eq("quick polls are soon", t.QUICK_POLL_MS[0] <= 2000 && t.QUICK_POLL_MS.length >= 3, true);

if (failed) process.exit(1);
console.log("handback_test ok");
