# BDDM 1.4.6 progress: bring windows and dialogs to the front

Baseline: 1.4.5 (commit c7471e2). Small fix: everything BDDM opens was appearing behind other windows.

- [x] Central helper to put a newly opened BDDM window or dialog in front with focus
- [x] Folder Browse (Settings, confirm, add torrent, video, Drive)
- [x] Add torrent file open dialog
- [x] Main window (second launch, opening from the background / Start menu)
- [x] Popup windows (add torrent, confirm, video, Drive folder)
- [x] Check tray/Open, update "View release", Explorer "Open folder"
- [x] Tests where feasible; full suite green
- [x] Version 1.4.6, build exe + installer + sha256; same assets as 1.4.5
- [x] Push and publish only if the fix is solid

There is no systray "Open" menu; running in the background uses `-nowindow`, and a second launch or the extension's POST /ext/show calls openAppWindow (now reuses and focuses the existing main window). The update banner's "View release" uses openURL; Install runs a silent setup which then restarts BDDM and opens the main window. Explorer "Open folder" is covered too.
