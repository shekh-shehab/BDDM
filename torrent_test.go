package main

import (
	"bytes"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"golang.org/x/time/rate"
)

func newTorrentRateLimit(bps int) *rate.Limiter {
	if bps < 1 {
		bps = 1
	}
	return rate.NewLimiter(rate.Limit(bps), bps)
}

func init() {
	testTorrentLoopback = true
}

func TestParseMagnet(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef01234567"
	m, err := parseMagnet("magnet:?xt=urn:btih:" + hash + "&dn=ubuntu-24.04.iso")
	if err != nil {
		t.Fatal(err)
	}
	if m.Hash != hash {
		t.Fatalf("hash: got %q", m.Hash)
	}
	if m.Name != "ubuntu-24.04.iso" {
		t.Fatalf("name: got %q", m.Name)
	}
	if !isMagnet(m.URI) {
		t.Fatalf("uri is not a magnet: %s", m.URI)
	}

	m, err = parseMagnet("  MAGNET:?xt=urn:btih:" + strings.ToUpper(hash) + "  ")
	if err != nil {
		t.Fatal(err)
	}
	if m.Hash != hash {
		t.Fatalf("uppercase hash: got %q", m.Hash)
	}
	if !strings.HasPrefix(m.Name, "Torrent ") {
		t.Fatalf("expected a fallback name, got %q", m.Name)
	}

	if _, err := parseMagnet("https://example.com/file.iso"); err == nil {
		t.Fatal("expected an error for a normal URL")
	}
	if _, err := parseMagnet("magnet:?dn=only-a-name"); err == nil {
		t.Fatal("expected an error for a magnet with no info hash")
	}
	if !isMagnet("  Magnet:?xt=urn:btih:" + hash) {
		t.Fatal("isMagnet should accept Magnet:")
	}
}

func torrentTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	base := t.TempDir()
	cfg := filepath.Join(base, "cfg")
	dl := filepath.Join(base, "dl")
	if err := os.MkdirAll(dl, 0755); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.tLoopback = true
	s := m.Settings()
	s.DownloadDir = dl
	s.ResumeOnStart = true
	if _, err := m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Shutdown() })
	return m, dl
}

func writeTestTorrent(t *testing.T, dir, name string, data []byte) (metaPath string, mi *metainfo.MetaInfo, srcDir string) {
	t.Helper()
	srcDir = filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, name), data, 0644); err != nil {
		t.Fatal(err)
	}
	info := metainfo.Info{PieceLength: 32 * 1024}
	if err := info.BuildFromFilePath(filepath.Join(srcDir, name)); err != nil {
		t.Fatal(err)
	}
	mi = &metainfo.MetaInfo{}
	mi.SetDefaults()
	var err error
	mi.InfoBytes, err = bencode.Marshal(&info)
	if err != nil {
		t.Fatal(err)
	}
	metaPath = filepath.Join(dir, name+".torrent")
	f, err := os.Create(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := mi.Write(f); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return metaPath, mi, srcDir
}

func startTestSeeder(t *testing.T, mi *metainfo.MetaInfo, dataDir string) *torrent.Client {
	t.Helper()
	cfg := torrent.NewDefaultClientConfig()
	cfg.Seed = true
	cfg.DataDir = dataDir
	cfg.Slogger = slog.New(slog.DiscardHandler)
	cfg.ListenHost = func(string) string { return "127.0.0.1" }
	cfg.ListenPort = 0
	cfg.NoDHT = true
	cfg.DisableTrackers = true
	cfg.DisablePEX = true
	cfg.NoDefaultPortForwarding = true
	cfg.DisableIPv6 = true
	cfg.DisableUTP = true
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	tor, err := cl.AddTorrent(mi)
	if err != nil {
		t.Fatal(err)
	}
	<-tor.GotInfo()
	tor.VerifyData()
	if tor.BytesCompleted() < tor.Length() {
		t.Fatalf("seeder does not have the complete data: %d / %d", tor.BytesCompleted(), tor.Length())
	}
	return cl
}

