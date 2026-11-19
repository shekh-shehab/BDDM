package main

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

func TestSpeedLimitUIScreenshot(t *testing.T) {
	if os.Getenv("BDDM_SHOT") == "" {
		t.Skip("set BDDM_SHOT=1 to write speed-limit UI screenshots")
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

	m, dl := newTestManager(t, 4)
	s := m.Settings()
	s.HTTPDownloadLimit = 5 << 20
	s.TorrentDownloadLimit = 0
	s.TorrentUploadLimit = 200 << 10
	s.DownloadDir = dl
	s.Notifications = true
	s.AskCaptured = true
	s.ResumeOnStart = true
	s.CheckUpdates = true
	if _, err := m.SetSettings(s); err != nil {
		t.Fatal(err)
	}

	dlLim := int64(2 << 20)
	ulLim := int64(100 << 10)
	j1 := &Job{
		ID: "shot-down", URL: "magnet:?xt=urn:btih:abc", Name: "ubuntu-24.04.1-desktop-amd64",
		Kind: kindTorrent, Status: StatusDownloading, Size: 5576286208, Dir: dl,
		Torrent: &TorrentInfo{Files: 1}, DlLimit: &dlLim, UlLimit: &ulLim,
	}
	j1.done.Store(2362232012)
	j1.liveMu.Lock()
	j1.speed, j1.upSpeed, j1.peers = 1.8*1024*1024, 120*1024, 12
	j1.liveMu.Unlock()
	j2 := &Job{
		ID: "shot-seed", URL: "magnet:?xt=urn:btih:def", Name: "open-source-films-collection",
		Kind: kindTorrent, Status: StatusSeeding, Size: 1288490188, Dir: dl,
		Torrent: &TorrentInfo{Files: 3, Uploaded: 1831656036},
	}
	j2.done.Store(j2.Size)
	j2.liveMu.Lock()
	j2.upSpeed, j2.peers = 85*1024, 8
	j2.liveMu.Unlock()
	m.mu.Lock()
	m.jobs = append([]*Job{j2, j1}, m.jobs...)
	m.mu.Unlock()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	hs := &http.Server{Handler: newServer(m, port, func() {}, nil)}
	go hs.Serve(ln)
	defer hs.Close()
	base := "http://127.0.0.1:" + port + "/"

	dir := os.Getenv("BDDM_SHOT_DIR")
	if dir == "" {
		dir = "/workspace/bddm"
	}
	shots := []struct {
		name, url string
		w, h      int
	}{
		{"speed-limit-settings-light.png", base + "?tab=settings&scheme=light", 900, 1060},
		{"speed-limit-settings-dark.png", base + "?tab=settings&scheme=dark", 900, 1060},
		{"speed-limit-popover-light.png", base + "?scheme=light&speedlimit=shot-down", 1100, 720},
		{"speed-limit-popover-dark.png", base + "?scheme=dark&speedlimit=shot-down", 1100, 720},
	}
	for _, sh := range shots {
		out := dir + "/" + sh.name
		cmd := exec.Command(chrome, "--headless=new", "--disable-gpu", "--no-sandbox", "--hide-scrollbars",
			"--window-size="+strconv.Itoa(sh.w)+","+strconv.Itoa(sh.h),
			"--virtual-time-budget=4000",
			"--screenshot="+out, sh.url)
		cmd.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS=")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", sh.name, err, b)
		}
		if fi, err := os.Stat(out); err != nil || fi.Size() < 1000 {
			t.Fatalf("%s missing or tiny: %v", sh.name, err)
		}
		t.Logf("wrote %s (%d bytes)", out, mustSize(out))
	}
	_ = time.Second
}
