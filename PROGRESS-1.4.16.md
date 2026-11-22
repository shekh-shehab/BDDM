# BDDM 1.4.16 — item 67 (no console window)

## Cause
The v1.4.15 Windows build used `-ldflags="-s -w"` and omitted
`-H=windowsgui`. BDDM.exe was a console (WINDOWS_CUI) program. On Windows
11, the default terminal opens a visible tab titled "BDDM" when the
installer starts BDDM after an update (and also from a shortcut or
autostart). Hidden-console tricks do not stop that for a console-subsystem
exe.

## Fix
Build BDDM.exe as a GUI-subsystem app (`-H=windowsgui`) via `build-windows.sh`.
Startup messages go to the log (`%APPDATA%\BDDM\bddm.log`) instead of stdout.
The official release exe was checked: PE Subsystem = 2 (WINDOWS_GUI).
The browser extension is unchanged (1.4.15).

## Tests
CGO_ENABLED=0 go test: 159 listed, 157 pass, 2 screenshot skips
(TestSpeedLimitUIScreenshot, TestTorrentUIScreenshot). go vet clean.
Windows test binary compiles. Release BDDM.exe is WINDOWS_GUI.
