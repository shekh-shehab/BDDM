# BDDM 1.4.3 progress: update notice only (step 1)

Baseline: commit 54215d4 (1.4.2 + .gitignore/README). Do not install anything automatically.

- [x] updater.go: check releases/latest (no token), semver compare (leading v ignored), skip draft/prerelease, quiet errors when offline or rate-limited
- [x] Check on start and about every 6 hours; setting "Check for updates automatically" (on by default); "Check for updates" button in Settings
- [x] Main-window banner "BDDM vX.Y.Z is available" with View release / Later; one tray/notification per version; Later hides it until the next start
- [x] Nothing is downloaded or installed
- [x] Tests (version compare, release JSON, mocked HTTP) + full suite: 88 pass
- [x] Version 1.4.3 (app, About, installer, INSTALL.txt). Extension left at 1.4.2.1; xpi not re-signed
- [x] Build BDDM.exe and BDDM-Setup.exe
