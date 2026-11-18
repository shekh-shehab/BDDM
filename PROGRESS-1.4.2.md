# BDDM 1.4.2 progress

Baseline: 1.4.1 committed in local git (`git log`) before any change.

- [x] Part 1: Anikoto / megaplay.buzz 403 (referer, 403 retry, PNG-disguised HLS pieces, qualities)
- [x] Part 2: clean file names from the page title
- [x] Part 3: Send Anywhere left to the browser
- [x] Part 4: movielinkbd.tv: HLS playlists without ".m3u8" sent as image/jpeg (found by content, read from the player frame with its Origin/Referer); "#EXT-X-ERROR:forbidden" counts as a refusal
- [x] Version 1.4.2 everywhere (extension 1.4.2.1, see INSTALL.txt), tests: 80 pass
- [x] Build exe, extensions, installer, signed xpi, INSTALL.txt, BDDM-src.zip
