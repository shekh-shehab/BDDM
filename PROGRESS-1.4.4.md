# BDDM 1.4.4 progress: Install on click

Baseline: 1.4.3 (commit 6c52481). Do not download or install until the user presses Install.

- [x] Install button on the banner and in Settings
- [x] Download BDDM-Setup.exe from the release, with progress and Cancel
- [x] Verify size; verify SHA-256 when a .sha256 asset is present
- [x] If downloads are running, ask first; then pause, save, silent upgrade (/S /run), restart, exit
- [x] NSIS: silent upgrade keeps optional choices; /run starts BDDM again
- [x] Clear English errors and a Try again button
- [x] Tests: asset selection, size/hash, mocked download (including cancel). Full suite: 96 pass
- [x] Version 1.4.4, build exe + installer, publish BDDM-Setup.exe.sha256
- [x] Do not re-sign the extension (still 1.4.2.1)
