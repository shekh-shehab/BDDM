# BDDM 1.4.14 progress: all windows and pickers open in front (list item 13)

Baseline: 1.4.13 (commit 73982ce). Built ONLY this item. Did not start item 12.

- [x] Folder/file pickers stay in front of the BDDM window they were opened from (confirm window included) for as long as they are open; Windows 11 Explorer/XAML pickers are recognized by title
- [x] New BDDM app windows keep focus briefly after they appear, same method as the Add torrent window
- [x] Tests; go vet; full suite (150 listed, 148 pass, 2 screenshot skips); Windows CGO_ENABLED=0 test compile and build; installer
- [x] Version 1.4.14
- [x] Did not start item 12 (Firefox capture-before-download)

Focus behavior cannot be fully verified on Linux; the user should check on real Windows.
