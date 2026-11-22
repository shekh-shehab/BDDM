# BDDM 1.4.17 — item 68 (yield bandwidth to other apps)

When other programs or the browser use the network, BDDM automatically
uses less bandwidth, then ramps back to full speed when that traffic
stops. Applies to regular HTTP, video (including yt-dlp via a local
proxy), Google Drive, and torrent download/upload. Never drops to zero.
Settings toggle "Slow down downloads while I use the internet" is on by
default. A "Saving bandwidth" note shows in the window while yielding.
Existing manual speed limits were left in place and still apply as a cap.

Tests: 172 listed, 170 pass, 2 screenshot skips. go vet clean. Windows
exe is WINDOWS_GUI. Extension unchanged (1.4.15).
