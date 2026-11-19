# BDDM 1.4.7 progress: speed limits (list item 4)

Baseline: 1.4.6 (commit 5cfda20). Build ONLY item 4 (speed limits). Do not touch other list items, the tray, or the extension.

- [x] Settings: global regular-download limit, torrent download limit, torrent upload limit (mockup 17)
- [x] Per-download speed-limit popover that overrides the global limit (mockup 18)
- [x] Light and dark via prefers-color-scheme; compact popups; UI in English; name BDDM
- [x] Persist settings and per-download limits across restarts
- [x] Token-bucket limiter for HTTP multi-part downloads; anacrolix rate limiters for torrents
- [x] Limit changes apply live to running downloads
- [x] Tests; go vet; full suite; Windows CGO_ENABLED=0 build; installer
- [x] Version 1.4.7; push and publish only if truly complete

Slider scale (0–10): 25 KB/s, 50 KB/s, 100 KB/s, 200 KB/s, 2 MB/s, 5 MB/s, 10 MB/s, 20 MB/s, 50 MB/s, 100 MB/s, Unlimited. These match the labeled values in mockups 17 and 18. 0 bytes/s in settings.json means unlimited. A per-download override of 0 means that job is unlimited even if a global cap is set; Off clears the override so the job uses Settings again.

Screenshots (not committed): speed-limit-settings-light.png, speed-limit-settings-dark.png, speed-limit-popover-light.png, speed-limit-popover-dark.png under /workspace/bddm/. Set BDDM_SHOT=1 to regenerate.
