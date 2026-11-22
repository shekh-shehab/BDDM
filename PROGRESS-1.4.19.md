# BDDM 1.4.19 — item 70 (disguised HLS capture and auto-offer)

Firefox (and Chrome/Edge) now confirm movielinkbd-style playlists that are
sent as image/jpeg and require the player's Origin. Peek tries credentials
"omit" then "include" so CORS "*" works. Firefox reads the original 200
body with filterResponseData (pass-through, only candidate disguised
playlist requests — not YouTube, Facebook, files, Drive, or ordinary
.m3u8). A 403 refetch no longer drops a stream the tab already received.
When a playlist is confirmed on a site that does not already have its own
video-page flow, BDDM's download window opens automatically once per
stream. YouTube, Facebook and similar sites are unchanged (no new
auto-popup). The context menu works on pages with a detected stream.
Native HLS sends the captured Origin (or the player frame's origin when
Referer is empty) so the CDN returns 200.

The browser extension is 1.4.19 (a new signed Firefox xpi is required;
1.4.15 cannot be updated in place).

Tests: 187 listed, 185 pass, 2 screenshot skips. go vet clean. Windows
test binary compiles. A live probe of the user's movielinkbd.at page
fetched the disguised master (image/jpeg, HTTP 200 with Origin, 403
without), parsed a 720p variant, and downloaded a byte-range fMP4
segment through BDDM's native HLS client.
