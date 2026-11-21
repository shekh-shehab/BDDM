# BDDM 1.4.13 progress: clean shutdown before update (item 10) and torrent resume after restart (item 11)

Baseline: 1.4.12 (commit 162a9c1). Built ONLY items 10 and 11, released together. Did not start item 12 or 13.

- [x] Item 10: before an update, stop transfers and save state, then start the installer; the installer asks BDDM to quit and waits for a graceful exit, and only force-kills as a last resort (avoids "BDDM stopped working")
- [x] Item 11: torrents that were downloading or seeding resume after quit, restart, or update; user-paused torrents stay paused (including when ResumeOnStart is off)
- [x] Tests; go vet; full suite (148 listed, 146 pass, 2 screenshot skips); Windows CGO_ENABLED=0 test compile and build; installer
- [x] Version 1.4.13
- [x] Did not start item 12 (Firefox capture-before-download) or item 13 (all windows open in front)
- [x] Extension version unchanged (still 1.4.2.1)