func waitTorrentStatus(t *testing.T, m *Manager, id, want string, timeout time.Duration) JobView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last JobView
	for time.Now().Before(deadline) {
		m.mu.Lock()
		j := m.findLocked(id)
		if j != nil {
			last = j.viewLocked()
		}
		m.mu.Unlock()
		if last.ID == id && last.Status == want {
			return last
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for status %s (last: %+v)", want, last)
	return last
}

func TestTorrentFileAndStatePersistence(t *testing.T) {
	m, _ := torrentTestManager(t)
	// A .torrent with no live swarm still appears in the list and is saved.
	dir := t.TempDir()
	data := bytes.Repeat([]byte("bddm-torrent-persist-"), 2000) // ~40 KiB
	meta, _, _ := writeTestTorrent(t, dir, "sample.bin", data)

	job, err := m.AddTorrentFile(meta, nil)
	if err != nil {
		t.Fatal(err)
	}
	pend, err := m.TorrentPending(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !pend.Ready || pend.Name != "sample.bin" || pend.Size != int64(len(data)) {
		t.Fatalf("pending: %+v", pend)
	}
	started, err := m.StartTorrent(job.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if started.Kind != kindTorrent {
		t.Fatalf("kind: %s", started.Kind)
	}
	if started.Status != StatusDownloading && started.Status != StatusSeeding {
		t.Fatalf("status after start: %s", started.Status)
	}

	// No peers: pause, persist, restore as paused.
	if err := m.Pause(job.ID); err != nil {
		t.Fatal(err)
	}
	waitTorrentStatus(t, m, job.ID, StatusPaused, 5*time.Second)
	cfg := m.configDir
	dl := m.Settings().DownloadDir
	m.Shutdown()

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
	found := false
	for _, v := range m2.List() {
		if v.ID == job.ID {
			found = true
			if v.Kind != kindTorrent {
				t.Fatalf("restored kind: %s", v.Kind)
			}
			if v.Status != StatusPaused {
				t.Fatalf("user-paused torrent should stay paused, got %s", v.Status)
			}
			if v.Name != "sample.bin" {
				t.Fatalf("restored name: %s", v.Name)
			}
		}
	}
	if !found {
		t.Fatal("torrent missing from the list after restart")
	}
}

func TestTorrentLocalSwarmDownloadAndSeed(t *testing.T) {
	data := bytes.Repeat([]byte("BDDM-SWARM-DATA-"), 4096) // 64 KiB
	tmp := t.TempDir()
	meta, mi, srcDir := writeTestTorrent(t, tmp, "swarm.bin", data)
	seeder := startTestSeeder(t, mi, srcDir)

	m, dl := torrentTestManager(t)
	job, err := m.AddTorrentFile(meta, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartTorrent(job.ID, dl); err != nil {
		t.Fatal(err)
	}
	// Give the client a moment to start listening, then introduce the seeder.
	time.Sleep(200 * time.Millisecond)
	if err := m.addTorrentClientPeer(job.ID, seeder); err != nil {
		t.Fatal(err)
	}
	view := waitTorrentStatus(t, m, job.ID, StatusSeeding, 20*time.Second)
	if view.Size != int64(len(data)) || view.Done < view.Size {
		t.Fatalf("incomplete after seeding status: done=%d size=%d", view.Done, view.Size)
	}
	got, err := os.ReadFile(filepath.Join(dl, "swarm.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("downloaded file does not match the source")
	}

	// Persistence: after a restart it should come back seeding (or at least complete and running).
	cfg := m.configDir
	m.Shutdown()

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
	// NewManager already called restoreTorrentsLocked before we set tLoopback.
	// Start any torrent that should be running (restore may have failed to
	// bind a public client in tests; kick it with the loopback flag).
	m2.mu.Lock()
	m2.restoreTorrentsLocked()
	var id string
	for _, j := range m2.jobs {
		if j.Kind == kindTorrent && !j.Removed {
			id = j.ID
		}
	}
	m2.mu.Unlock()
	if id == "" {
		t.Fatal("torrent not restored")
	}
	view = waitTorrentStatus(t, m2, id, StatusSeeding, 15*time.Second)
	if view.Status != StatusSeeding {
		t.Fatalf("wanted Seeding after restart, got %s", view.Status)
	}

	// A third client should be able to download from the restored seeder.
	leecherDir := filepath.Join(tmp, "leecher")
	if err := os.MkdirAll(leecherDir, 0755); err != nil {
		t.Fatal(err)
	}
	lcfg := torrent.NewDefaultClientConfig()
	lcfg.Seed = true
	lcfg.DataDir = leecherDir
	lcfg.Slogger = slog.New(slog.DiscardHandler)
	lcfg.ListenHost = func(string) string { return "127.0.0.1" }
	lcfg.ListenPort = 0
	lcfg.NoDHT, lcfg.DisableTrackers, lcfg.DisablePEX = true, true, true
	lcfg.NoDefaultPortForwarding, lcfg.DisableIPv6, lcfg.DisableUTP = true, true, true
	leecher, err := torrent.NewClient(lcfg)
	if err != nil {
		t.Fatal(err)
	}
	defer leecher.Close()
	lt, err := leecher.AddTorrent(mi)
	if err != nil {
		t.Fatal(err)
	}
	<-lt.GotInfo()
	lt.DownloadAll()
	m2.mu.Lock()
	cl := m2.tClient
	m2.mu.Unlock()
	if cl == nil {
		t.Fatal("restored manager has no torrent client")
	}
	if lt.AddClientPeer(cl) == 0 {
		t.Fatal("could not add the restored seeder as a peer")
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if lt.BytesCompleted() >= lt.Length() && lt.Length() > 0 {
			got, err := os.ReadFile(filepath.Join(leecherDir, "swarm.bin"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, data) {
				t.Fatal("leecher file does not match after downloading from the restored seeder")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("restored seeder did not upload to a new leecher (got %d / %d)", lt.BytesCompleted(), lt.Length())
}

func TestAddTorrentDuplicateAndCancel(t *testing.T) {
	m, _ := torrentTestManager(t)
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	uri := "magnet:?xt=urn:btih:" + hash + "&dn=dup-test"
	j1, err := m.AddTorrentMagnet(uri)
	if err != nil {
		t.Fatal(err)
	}
	// Still pending: a second add with the same hash is allowed to wait, but
	// after cancel the magnet can be added again.
	if err := m.CancelTorrent(j1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.TorrentPending(j1.ID); err == nil {
		t.Fatal("cancelled torrent should disappear")
	}
	j2, err := m.AddTorrentMagnet(uri)
	if err != nil {
		t.Fatal(err)
	}
	if j2.ID == j1.ID {
		t.Fatal("expected a new pending job after cancel")
	}
	if err := m.CancelTorrent(j2.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTorrentUIScreenshot(t *testing.T) {
	if os.Getenv("BDDM_SHOT") == "" {
		t.Skip("set BDDM_SHOT=1 to write /workspace/bddm/torrent-ui.png")
	}
	out := os.Getenv("BDDM_SHOT_PATH")
	if out == "" {
		out = "/workspace/bddm/torrent-ui.png"
	}
	chrome := firstNonEmpty(
		os.Getenv("CHROME"),
		"/opt/google/chrome/chrome",
		"/usr/bin/google-chrome",
		"/opt/ungoogled-chromium/chrome",
	)
	if _, err := os.Stat(chrome); err != nil {
		t.Skip("no chrome for a screenshot")
	}

	small := bytes.Repeat([]byte("SMALL-SEED-"), 6000)   // ~66 KiB, finishes first
	large := bytes.Repeat([]byte("LARGE-DOWN-"), 400000) // ~4.4 MiB, still downloading
	tmp := t.TempDir()
	smallMeta, smallMI, smallSrc := writeTestTorrent(t, filepath.Join(tmp, "s"), "open-source-films.bin", small)
	largeMeta, largeMI, largeSrc := writeTestTorrent(t, filepath.Join(tmp, "l"), "ubuntu-desktop-sample.bin", large)

	// One seeder client, rate-limited so the large file is mid-transfer in the shot.
	cfg := torrent.NewDefaultClientConfig()
	cfg.Seed = true
	cfg.DataDir = tmp // files live in different src dirs; we'll add each with its own storage
	cfg.Slogger = slog.New(slog.DiscardHandler)
	cfg.ListenHost = func(string) string { return "127.0.0.1" }
	cfg.ListenPort = 0
	cfg.NoDHT, cfg.DisableTrackers, cfg.DisablePEX = true, true, true
	cfg.NoDefaultPortForwarding, cfg.DisableIPv6, cfg.DisableUTP = true, true, true
	cfg.UploadRateLimiter = newTorrentRateLimit(180 * 1024) // ~180 KiB/s
	seeder, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer seeder.Close()
	addSeed := func(mi *metainfo.MetaInfo, dir string) {
		spec := torrent.TorrentSpecFromMetaInfo(mi)
		spec.Storage = storage.NewFile(dir)
		tor, _, err := seeder.AddTorrentSpec(spec)
		if err != nil {
			t.Fatal(err)
		}
		<-tor.GotInfo()
		if err := tor.VerifyData(); err != nil {
			t.Fatal(err)
		}
	}
	addSeed(smallMI, smallSrc)
	addSeed(largeMI, largeSrc)

	m, dl := torrentTestManager(t)
	startAndPeer := func(meta string) string {
		job, err := m.AddTorrentFile(meta, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.StartTorrent(job.ID, dl); err != nil {
			t.Fatal(err)
		}
		time.Sleep(150 * time.Millisecond)
		if err := m.addTorrentClientPeer(job.ID, seeder); err != nil {
			t.Fatal(err)
		}
		return job.ID
	}
	smallID := startAndPeer(smallMeta)
	waitTorrentStatus(t, m, smallID, StatusSeeding, 20*time.Second)
	largeID := startAndPeer(largeMeta)

	deadline := time.Now().Add(25 * time.Second)
	var largeView JobView
	for time.Now().Before(deadline) {
		m.mu.Lock()
		j := m.findLocked(largeID)
		if j != nil {
			largeView = j.viewLocked()
		}
		m.mu.Unlock()
		if largeView.Size > 0 && largeView.Done > largeView.Size/5 && largeView.Done < largeView.Size*3/4 && largeView.Status == StatusDownloading {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if largeView.Status != StatusDownloading {
		t.Fatalf("large torrent was not downloading: %+v", largeView)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	hs := &http.Server{Handler: newServer(m, port, func() {}, nil)}
	go hs.Serve(ln)
	defer hs.Close()

	url := "http://127.0.0.1:" + port + "/"
	cmd := exec.Command(chrome, "--headless=new", "--disable-gpu", "--no-sandbox", "--hide-scrollbars",
		"--window-size=1100,720", "--screenshot="+out, url)
	cmd.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS=")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("chrome screenshot: %v\n%s", err, b)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() < 1000 {
		t.Fatalf("screenshot missing or tiny: %v", err)
	}
	t.Logf("wrote %s (%d bytes); large torrent at %d%%", out, mustSize(out), int(largeView.Done*100/largeView.Size))
}

func mustSize(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func torrentJob(m *Manager, id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.findLocked(id)
}

func reopenTorrentManager(t *testing.T, cfg, dl string, resumeOnStart bool) *Manager {
	t.Helper()
	m, err := NewManager(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Shutdown() })
	m.tLoopback = true
	s := m.Settings()
	s.DownloadDir = dl
	s.ResumeOnStart = resumeOnStart
	if _, err := m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.restoreTorrentsLocked()
	m.mu.Unlock()
	return m
}

func TestTorrentActiveResumesAfterRestart(t *testing.T) {
	m, dl := torrentTestManager(t)
	s := m.Settings()
	s.ResumeOnStart = false // torrents should still come back
	if _, err := m.SetSettings(s); err != nil {
		t.Fatal(err)
	}

	base := t.TempDir()
	metaActive, _, _ := writeTestTorrent(t, filepath.Join(base, "a"), "active.bin", bytes.Repeat([]byte("bddm-active-"), 2000))
	metaPaused, _, _ := writeTestTorrent(t, filepath.Join(base, "p"), "paused.bin", bytes.Repeat([]byte("bddm-paused-"), 2000))

	active, err := m.AddTorrentFile(metaActive, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartTorrent(active.ID, ""); err != nil {
		t.Fatal(err)
	}
	paused, err := m.AddTorrentFile(metaPaused, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartTorrent(paused.ID, ""); err != nil {
		t.Fatal(err)
	}
	waitTorrentStatus(t, m, active.ID, StatusDownloading, 5*time.Second)
	if err := m.Pause(paused.ID); err != nil {
		t.Fatal(err)
	}
	waitTorrentStatus(t, m, paused.ID, StatusPaused, 5*time.Second)
	if j := torrentJob(m, paused.ID); j == nil || j.Interrupted {
		t.Fatal("a user-paused torrent must not be marked to auto-resume")
	}

	cfg := m.configDir
	m.Shutdown()

	if j := torrentJob(m, active.ID); j == nil || !j.Interrupted {
		t.Fatal("a downloading torrent must be marked to resume after shutdown")
	}
	if j := torrentJob(m, paused.ID); j == nil || j.Interrupted || j.Status != StatusPaused {
		t.Fatalf("user-paused torrent should stay paused, not auto-resume: %+v", j)
	}

	m2 := reopenTorrentManager(t, cfg, dl, false)
	waitTorrentStatus(t, m2, active.ID, StatusDownloading, 10*time.Second)
	if got := waitTorrentStatus(t, m2, paused.ID, StatusPaused, 5*time.Second); got.Status != StatusPaused {
		t.Fatalf("user-paused torrent resumed unexpectedly: %s", got.Status)
	}
}

func TestTorrentResumesAfterUpdateStylePause(t *testing.T) {
	// An in-app update used to pause every running torrent (without
	// remembering that this was not a user pause). After that kind of
	// save, the torrent must still start again.
	m, dl := torrentTestManager(t)
	s := m.Settings()
	s.ResumeOnStart = false
	if _, err := m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	meta, _, _ := writeTestTorrent(t, t.TempDir(), "update.bin", bytes.Repeat([]byte("bddm-update-"), 2000))
	job, err := m.AddTorrentFile(meta, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartTorrent(job.ID, ""); err != nil {
		t.Fatal(err)
	}
	waitTorrentStatus(t, m, job.ID, StatusDownloading, 5*time.Second)

	m.PauseAllRunning()
	waitTorrentStatus(t, m, job.ID, StatusPaused, 5*time.Second)
	if j := torrentJob(m, job.ID); j == nil || !j.Interrupted {
		t.Fatal("update-style pause should still mark the torrent to resume")
	}

	cfg := m.configDir
	m.Shutdown()
	m2 := reopenTorrentManager(t, cfg, dl, false)
	waitTorrentStatus(t, m2, job.ID, StatusDownloading, 10*time.Second)
}

func TestTorrentSeedingResumesAfterShutdown(t *testing.T) {
	data := bytes.Repeat([]byte("BDDM-SEED-RESUME-"), 4096)
	tmp := t.TempDir()
	meta, mi, srcDir := writeTestTorrent(t, tmp, "seed.bin", data)
	seeder := startTestSeeder(t, mi, srcDir)

	m, dl := torrentTestManager(t)
	s := m.Settings()
	s.ResumeOnStart = false
	if _, err := m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
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
	if j := torrentJob(m, job.ID); j == nil || !j.Interrupted {
		t.Fatal("a seeding torrent must be marked to resume after shutdown")
	}

	m2 := reopenTorrentManager(t, cfg, dl, false)
	waitTorrentStatus(t, m2, job.ID, StatusSeeding, 15*time.Second)
}
