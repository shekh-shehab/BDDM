# BDDM 1.4.9 progress: remember the last Add torrent folder (list item 6)

Baseline: 1.4.8 (commit 90a6460). Built ONLY this item. The bolt.db location (item 7) was not changed.

- [x] LastTorrentDir setting, persisted in settings.json
- [x] Add torrent window (magnet and .torrent file) pre-fills the last folder the user chose
- [x] The user can still pick a different folder; that becomes the new remembered folder
- [x] If the saved folder no longer exists, fall back to the current default
- [x] Tests; go vet; full suite (123 listed, 121 pass, 2 screenshot skips); Windows CGO_ENABLED=0 test compile and build; installer
- [x] Version 1.4.9

StartTorrent saves the chosen folder as LastTorrentDir after the folder is accepted. The next magnet or .torrent add pre-fills that path (via torrentSaveDir). If the saved folder is missing or is not an absolute directory, the usual default is used (category folder for a single-file .torrent, otherwise DownloadDir).
