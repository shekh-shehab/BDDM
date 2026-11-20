# BDDM 1.4.10 progress: keep torrent resume data out of the download folder (list item 7)

Baseline: 1.4.9 (commit ab31e52). Built ONLY this item. LastTorrentDir, speed limits, window focus, the tray, and the extension were not changed.

- [x] Piece-completion store under the BDDM data folder (configDir/torrents/<infohash>/.torrent.bolt.db), CGO_ENABLED=0 (anacrolix NewBoltPieceCompletion)
- [x] Do not create .torrent.bolt.db in the user's download folder
- [x] Migrate an old leftover <download folder>/.torrent.bolt.db (copy, then delete the leftover)
- [x] Removing a torrent always deletes its completion data and stored .torrent/metainfo in the data folder; downloaded files are deleted only when the existing remove-with-files option is used
- [x] Seeding and resuming still work after a restart
- [x] Tests; go vet; full suite (129 listed, 127 pass, 2 screenshot skips); Windows CGO_ENABLED=0 test compile and build; installer
- [x] Version 1.4.10

Each started torrent gets its own bolt store under the data folder so several torrents can share one download folder without locking a database there. The default torrent client storage no longer uses storage.NewFile (which would write a bolt database under torrent-work).
