# BDDM 1.4.5 progress: basic torrent support (step 3)

Baseline: 1.4.4 (commit e589981). Do not add file selection, speed limits, or magnet handoff from the browser.

- [x] Integrate a pure-Go torrent library (anacrolix/torrent), Windows-friendly
- [x] Add magnet links via the existing paste/Add URL flow
- [x] Add a .torrent file (open file)
- [x] Download into the normal BDDM folder logic
- [x] Main list: name, progress, size, peers, down/up speed, status (Downloading, Paused, Seeding, Error)
- [x] Pause, resume, remove (ask whether to also delete files)
- [x] Keep seeding after completion while BDDM runs (including tray); no auto-stop by ratio or time
- [x] Persist torrent state in the config dir; resume or keep seeding on restart
- [x] UI styled after the approved mockups, within the current window
- [x] Tests: magnet parsing, state persistence, seeding after completion; local swarm if feasible; full suite green
- [x] Version 1.4.5, build exe + installer + sha256; extension unchanged (1.4.2.1)
- [x] Do not push unless complete and all tests pass
