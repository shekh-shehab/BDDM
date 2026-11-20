# BDDM 1.4.11 progress: new dark-theme icon (list item 8)

Baseline: 1.4.10 (commit bca56f8). Built ONLY this item. Item 9 (notification app identity) was not changed. There is no persistent tray icon in BDDM, and no separate toast icon file; those were left alone.

- [x] Crop the user's artwork tightly to the rounded square; transparent outside corners (PNG with alpha)
- [x] Full icon with "BDDM" text for large sizes (256, 128, 96, 64)
- [x] Symbol-only (shield and symbols, no text, scaled up) for small sizes (48, 32, 24, 16)
- [x] Multi-size .ico used for the exe resource, installer and shortcuts
- [x] UI favicon / app-window icon, header and About logo
- [x] Extension icons: symbol for 16/32/48, full icon for 128
- [x] assets/icon.png and README logo
- [x] Previews under /workspace/bddm/icon-preview-*.png (not committed)
- [x] Tests; go vet; full suite (132 listed, 130 pass, 2 screenshot skips); Windows CGO_ENABLED=0 test compile and build; installer
- [x] Version 1.4.11
- [x] Did not sign a new Firefox xpi (release keeps 1.4.2.1)

Regenerate icons with: python3 assets/make_icons.py
Regenerate Windows resources with: go-winres make --in winres/winres.json --arch amd64,386,arm64 --out rsrc
