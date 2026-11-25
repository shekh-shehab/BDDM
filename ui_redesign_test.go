package main

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
)

func readUI(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("ui", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMainWindowRedesignShell(t *testing.T) {
	index := readUI(t, "index.html")
	theme := readUI(t, "theme.css")
	for _, want := range []string{
		`href="theme.css"`,
		`class="app"`,
		`class="sidebar"`,
		`data-filter="all"`,
		`data-filter="Video"`,
		`data-filter="Audio"`,
		`data-filter="Documents"`,
		`data-filter="Programs"`,
		`data-filter="torrent"`,
		`id="navSettings"`,
		`data-tab="settings"`,
		`id="tab-downloads"`,
		`id="tab-settings"`,
		`id="yieldHint"`,
		`id="setYield"`,
		"Slow down downloads while I use the internet",
		"Saving bandwidth",
		`id="list"`,
		`id="emptyList"`,
		`id="url"`,
		`id="addBtn"`,
		`id="torrentBtn"`,
		`id="openDir"`,
		`id="quitBtn"`,
		`id="statSpeed"`,
		`id="statActive"`,
		`id="statsRow"`,
		`src="icon-32.png"`,
		`href="icon-32.png"`,
		`href="icon.png"`,
		"function fileCategory",
		"function showTab",
		"listFilter",
	} {
		if !strings.Contains(index, want) {
			t.Errorf("ui/index.html missing %q", want)
		}
	}
	for _, want := range []string{
		`--page: #e6e9ee`,
		`--surface: #ffffff`,
		`--accent: #2f6fed`,
		`--page: #14161a`,
		`--surface: #1f2228`,
		`prefers-color-scheme: dark`,
		`html[data-scheme="light"]`,
		`html[data-scheme="dark"]`,
		`.nav`,
		`.stat`,
		`.card`,
		`.seg`,
	} {
		if !strings.Contains(theme, want) && !strings.Contains(index, want) {
			t.Errorf("theme/index missing design token or rule %q", want)
		}
	}
	if strings.Contains(index, "fill='#3b82f6'") && strings.Contains(index, "M32 14v26") {
		t.Error("old SVG logo should stay retired")
	}
	assertEnglishUI(t, "ui/index.html", index)
}

func assertEnglishUI(t *testing.T, name, s string) {
	t.Helper()
	// UI copy must stay English: no Bangla letters in the source.
	for _, r := range s {
		if unicode.In(r, unicode.Bengali) {
			t.Errorf("%s contains a Bengali character (UI must be English)", name)
			return
		}
	}
}

func findChrome() string {
	for _, p := range []string{
		os.Getenv("CHROME"),
		"/usr/bin/google-chrome",
		"/opt/google/chrome/chrome",
		"/opt/ungoogled-chromium/chrome",
	} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func startUITestServer(t *testing.T, m *Manager) (base string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	hs := &http.Server{Handler: newServer(m, port, func() {}, nil)}
	go hs.Serve(ln)
	t.Cleanup(func() { _ = hs.Close() })
	return "http://127.0.0.1:" + port
}

func seedRedesignJobs(t *testing.T, m *Manager, dl string) {
	t.Helper()
	now := time.Now()
	jobs := []*Job{
		{
			ID: "shot-video", URL: "https://example.com/film.mp4", Name: "The Hidden River.mp4",
			Kind: "video", Status: StatusDownloading, Size: 1845493760, Dir: dl, Started: now.Add(-12 * time.Minute),
		},
		{
			ID: "shot-doc", URL: "https://example.com/guide.pdf", Name: "User Guide.pdf",
			Status: StatusDone, Size: 2488320, Dir: dl, Started: now.Add(-2 * time.Hour), Finished: now.Add(-5 * time.Minute),
		},
		{
			ID: "shot-song", URL: "https://example.com/song.mp3", Name: "Midnight Drive.mp3",
			Status: StatusPaused, Size: 9437184, Dir: dl, Started: now.Add(-40 * time.Minute),
		},
		{
			ID: "shot-tor", URL: "magnet:?xt=urn:btih:abc", Name: "ubuntu-24.04.1-desktop-amd64",
			Kind: kindTorrent, Status: StatusDownloading, Size: 5576286208, Dir: dl, Started: now.Add(-30 * time.Minute),
			Torrent: &TorrentInfo{Files: 1},
		},
		{
			ID: "shot-err", URL: "https://example.com/missing.zip", Name: "archive.zip",
			Status: StatusError, Size: 0, Dir: dl, Started: now.Add(-3 * time.Hour), Error: "The server returned 404.",
		},
	}
	jobs[0].done.Store(794568704)
	jobs[0].liveMu.Lock()
	jobs[0].speed = 2.4 * 1024 * 1024
	jobs[0].liveMu.Unlock()
	jobs[1].done.Store(jobs[1].Size)
	jobs[2].done.Store(3145728)
	jobs[3].done.Store(2362232012)
	jobs[3].liveMu.Lock()
	jobs[3].speed, jobs[3].upSpeed, jobs[3].peers = 1.8*1024*1024, 120*1024, 12
	jobs[3].liveMu.Unlock()
	m.mu.Lock()
	m.jobs = append(jobs, m.jobs...)
	m.mu.Unlock()
}

func chromeShot(t *testing.T, chrome, url, out string, w, h int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(chrome, "--headless=new", "--disable-gpu", "--no-sandbox", "--hide-scrollbars",
		"--window-size="+strconv.Itoa(w)+","+strconv.Itoa(h),
		"--virtual-time-budget=4000",
		"--screenshot="+out, url)
	cmd.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS=")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("screenshot %s: %v\n%s", out, err, b)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() < 1000 {
		t.Fatalf("screenshot missing or tiny: %s: %v", out, err)
	}
	t.Logf("wrote %s (%d bytes)", out, mustSize(out))
}

func TestMainWindowRedesignRenders(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no chrome for a screenshot")
	}
	m, dl := newTestManager(t, 4)
	seedRedesignJobs(t, m, dl)
	base := startUITestServer(t, m)
	dir := filepath.Join("ui-shots")
	chromeShot(t, chrome, base+"/?scheme=light", filepath.Join(dir, "25-main-light.png"), 1100, 720)
	chromeShot(t, chrome, base+"/?scheme=dark", filepath.Join(dir, "25-main-dark.png"), 1100, 720)
	chromeShot(t, chrome, base+"/?tab=settings&scheme=light", filepath.Join(dir, "25-settings-light.png"), 1100, 900)
}
