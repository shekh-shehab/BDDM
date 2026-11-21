# BDDM 1.4.12 progress: Windows notification identity (list item 9)

Baseline: 1.4.11 (commit 4a6605b). Built ONLY this item. Did not touch items 10-13.

- [x] Give BDDM its own AppUserModelID (`BDDM.DownloadManager`)
- [x] Set AUMID on Start-menu and desktop shortcuts (installer creates/recreates; also stamped when BDDM starts or is run with `-register`)
- [x] Make toast code use BDDM's AUMID instead of PowerShell's (keep PowerShell and a balloon with the BDDM icon as fallbacks)
- [x] Set the same AUMID on Edge/Chrome `--app` windows, with relaunch properties so a new taskbar pin groups under BDDM
- [x] Tests; go vet; full suite (141 listed, 139 pass, 2 screenshot skips); Windows CGO_ENABLED=0 test compile and build; installer
- [x] Version 1.4.12
- [x] Did not touch items 10-13 (update clean shutdown, torrent resume after restart, Firefox capture-before-download, all windows open in front)

Existing taskbar pins of the Edge/Chrome app window may still show the old blue arrow until the user unpins and pins BDDM again from the Start menu (or signs out / restarts so Windows refreshes the icon cache).
