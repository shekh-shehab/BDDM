# BDDM 1.4.18 — item 24 (native HLS / m3u8 downloads)

HLS streams are downloaded by BDDM itself instead of only through yt-dlp:
master/media playlists, quality picker defaulting to highest, parallel
segments with retry and resume, AES-128, MPEG-TS and fMP4 (including
pieces hidden behind a picture), ffmpeg stream-copy to one MP4, temp
cleanup. Speed limits and auto-yield apply. The video window follows
Windows light/dark theme. The existing browser extension already offers
.m3u8 and movielinkbd-style playlists (with Referer/Origin/cookies/UA)
and is unchanged (1.4.15). yt-dlp remains the fallback.

Tests: 182 listed, 180 pass, 2 screenshot skips. go vet clean. Windows
exe is WINDOWS_GUI. A public mux.dev HLS master was fetched and parsed.
movielinkbd.tv was blocked by a Cloudflare challenge from this machine;
the .at homepage loaded but no real movie playlist was captured, so a
live movielinkbd download was not verified (a local look-alike test
passes).
