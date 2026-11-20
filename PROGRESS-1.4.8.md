# BDDM 1.4.8 progress: bring native pickers in front of the confirm window

Baseline: 1.4.7 (commit fdb220b). Build ONLY this item. The v1.4.6 owner-HWND fix did not work on real Windows: IFileDialog lives in BDDM.exe, a different process from the Edge/Chrome --app window, and Show() is modal, so the dialog's own HWND was never brought forward. Activating the owner just before Show may have kept the owner on top.

- [x] Watcher goroutine during Show(): find the dialog's own window (#32770 / known title / owned by this process, new since Show started)
- [x] Raise that HWND (AllowSetForegroundWindow, AttachThreadInput, HWND_TOPMOST then HWND_NOTOPMOST, SetForegroundWindow, BringWindowToTop); retry ~1.5s until it is foreground
- [x] Do not re-activate the owner after the dialog is up; do not activate the owner immediately before Show
- [x] Same fix for the PowerShell fallback and the torrent file picker
- [x] Defensive: no crash, no change if the HWND is not found
- [x] Tests where feasible; go vet; full suite; Windows CGO_ENABLED=0 build; installer
- [x] Version 1.4.8; release notes must say this needs testing on real Windows
- [x] Push and publish only if complete; if in doubt, do not push

The common-item dialog and the WinForms fallback are raised by watching for a new #32770 window (or a known picker title) and bringing that window, not the Edge/Chrome owner, to the front. This cannot be verified on Linux.
