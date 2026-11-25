# BDDM 1.4.19 — items 70 and 71

Item 70: Firefox (and Chrome/Edge) now confirm movielinkbd-style playlists
that are sent as image/jpeg and require the player's Origin. Peek tries
credentials "omit" then "include" so CORS "*" works. Firefox reads the
original 200 body with filterResponseData (pass-through, only candidate
disguised playlist requests — not YouTube, Facebook, files, Drive, or
ordinary .m3u8). A 403 refetch no longer drops a stream the tab already
received. When a playlist is confirmed on a site that does not already
have its own video-page flow, BDDM's download window opens automatically
once per stream. YouTube, Facebook and similar sites are unchanged (no
new auto-popup). The context menu works on pages with a detected stream.
Native HLS sends the captured Origin (or the player frame's origin when
Referer is empty) so the CDN returns 200.

Item 71: A small "Download with BDDM" button is shown on the corner of
playing or visible videos on every site, including YouTube, Facebook and
Instagram. It is hidden on very short clips (under 30 seconds), tiny
players and known ad containers, follows the hovered or active video in
a scrolling feed without flicker, stays out of the site's controls, and
works in iframes and fullscreen. Clicking it opens the existing compact
English video window (preview thumbnail from the poster or a captured
frame, title, quality choices from the detected stream or yt-dlp, size
when known, Download). The window follows Windows light or dark theme.
The item 70 auto-popup rule is unchanged: YouTube/Facebook do not get an
automatic window, only the button.

The browser extension is 1.4.19 (a new signed Firefox xpi is required;
1.4.15 cannot be updated in place).

Tests: 190 listed, 188 pass, 2 screenshot skips. go vet clean. Windows
test binary compiles.
