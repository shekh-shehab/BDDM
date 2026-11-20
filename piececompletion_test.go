package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

func torrentInfoHash(t *testing.T, mi *metainfo.MetaInfo) string {
	t.Helper()
	return mi.HashInfoBytes().HexString()
}

func dataDirCompletionPath(cfg, hash string) string {
	return filepath.Join(cfg, "torrents", hash, torrentPieceDBName)
}

func writeLegacyPieceDB(t *testing.T, dir string, mi *metainfo.MetaInfo) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	pc, err := storage.NewBoltPieceCompletion(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		pc.Close()
		t.Fatal(err)
	}
	ih := mi.HashInfoBytes()
	for i := 0; i < info.NumPieces(); i++ {
		if err := pc.Set(metainfo.PieceKey{InfoHash: ih, Index: i}, g.Some(true)); err != nil {
			pc.Close()
			t.Fatal(err)
		}
	}
	if err := pc.Close(); err != nil {
		t.Fatal(err)
	}
	if !fileExists(filepath.Join(dir, torrentPieceDBName)) {
		t.Fatal("legacy piece-completion file was not created")
	}
}

func TestTorrentPieceCompletionStaysInDataDir(t *testing.T) {
	m, dl := torrentTestManager(t)
	meta, mi, _ := writeTestTorrent(t, t.TempDir(), "keep-db.bin", []byte("piece-completion-payload-aaaa"))
	hash := torrentInfoHash(t, mi)

	job, err := m.AddTorrentFile(meta, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartTorrent(job.ID, dl); err != nil {
		t.Fatal(err)
	}

	meta2, mi2, _ := writeTestTorrent(t, t.TempDir(), "keep-db-2.bin", []byte("piece-completion-payload-bbbb"))
	hash2 := torrentInfoHash(t, mi2)
	job2, err := m.AddTorrentFile(meta2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartTorrent(job2.ID, dl); err != nil {
		t.Fatal(err)
	}

	if fileExists(filepath.Join(dl, torrentPieceDBName)) {
		t.Fatal("piece-completion database was created in the download folder")
	}
	if !fileExists(dataDirCompletionPath(m.configDir, hash)) {
		t.Fatalf("missing completion file for first torrent in the data folder")
	}
	if !fileExists(dataDirCompletionPath(m.configDir, hash2)) {
		t.Fatalf("missing completion file for second torrent in the data folder")
	}

	if err := m.Pause(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Pause(job2.ID); err != nil {
		t.Fatal(err)
	}
	waitTorrentStatus(t, m, job.ID, StatusPaused, 5*time.Second)
	waitTorrentStatus(t, m, job2.ID, StatusPaused, 5*time.Second)
}

func TestOldPieceCompletionMigratedFromDownloadDir(t *testing.T) {
	m, dl := torrentTestManager(t)
	meta, mi, _ := writeTestTorrent(t, t.TempDir(), "legacy.bin", []byte("legacy-bolt-payload-cccc"))
	hash := torrentInfoHash(t, mi)
	writeLegacyPieceDB(t, dl, mi)

	job, err := m.AddTorrentFile(meta, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartTorrent(job.ID, dl); err != nil {
		t.Fatal(err)
	}
	if err := m.Pause(job.ID); err != nil {
		t.Fatal(err)
	}
	waitTorrentStatus(t, m, job.ID, StatusPaused, 5*time.Second)

	if fileExists(filepath.Join(dl, torrentPieceDBName)) {
		t.Fatal("leftover .torrent.bolt.db was not removed from the download folder")
	}
	if !fileExists(dataDirCompletionPath(m.configDir, hash)) {
		t.Fatal("legacy completion data was not copied into the BDDM data folder")
	}
}

func TestRemoveTorrentDeletesResumeDataKeepsFiles(t *testing.T) {
	data := []byte("remove-resume-payload-dddd-and-more")
	tmp := t.TempDir()
	meta, mi, srcDir := writeTestTorrent(t, tmp, "keep-file.bin", data)
	seeder := startTestSeeder(t, mi, srcDir)
	hash := torrentInfoHash(t, mi)

	m, dl := torrentTestManager(t)
	job, err := m.AddTorrentFile(meta, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartTorrent(job.ID, dl); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := m.addTorrentClientPeer(job.ID, seeder); err != nil {
		t.Fatal(err)
	}
	waitTorrentStatus(t, m, job.ID, StatusSeeding, 20*time.Second)
	gotPath := filepath.Join(dl, "keep-file.bin")
	if !fileExists(gotPath) {
		t.Fatal("downloaded file missing before remove")
	}
	metaPath := m.metaPath(hash + ".torrent")
	if !fileExists(metaPath) {
		t.Fatal("saved .torrent missing before remove")
	}

	if err := m.Delete(job.ID, false); err != nil {
		t.Fatal(err)
	}
	if !fileExists(gotPath) {
		t.Fatal("removing a torrent without deleting files removed the downloaded file")
	}
	if fileExists(metaPath) {
		t.Fatal("saved .torrent was left in the data folder after remove")
	}
	if fileExists(m.pieceCompletionDir(hash)) || fileExists(dataDirCompletionPath(m.configDir, hash)) {
		t.Fatal("completion data was left in the data folder after remove")
	}
	if fileExists(filepath.Join(dl, torrentPieceDBName)) {
		t.Fatal("leftover .torrent.bolt.db remains in the download folder after remove")
	}
}

func TestRemoveTorrentWithFilesDeletesDownload(t *testing.T) {
	m, dl := torrentTestManager(t)
	meta, mi, _ := writeTestTorrent(t, t.TempDir(), "also-delete.bin", []byte("also-delete-payload-eeee"))
	hash := torrentInfoHash(t, mi)
	job, err := m.AddTorrentFile(meta, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartTorrent(job.ID, dl); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(job.ID, true); err != nil {
		t.Fatal(err)
	}
	// The file may never have finished, but any created name and resume data must be gone.
	if fileExists(filepath.Join(dl, "also-delete.bin")) || fileExists(filepath.Join(dl, "also-delete.bin.part")) {
		t.Fatal("remove-with-files left the downloaded file behind")
	}
	if fileExists(m.metaPath(hash+".torrent")) || fileExists(m.pieceCompletionDir(hash)) {
		t.Fatal("remove-with-files left torrent resume data in the data folder")
	}
}

func TestCancelPendingTorrentDeletesSavedMeta(t *testing.T) {
	m, _ := torrentTestManager(t)
	meta, mi, _ := writeTestTorrent(t, t.TempDir(), "cancel-me.bin", []byte("cancel-pending-payload-ffff"))
	hash := torrentInfoHash(t, mi)
	job, err := m.AddTorrentFile(meta, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !fileExists(m.metaPath(hash + ".torrent")) {
		t.Fatal("expected a saved .torrent after adding the file")
	}
	if err := m.CancelTorrent(job.ID); err != nil {
		t.Fatal(err)
	}
	if fileExists(m.metaPath(hash + ".torrent")) {
		t.Fatal("saved .torrent was left after cancelling the add window")
	}
	if fileExists(m.pieceCompletionDir(hash)) {
		t.Fatal("completion dir was created for a cancelled torrent")
	}
}

func TestTorrentResumeAfterRestartKeepsCompletionInDataDir(t *testing.T) {
	data := []byte("resume-data-dir-payload-gggg-and-more")
	tmp := t.TempDir()
	meta, mi, srcDir := writeTestTorrent(t, tmp, "resume-db.bin", data)
	seeder := startTestSeeder(t, mi, srcDir)
	hash := torrentInfoHash(t, mi)

	m, dl := torrentTestManager(t)
	job, err := m.AddTorrentFile(meta, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartTorrent(job.ID, dl); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := m.addTorrentClientPeer(job.ID, seeder); err != nil {
		t.Fatal(err)
	}
	waitTorrentStatus(t, m, job.ID, StatusSeeding, 20*time.Second)

	cfg := m.configDir
	m.Shutdown()
	if fileExists(filepath.Join(dl, torrentPieceDBName)) {
		t.Fatal("piece-completion database appeared in the download folder after shutdown")
	}
	if !fileExists(dataDirCompletionPath(cfg, hash)) {
		t.Fatal("completion file missing from the data folder after shutdown")
	}

	// Pretend an older build left the database in the download folder.
	legacy := filepath.Join(dl, torrentPieceDBName)
	if err := os.Rename(dataDirCompletionPath(cfg, hash), legacy); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(filepath.Join(cfg, "torrents", hash))

	m2, err := NewManager(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Shutdown()
	m2.tLoopback = true
	s := m2.Settings()
	s.DownloadDir = dl
	if _, err := m2.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	m2.mu.Lock()
	m2.restoreTorrentsLocked()
	m2.mu.Unlock()

	view := waitTorrentStatus(t, m2, job.ID, StatusSeeding, 15*time.Second)
	if view.Status != StatusSeeding || view.Done < view.Size || view.Size == 0 {
		t.Fatalf("wanted a complete seeding torrent after restart, got %+v", view)
	}
	if fileExists(legacy) {
		t.Fatal("legacy .torrent.bolt.db was not removed from the download folder after restart")
	}
	if !fileExists(dataDirCompletionPath(cfg, hash)) {
		t.Fatal("completion file was not restored in the data folder after migration")
	}
}
