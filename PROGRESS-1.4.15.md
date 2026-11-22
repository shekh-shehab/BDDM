# BDDM 1.4.15 — item 12 (Firefox capture)

## Goal
In Firefox, catch downloads before Firefox's own downloader saves them.
Leave blob:/data: (Send Anywhere-style) with the browser. Keep Chrome/Edge
unchanged. English UI only.

## Approach
Firefox has no downloads.onDeterminingFilename. Register a Firefox-only
blocking webRequest.onHeadersReceived listener (webRequestBlocking in the
Firefox manifest only). Cancel the response only after BDDM actually takes
the file. onCreated remains the fallback. Pure helpers in
extension/firefox_capture.js, tested in Node and via go test.

## Result
Complete. App and extension version 1.4.15. Firefox add-on signed as
unlisted 1.4.15.

Tests (CGO_ENABLED=0 go test): 153 listed, 151 pass, 2 screenshot skips
(TestSpeedLimitUIScreenshot, TestTorrentUIScreenshot) as before.
go vet clean. Windows test binary compiles (GOOS=windows CGO_ENABLED=0
go test -c).
