package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExistingAbsDir(t *testing.T) {
	if existingAbsDir("") != "" || existingAbsDir("   ") != "" {
		t.Fatal("empty path should be rejected")
	}
	if existingAbsDir("Downloads") != "" || existingAbsDir("./here") != "" {
		t.Fatal("relative path should be rejected")
	}
	missing := filepath.Join(t.TempDir(), "gone")
	if existingAbsDir(missing) != "" {
		t.Fatal("missing folder should be rejected")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-folder")
	if err := os.WriteFile(file, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if existingAbsDir(file) != "" {
		t.Fatal("a file should not count as a folder")
	}
	got := existingAbsDir(dir)
	if got != filepath.Clean(dir) {
		t.Fatalf("existing folder: got %q want %q", got, filepath.Clean(dir))
	}
}

func TestTorrentSaveDirFallback(t *testing.T) {
	dl := t.TempDir()
	last := t.TempDir()
	s := Settings{DownloadDir: dl, LastTorrentDir: last}
	if got := s.torrentSaveDir("film.mkv", true); got != filepath.Clean(last) {
		t.Fatalf("should use last folder when it exists: %q", got)
	}

	gone := filepath.Join(t.TempDir(), "removed")
	s.LastTorrentDir = gone
	if got := s.torrentSaveDir("readme.txt", false); got != dl {
		t.Fatalf("missing last folder should fall back to DownloadDir: %q", got)
	}

	s.LastTorrentDir = ""
	s.LastTorrentDir = "relative/path"
	s.fix()
	if s.LastTorrentDir != "" {
		t.Fatalf("relative LastTorrentDir should be cleared by fix: %q", s.LastTorrentDir)
	}
}

func TestStartTorrentRemembersFolderAndPrefills(t *testing.T) {
	m, dl := torrentTestManager(t)
	chosen := filepath.Join(t.TempDir(), "movies")
	if err := os.MkdirAll(chosen, 0755); err != nil {
		t.Fatal(err)
	}

	meta, _, _ := writeTestTorrent(t, t.TempDir(), "first.bin", []byte("first-torrent-payload-aaaa"))
	job, err := m.AddTorrentFile(meta, nil)
	if err != nil {
		t.Fatal(err)
	}
	pend, err := m.TorrentPending(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pend.Dir != dl {
		t.Fatalf("first add should pre-fill the default folder %q, got %q", dl, pend.Dir)
	}

	if _, err := m.StartTorrent(job.ID, chosen); err != nil {
		t.Fatal(err)
	}
	if err := m.Pause(job.ID); err != nil {
		t.Fatal(err)
	}
	if got := m.Settings().LastTorrentDir; got != chosen {
		t.Fatalf("LastTorrentDir after start: %q want %q", got, chosen)
	}

	raw, err := os.ReadFile(filepath.Join(m.configDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk Settings
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.LastTorrentDir != chosen {
		t.Fatalf("settings.json lastTorrentDir: %q want %q", disk.LastTorrentDir, chosen)
	}

	// A later .torrent file pre-fills the remembered folder.
	meta2, _, _ := writeTestTorrent(t, t.TempDir(), "second.bin", []byte("second-torrent-payload-bbbb"))
	job2, err := m.AddTorrentFile(meta2, nil)
	if err != nil {
		t.Fatal(err)
	}
	pend2, err := m.TorrentPending(job2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pend2.Dir != chosen {
		t.Fatalf(".torrent add should pre-fill last folder %q, got %q", chosen, pend2.Dir)
	}

	// A magnet also pre-fills it, even before metadata arrives.
	hash := strings.Repeat("ab", 20)
	mag, err := m.AddTorrentMagnet("magnet:?xt=urn:btih:" + hash + "&dn=third")
	if err != nil {
		t.Fatal(err)
	}
	pendMag, err := m.TorrentPending(mag.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pendMag.Dir != chosen {
		t.Fatalf("magnet add should pre-fill last folder %q, got %q", chosen, pendMag.Dir)
	}

	cfg := m.configDir
	m.Shutdown()

	m2, err := NewManager(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Shutdown()
	m2.tLoopback = true
	if got := m2.Settings().LastTorrentDir; got != chosen {
		t.Fatalf("after restart LastTorrentDir: %q want %q", got, chosen)
	}

	meta3, _, _ := writeTestTorrent(t, t.TempDir(), "fourth.bin", []byte("fourth-torrent-payload-cccc"))
	job3, err := m2.AddTorrentFile(meta3, nil)
	if err != nil {
		t.Fatal(err)
	}
	pend3, err := m2.TorrentPending(job3.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pend3.Dir != chosen {
		t.Fatalf("after restart, add should pre-fill last folder %q, got %q", chosen, pend3.Dir)
	}
}

func TestLastTorrentDirMissingFallsBack(t *testing.T) {
	m, dl := torrentTestManager(t)
	chosen := filepath.Join(t.TempDir(), "old-folder")
	if err := os.MkdirAll(chosen, 0755); err != nil {
		t.Fatal(err)
	}

	meta, _, _ := writeTestTorrent(t, t.TempDir(), "keep.bin", []byte("keep-torrent-payload-dddd"))
	job, err := m.AddTorrentFile(meta, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartTorrent(job.ID, chosen); err != nil {
		t.Fatal(err)
	}
	if err := m.Pause(job.ID); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(chosen); err != nil {
		t.Fatal(err)
	}

	meta2, _, _ := writeTestTorrent(t, t.TempDir(), "later.bin", []byte("later-torrent-payload-eeee"))
	job2, err := m.AddTorrentFile(meta2, nil)
	if err != nil {
		t.Fatal(err)
	}
	pend, err := m.TorrentPending(job2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pend.Dir != dl {
		t.Fatalf("missing last folder should fall back to default %q, got %q", dl, pend.Dir)
	}

	hash := strings.Repeat("cd", 20)
	mag, err := m.AddTorrentMagnet("magnet:?xt=urn:btih:" + hash + "&dn=later-magnet")
	if err != nil {
		t.Fatal(err)
	}
	pendMag, err := m.TorrentPending(mag.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pendMag.Dir != dl {
		t.Fatalf("magnet should also fall back to default %q, got %q", dl, pendMag.Dir)
	}

	// Choosing a new folder updates the remembered path.
	newer := filepath.Join(t.TempDir(), "new-folder")
	if err := os.MkdirAll(newer, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartTorrent(job2.ID, newer); err != nil {
		t.Fatal(err)
	}
	if err := m.Pause(job2.ID); err != nil {
		t.Fatal(err)
	}
	if got := m.Settings().LastTorrentDir; got != newer {
		t.Fatalf("LastTorrentDir after a new choice: %q want %q", got, newer)
	}
}
