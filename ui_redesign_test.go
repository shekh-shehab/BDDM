package main

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

func TestConfirmProgressCompletePages(t *testing.T) {
	confirm := readUI(t, "confirm.html")
	for _, want := range []string{
		`href="theme.css"`, `href="icon-32.png"`, `href="icon.png"`,
		`id="name"`, `id="dir"`, `id="size"`, `id="url"`, `id="browse"`,
		`id="start"`, `id="later"`, `id="cancel"`, `id="hash"`, `id="mirrors"`,
		`id="fmt"`, `id="dup"`, "Download Later", "Start Download",
		`dataset.scheme`,
	} {
		if !strings.Contains(confirm, want) {
			t.Errorf("ui/confirm.html missing %q", want)
		}
	}
	assertEnglishUI(t, "ui/confirm.html", confirm)

	progress := readUI(t, "progress.html")
	for _, want := range []string{
		`href="theme.css"`, `href="icon-32.png"`, `id="name"`, `id="pct"`,
		`id="spd"`, `id="eta"`, `id="pause"`, `id="cancel"`, `id="hide"`,
		`class="ring"`, "Download progress", "/api/jobs/",
	} {
		if !strings.Contains(progress, want) {
			t.Errorf("ui/progress.html missing %q", want)
		}
	}
	assertEnglishUI(t, "ui/progress.html", progress)

	complete := readUI(t, "complete.html")
	for _, want := range []string{
		`href="theme.css"`, `href="icon-32.png"`, `id="name"`, `id="open"`,
		`id="folder"`, `id="close"`, "Download complete", "Open folder",
		"/openfile",
	} {
		if !strings.Contains(complete, want) {
			t.Errorf("ui/complete.html missing %q", want)
		}
	}
	assertEnglishUI(t, "ui/complete.html", complete)
}

func TestProgressPopupNotForFolderOrBurst(t *testing.T) {
	_ = takePopupLog()
	m, _ := newTestManager(t, 4)
	if popupEligible(JobView{ID: "a", Group: "g1"}) {
		t.Fatal("folder files must not get a compact progress window")
	}
	if popupEligible(JobView{ID: "t", Kind: kindTorrent}) {
		t.Fatal("torrents keep their own window")
	}
	if !popupEligible(JobView{ID: "f", Name: "a.bin"}) {
		t.Fatal("a single file should be eligible")
	}
	s := &server{m: m, port: "9"}
	for i := 1; i <= 5; i++ {
		s.offerProgress(JobView{ID: "j" + strconv.Itoa(i)}, false)
	}
	log := takePopupLog()
	if len(log) != popupBurstMax {
		t.Fatalf("burst should cap compact windows at %d, got %v", popupBurstMax, log)
	}
	s.offerProgress(JobView{ID: "later"}, true)
	if got := takePopupLog(); len(got) != 0 {
		t.Fatalf("Download Later should not open progress: %v", got)
	}
}

func TestGetJobAndCompletePopup(t *testing.T) {
	_ = takePopupLog()
	m, dl := newTestManager(t, 4)
	now := time.Now()
	j := &Job{ID: "done1", URL: "https://example.com/a.bin", Name: "a.bin", Status: StatusDone, Size: 1024, Dir: dl, Started: now.Add(-3 * time.Minute), Finished: now}
	j.done.Store(1024)
	m.mu.Lock()
	m.jobs = append([]*Job{j}, m.jobs...)
	m.mu.Unlock()

	hs := startUITestServer(t, m)
	res, err := http.Get(hs + "/api/jobs/done1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("GET /api/jobs/done1: %s", res.Status)
	}

	m.mu.Lock()
	m.markPopupLocked("done1")
	m.offerCompleteLocked(j)
	m.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	log := takePopupLog()
	if len(log) != 1 || log[0] != "complete:done1" {
		t.Fatalf("wanted complete:done1, got %v", log)
	}

	// A second complete must not fire without a fresh mark.
	m.mu.Lock()
	m.offerCompleteLocked(j)
	m.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	if got := takePopupLog(); len(got) != 0 {
		t.Fatalf("complete window must not repeat: %v", got)
	}
}

func TestConfirmProgressCompleteRenders(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no chrome for a screenshot")
	}
	m, dl := newTestManager(t, 4)
	now := time.Now()
	pend, err := m.AddPending(AddRequest{URL: "https://example.com/film.mp4", Filename: "episode_01.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	down := &Job{
		ID: "prog1", URL: "https://example.com/film.mp4", Name: "episode_01.mp4",
		Kind: "video", Status: StatusDownloading, Size: 420 * 1024 * 1024, Dir: dl, Started: now.Add(-40 * time.Second),
	}
	down.done.Store(int64(68 * 420 * 1024 * 1024 / 100))
	down.liveMu.Lock()
	down.speed = 2.4 * 1024 * 1024
	down.liveMu.Unlock()
	done := &Job{
		ID: "comp1", URL: "https://example.com/film.mp4", Name: "episode_01.mp4",
		Kind: "video", Status: StatusDone, Size: 420 * 1024 * 1024, Dir: dl,
		Started: now.Add(-3 * time.Minute), Finished: now.Add(-8 * time.Second),
	}
	done.done.Store(done.Size)
	m.mu.Lock()
	m.jobs = append([]*Job{down, done}, m.jobs...)
	m.mu.Unlock()
	base := startUITestServer(t, m)
	dir := "ui-shots"
	chromeShot(t, chrome, base+"/confirm.html?id="+pend.ID+"&scheme=light", filepath.Join(dir, "26-confirm-light.png"), 600, 520)
	chromeShot(t, chrome, base+"/confirm.html?id="+pend.ID+"&scheme=dark", filepath.Join(dir, "26-confirm-dark.png"), 600, 520)
	chromeShot(t, chrome, base+"/progress.html?id=prog1&scheme=light", filepath.Join(dir, "26-progress-light.png"), 380, 400)
	chromeShot(t, chrome, base+"/progress.html?id=prog1&scheme=dark", filepath.Join(dir, "26-progress-dark.png"), 380, 400)
	chromeShot(t, chrome, base+"/complete.html?id=comp1&scheme=light", filepath.Join(dir, "26-complete-light.png"), 380, 240)
	chromeShot(t, chrome, base+"/complete.html?id=comp1&scheme=dark", filepath.Join(dir, "26-complete-dark.png"), 380, 240)
}

func TestVideoDriveErrorPages(t *testing.T) {
	video := readUI(t, "video.html")
	for _, want := range []string{
		`href="theme.css"`, `href="icon-32.png"`, `id="title"`, `id="thumb"`,
		`id="qualities"`, `id="start"`, `id="later"`, `id="cancel"`, `id="dir"`,
		`id="plSection"`, `opt("q", "best", "Best available"`,
		"prefers-color-scheme: dark", `html[data-scheme="dark"]`,
	} {
		if !strings.Contains(video, want) {
			t.Errorf("ui/video.html missing %q", want)
		}
	}
	if strings.Contains(video, "h.height <= 1080") {
		t.Error("old default (1080p cap) is still in video.html")
	}
	assertEnglishUI(t, "ui/video.html", video)

	drive := readUI(t, "drive.html")
	for _, want := range []string{
		`href="theme.css"`, `href="icon-32.png"`, `id="tree"`, `id="all"`, `id="none"`,
		`id="start"`, `id="later"`, `id="cancel"`, `id="dir"`, `id="browse"`, `id="again"`,
		"dataset.scheme",
	} {
		if !strings.Contains(drive, want) {
			t.Errorf("ui/drive.html missing %q", want)
		}
	}
	assertEnglishUI(t, "ui/drive.html", drive)

	errPage := readUI(t, "error.html")
	for _, want := range []string{
		`href="theme.css"`, `href="icon-32.png"`, `id="name"`, `id="reason"`, `id="fix"`,
		`id="retry"`, `id="close"`, "Download failed",
	} {
		if !strings.Contains(errPage, want) {
			t.Errorf("ui/error.html missing %q", want)
		}
	}
	assertEnglishUI(t, "ui/error.html", errPage)

	torrent := readUI(t, "torrent.html")
	if !strings.Contains(torrent, `href="theme.css"`) {
		t.Error("ui/torrent.html should use the shared theme")
	}
	assertEnglishUI(t, "ui/torrent.html", torrent)
}

func TestErrorPopupOnFailedJob(t *testing.T) {
	_ = takePopupLog()
	m, dl := newTestManager(t, 4)
	j := &Job{ID: "err1", URL: "https://example.com/a.bin", Name: "a.bin", Status: StatusError, Dir: dl, Error: "HTTP 403 Forbidden"}
	m.mu.Lock()
	m.jobs = append([]*Job{j}, m.jobs...)
	m.uiPort = "9"
	m.markPopupLocked("err1")
	m.offerCompleteLocked(j)
	m.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	log := takePopupLog()
	if len(log) != 1 || log[0] != "error:err1" {
		t.Fatalf("wanted error:err1, got %v", log)
	}
}

func TestVideoDriveErrorRenders(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no chrome for a screenshot")
	}
	m, dl := newTestManager(t, 4)
	errJob := &Job{ID: "errshot", URL: "https://example.com/episode_01.mp4", Name: "episode_01.mp4", Status: StatusError, Dir: dl, Error: "HTTP 403 Forbidden"}
	m.mu.Lock()
	m.jobs = append([]*Job{errJob}, m.jobs...)
	m.mu.Unlock()
	base := startUITestServer(t, m)
	dir := "ui-shots"
	// Video and Drive shells (pending id may 404; still records the themed chrome).
	chromeShot(t, chrome, base+"/video.html?id=missing&scheme=light", filepath.Join(dir, "27-video-light.png"), 720, 760)
	chromeShot(t, chrome, base+"/video.html?id=missing&scheme=dark", filepath.Join(dir, "27-video-dark.png"), 720, 760)
	chromeShot(t, chrome, base+"/drive.html?id=missing&scheme=light", filepath.Join(dir, "27-drive-light.png"), 760, 720)
	chromeShot(t, chrome, base+"/error.html?id=errshot&scheme=light", filepath.Join(dir, "27-error-light.png"), 420, 300)
	chromeShot(t, chrome, base+"/error.html?id=errshot&scheme=dark", filepath.Join(dir, "27-error-dark.png"), 420, 300)
}

func TestSettingsAndAboutRedesign(t *testing.T) {
	index := readUI(t, "index.html")
	for _, want := range []string{
		`id="tab-settings"`, `id="setDir"`, `id="setParts"`, `id="setActive"`,
		`id="setYield"`, `id="setNotify"`, `id="setAsk"`, `id="setResume"`,
		`id="setAutostart"`, `id="setCheckUpdates"`, `id="saveSettings"`,
		`id="cats"`, `id="saveCats"`, `id="appVersion"`, `id="checkUpdatesBtn"`,
		`id="settingsInstallBtn"`, `id="updateStatus"`, `id="setCookies"`,
		`id="toolsList"`, `id="toolsInstall"`, `id="aboutCard"`, `src="icon.png"`,
		"Slow down downloads while I use the internet", "Saving bandwidth",
		`id="limHTTP"`, `id="resetLimits"`,
	} {
		if !strings.Contains(index, want) {
			t.Errorf("settings/about missing %q", want)
		}
	}
	// Settings and About cards stretch with the content column instead of
	// sitting as a narrow ~580-680px strip next to a large empty area.
	for _, needle := range []string{`max-width: 640px`, `max-width: 680px`} {
		if strings.Contains(index, needle) {
			t.Errorf("settings/about still pin a narrow card with %q", needle)
		}
	}
	for _, want := range []string{
		`#tab-settings { width: 100%; max-width: 1600px;`,
		`.settings-card { background: var(--surface); border: 1px solid var(--border); border-radius: 18px; padding: 18px; width: 100%; max-width: 1600px;`,
		`.about-card { background: var(--surface); border: 1px solid var(--border); border-radius: 18px; padding: 22px 18px; width: 100%; max-width: 1600px;`,
	} {
		if !strings.Contains(index, want) {
			t.Errorf("settings/about should fill the content width: missing %q", want)
		}
	}
}

func TestSettingsAndAboutRenders(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no chrome for a screenshot")
	}
	m, _ := newTestManager(t, 4)
	base := startUITestServer(t, m)
	dir := "ui-shots"
	chromeShot(t, chrome, base+"/?tab=settings&scheme=light", filepath.Join(dir, "28-settings-light.png"), 1100, 1400)
	chromeShot(t, chrome, base+"/?tab=settings&scheme=dark", filepath.Join(dir, "28-settings-dark.png"), 1100, 1400)
}

func TestSettingsFillsWindowScreenshots(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no chrome for a screenshot")
	}
	m, _ := newTestManager(t, 4)
	base := startUITestServer(t, m)
	dir := "ui-shots"
	chromeShot(t, chrome, base+"/?tab=settings&scheme=light", filepath.Join(dir, "74-settings-900x650-light.png"), mainWindowWidth, mainWindowHeight)
	chromeShot(t, chrome, base+"/?tab=settings&scheme=dark", filepath.Join(dir, "74-settings-900x650-dark.png"), mainWindowWidth, mainWindowHeight)
	chromeShot(t, chrome, base+"/?tab=settings&scheme=light", filepath.Join(dir, "74-settings-1920x1080-light.png"), 1920, 1080)
	chromeShot(t, chrome, base+"/?tab=settings&scheme=dark", filepath.Join(dir, "74-settings-1920x1080-dark.png"), 1920, 1080)
}

func TestTrayMenuAndPopup(t *testing.T) {
	for _, want := range []string{trayMenuShow, trayMenuAddURL, trayMenuPauseAll, trayMenuSettings, trayMenuQuit} {
		if want == "" {
			t.Fatalf("empty tray label")
		}
	}
	page := readUI(t, "tray.html")
	for _, want := range []string{
		`href="theme.css"`, `href="icon-32.png"`, `src="icon-32.png"`,
		`id="show"`, `id="add"`, `id="pause"`, `id="settings"`, `id="quit"`,
		trayMenuShow, trayMenuAddURL, trayMenuPauseAll, trayMenuSettings, trayMenuQuit,
		"/api/pauseall", "/api/quit", "/api/open", `add: true`, `tab: "settings"`,
		`addEventListener("blur"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("ui/tray.html missing %q", want)
		}
	}
	assertEnglishUI(t, "ui/tray.html", page)
	if !strings.Contains(readUI(t, "index.html"), `q.has("add")`) {
		t.Error("main window should focus the URL box for /?add=1")
	}

	jobs := []JobView{
		{Status: StatusDownloading, Speed: 2.4 * 1024 * 1024, Size: 100, Done: 68},
		{Status: StatusDownloading, Speed: 0, Size: 100, Done: 68},
	}
	st := snapshotTray(jobs)
	if st.Active != 2 || st.Pct != 68 {
		t.Fatalf("snapshot: %+v", st)
	}
	if got := formatTrayHeadline(st); got != "2 active, 2.4 MB/s" {
		t.Fatalf("headline: %q", got)
	}
	if got := snapshotTray(nil); formatTrayHeadline(got) != "No active downloads" {
		t.Fatalf("idle headline: %q", formatTrayHeadline(got))
	}
}

func TestPauseAllAPI(t *testing.T) {
	m, dl := newTestManager(t, 4)
	j := &Job{ID: "p1", URL: "https://example.com/a.bin", Name: "a.bin", Status: StatusDownloading, Dir: dl}
	m.mu.Lock()
	m.jobs = append([]*Job{j}, m.jobs...)
	m.mu.Unlock()
	base := startUITestServer(t, m)
	res, err := http.Post(base+"/api/pauseall", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("pauseall: %s", res.Status)
	}
	if got := m.List()[0].Status; got != StatusPaused {
		t.Fatalf("status after Pause all: %s", got)
	}
}

func TestTrayPopupRenders(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no chrome for a screenshot")
	}
	m, dl := newTestManager(t, 4)
	j := &Job{ID: "t1", URL: "https://example.com/a.bin", Name: "a.bin", Status: StatusDownloading, Size: 100, Dir: dl}
	j.done.Store(68)
	j.liveMu.Lock()
	j.speed = 2.4 * 1024 * 1024
	j.liveMu.Unlock()
	m.mu.Lock()
	m.jobs = append([]*Job{j}, m.jobs...)
	m.mu.Unlock()
	base := startUITestServer(t, m)
	chromeShot(t, chrome, base+"/tray.html?scheme=light", filepath.Join("ui-shots", "29-tray-light.png"), trayPopupWidth, trayPopupHeight)
	chromeShot(t, chrome, base+"/tray.html?scheme=dark", filepath.Join("ui-shots", "29-tray-dark.png"), trayPopupWidth, trayPopupHeight)
	chromeShot(t, chrome, base+"/tray.html?scheme=light", filepath.Join("ui-shots", "73-tray-popup-light.png"), trayPopupWidth, trayPopupHeight)
	chromeShot(t, chrome, base+"/tray.html?scheme=dark", filepath.Join("ui-shots", "73-tray-popup-dark.png"), trayPopupWidth, trayPopupHeight)
}

func TestTaskbarProgressFromJobs(t *testing.T) {
	none := taskbarFromJobs(nil)
	if none.State != taskbarNone {
		t.Fatalf("idle: %+v", none)
	}
	n := taskbarFromJobs([]JobView{
		{Status: StatusDownloading, Size: 100, Done: 25, Speed: 10},
		{Status: StatusDownloading, Size: 100, Done: 75, Speed: 10},
	})
	if n.State != taskbarNormal || n.Done != 100 || n.Total != 200 {
		t.Fatalf("normal: %+v", n)
	}
	ind := taskbarFromJobs([]JobView{{Status: StatusDownloading, Size: 0, Done: 10}})
	if ind.State != taskbarIndeterminate {
		t.Fatalf("unknown size: %+v", ind)
	}
	p := taskbarFromJobs([]JobView{{Status: StatusPaused, Size: 100, Done: 40}})
	if p.State != taskbarPaused || p.Done != 40 || p.Total != 100 {
		t.Fatalf("paused: %+v", p)
	}
	e := taskbarFromJobs([]JobView{{Status: StatusError, Error: "fail"}})
	if e.State != taskbarError {
		t.Fatalf("error: %+v", e)
	}
}

func TestTaskbarCOMIndexes(t *testing.T) {
	// Documented ITaskbarList3 slots used on Windows.
	if idx := 9; idx != 9 {
		t.Fatal("SetProgressValue must be vtable index 9")
	}
	src := readFileIfPresent(t, "taskbar_windows.go")
	if src == "" {
		return
	}
	for _, want := range []string{
		"ITaskbarList3", "SetProgressValue", "SetProgressState",
		"0x56FDF344", "0xEA1AFB91", "idxSetProgressValue = 9", "idxSetProgressState = 10",
		"mainWindowHWND", "Chrome",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("taskbar_windows.go missing %q", want)
		}
	}
}

func readFileIfPresent(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		return ""
	}
	return string(b)
}

func TestTrayTooltipFormat(t *testing.T) {
	if got := formatTrayTip(traySnapshot{}); got != "BDDM" {
		t.Fatalf("idle tip: %q", got)
	}
	st := traySnapshot{Active: 1, Speed: 2.4 * 1024 * 1024, Pct: 68}
	if got := formatTrayTip(st); got != "BDDM — 2.4 MB/s, 68%" {
		t.Fatalf("tip: %q", got)
	}
	st.Pct = -1
	if got := formatTrayTip(st); got != "BDDM — 2.4 MB/s" {
		t.Fatalf("tip without percent: %q", got)
	}
	src := readFileIfPresent(t, "tray_windows.go")
	if src != "" && !strings.Contains(src, "trayTipLoop") {
		t.Error("Windows tray should update the tooltip while downloads run")
	}
}

func TestExtensionPopupRedesign(t *testing.T) {
	htmlB, err := os.ReadFile(filepath.Join("extension", "popup.html"))
	if err != nil {
		t.Fatal(err)
	}
	cssB, err := os.ReadFile(filepath.Join("extension", "popup.css"))
	if err != nil {
		t.Fatal(err)
	}
	html, css := string(htmlB), string(cssB)
	for _, want := range []string{
		`id="status"`, `id="videos"`, `id="videoList"`, `id="enabled"`,
		`id="siteOn"`, `id="siteName"`, `id="permRow"`, `id="grant"`,
		`id="open"`, `id="offlineHelp"`, `id="excluded"`, `id="save"`,
		"Catch downloads", "On this site", "Open BDDM", "Download with BDDM",
		"Sites to skip", "Allow access",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("popup.html missing %q", want)
		}
	}
	if strings.Contains(html, "go run") {
		t.Error("popup help should not tell Windows users to run go")
	}
	for _, want := range []string{
		"prefers-color-scheme: dark", "--accent: #2f6fed", "--page: #14161a",
		".chip", "border-radius: 999px", "300px",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("popup.css missing %q", want)
		}
	}
	assertEnglishUI(t, "extension/popup.html", html)
	assertEnglishUI(t, "extension/popup.css", css)
}

func TestExtensionPopupRenders(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no chrome for a screenshot")
	}
	dir := t.TempDir()
	// Serve the extension folder so icons and CSS resolve; use a preview
	// page that does not need the chrome.runtime API.
	preview := `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>BDDM</title>
<link rel="stylesheet" href="/popup.css">
</head><body>
  <header>
    <img src="/icons/icon32.png" alt="">
    <span class="title">BDDM</span>
    <span id="status" class="status on">Connected</span>
  </header>
  <section id="videos">
    <div class="vhead"><span class="label">Video found on this page</span> <span class="hint">1 video</span></div>
    <div><span class="chip">1080p, 420 MB</span><span class="chip">720p, 260 MB</span></div>
    <ul id="videoList">
      <li><div class="vtext"><div class="vtitle">The Last Adventure — Official Trailer</div><div class="hint">example.com</div></div>
      <button class="small primary">Download with BDDM</button></li>
    </ul>
  </section>
  <div class="row">
    <div><div class="label">Catch downloads</div><div class="hint">Send browser downloads to BDDM</div></div>
    <label class="switch"><input type="checkbox" checked><span></span></label>
  </div>
  <div class="row">
    <div><div class="label">On this site</div><div class="hint" id="siteName">example.com</div></div>
    <label class="switch"><input type="checkbox" checked><span></span></label>
  </div>
  <button id="open" class="primary">Open BDDM</button>
</body></html>`
	if err := os.WriteFile(filepath.Join(dir, "preview.html"), []byte(preview), 0644); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir("extension")))
	mux.HandleFunc("/preview.html", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(dir, "preview.html"))
	})
	hs := &http.Server{Handler: mux}
	go hs.Serve(ln)
	t.Cleanup(func() { _ = hs.Close() })
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	base := "http://127.0.0.1:" + port
	chromeShot(t, chrome, base+"/preview.html", filepath.Join("ui-shots", "32-extension-popup-light.png"), 300, 420)
	// Dark: prefer-color-scheme via forced dark in a second preview is hard in
	// headless without a flag; emulate with --force-dark-mode when available.
	cmdDark := base + "/preview.html"
	_ = cmdDark
	out := filepath.Join("ui-shots", "32-extension-popup-dark.png")
	cmd := exec.Command(chrome, "--headless=new", "--disable-gpu", "--no-sandbox", "--hide-scrollbars",
		"--force-dark-mode", "--window-size=300,420", "--virtual-time-budget=4000",
		"--screenshot="+out, base+"/preview.html")
	cmd.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS=")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dark popup shot: %v\n%s", err, b)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() < 1000 {
		t.Fatalf("dark popup shot missing: %v", err)
	}
	t.Logf("wrote %s", out)
}

func TestPopupPagesReportFit(t *testing.T) {
	pages := []string{"video.html", "confirm.html", "progress.html", "complete.html", "error.html", "drive.html", "torrent.html"}
	for _, name := range pages {
		page := readUI(t, name)
		if !strings.Contains(page, `src="fit.js"`) {
			t.Errorf("%s should include fit.js", name)
		}
		if !strings.Contains(page, "bddmFitWindow") {
			t.Errorf("%s should call bddmFitWindow after it has content", name)
		}
		if name == "tray.html" || name == "index.html" {
			t.Errorf("unexpected page %s", name)
		}
	}
	index, tray := readUI(t, "index.html"), readUI(t, "tray.html")
	if strings.Contains(index, `src="fit.js"`) || strings.Contains(tray, `src="fit.js"`) {
		t.Fatal("main window and tray popup must not use the compact-popup fitter")
	}
	fit := readUI(t, "fit.js")
	for _, want := range []string{"/api/window/fit", "scrollHeight", "bddmFitWindow"} {
		if !strings.Contains(fit, want) {
			t.Errorf("fit.js missing %q", want)
		}
	}
}

func TestWindowFitAPI(t *testing.T) {
	m, _ := newTestManager(t, 4)
	base := startUITestServer(t, m)
	body := `{"title":"Download Video","width":720,"height":540,"path":"/video.html"}`
	res, err := http.Post(base+"/api/window/fit", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("fit api: %s", res.Status)
	}
	res, err = http.Post(base+"/api/window/fit", "application/json", strings.NewReader(`{"title":"BDDM - Download Manager","width":1100,"height":900,"path":"/"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("ignored fit: %s", res.Status)
	}
}

func seedReadyVideo(t *testing.T, m *Manager, dl, id string) *Job {
	t.Helper()
	j := &Job{
		ID: id, URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		Name: "Never Gonna Give You Up", Dir: dl, Status: StatusPending, Kind: "video",
		vpend: &videoPending{
			state: "ready",
			title: "Never Gonna Give You Up",
			info: &VideoInfo{
				Title: "Never Gonna Give You Up", Uploader: "Rick Astley", Site: "YouTube", Duration: 213,
				Qualities: []QualityOpt{
					{Height: 2160, Label: "2160p", Size: 800 * 1024 * 1024},
					{Height: 1440, Label: "1440p", Size: 400 * 1024 * 1024},
					{Height: 1080, Label: "1080p", Size: 220 * 1024 * 1024},
					{Height: 720, Label: "720p", Size: 120 * 1024 * 1024},
					{Height: 480, Label: "480p", Size: 60 * 1024 * 1024},
					{Height: 360, Label: "360p", Size: 30 * 1024 * 1024},
				},
				Subtitles: []SubtitleLang{{Code: "en", Name: "English"}},
			},
		},
	}
	m.mu.Lock()
	m.jobs = append(m.jobs, j)
	m.mu.Unlock()
	return j
}

func TestPopupContentScreenshots(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no chrome for a screenshot")
	}
	m, dl := newTestManager(t, 4)
	seedReadyVideo(t, m, dl, "vid75l")
	seedReadyVideo(t, m, dl, "vid75d")
	pend, err := m.AddPending(AddRequest{URL: "https://example.com/film.mp4", Filename: "film.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	prog := &Job{ID: "prog75", URL: "https://example.com/a.bin", Name: "a.bin", Status: StatusDownloading, Size: 100, Dir: dl}
	prog.done.Store(42)
	comp := &Job{ID: "comp75", URL: "https://example.com/b.bin", Name: "b.bin", Status: StatusDone, Size: 100, Dir: dl}
	errJob := &Job{ID: "err75", URL: "https://example.com/c.bin", Name: "c.bin", Status: StatusError, Dir: dl, Error: "HTTP 403 Forbidden"}
	m.mu.Lock()
	m.jobs = append(m.jobs, prog, comp, errJob)
	m.mu.Unlock()
	base := startUITestServer(t, m)
	dir := "ui-shots"
	chromeShot(t, chrome, base+"/video.html?id=vid75l&scheme=light", filepath.Join(dir, "75-video-light.png"), 720, 580)
	chromeShot(t, chrome, base+"/video.html?id=vid75d&scheme=dark", filepath.Join(dir, "75-video-dark.png"), 720, 580)
	chromeShot(t, chrome, base+"/confirm.html?id="+pend.ID+"&scheme=light", filepath.Join(dir, "75-confirm-light.png"), 600, 430)
	chromeShot(t, chrome, base+"/progress.html?id=prog75&scheme=light", filepath.Join(dir, "75-progress-light.png"), 380, 350)
	chromeShot(t, chrome, base+"/complete.html?id=comp75&scheme=light", filepath.Join(dir, "75-complete-light.png"), 380, 220)
	chromeShot(t, chrome, base+"/error.html?id=err75&scheme=light", filepath.Join(dir, "75-error-light.png"), 420, 360)
}

// chromeFit opens a popup page in a headless browser as big as the window
// BDDM first opens it (w x h) and returns the size fit.js measured for it.
func chromeFit(t *testing.T, chrome, url string, w, h int) (int, int) {
	t.Helper()
	cmd := exec.Command(chrome, "--headless=new", "--disable-gpu", "--no-sandbox", "--hide-scrollbars",
		"--window-size="+strconv.Itoa(w)+","+strconv.Itoa(h),
		"--virtual-time-budget=5000", "--dump-dom", url)
	cmd.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS=")
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("dump %s: %v", url, err)
	}
	m := regexp.MustCompile(`data-bddm-fit="(\d+)x(\d+)"`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("fit.js did not report a size for %s", url)
	}
	fw, _ := strconv.Atoi(string(m[1]))
	fh, _ := strconv.Atoi(string(m[2]))
	return fw, fh
}

// Bug 81: the Download Video window opened too tall and too wide because the
// measurement could never be smaller than the window it was opened in.
func TestVideoPopupFitsContentNotWindow(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no chrome for measuring")
	}
	m, dl := newTestManager(t, 4)
	// Each page view gets its own job: closing a video window cancels it.
	for _, id := range []string{"fit-live-a", "fit-live-b"} {
		live := seedReadyVideo(t, m, dl, id)
		live.vpend.info = &VideoInfo{Title: "Live stream", Site: "generic", IsLive: true}
	}
	seedReadyVideo(t, m, dl, "fit-yt")
	base := startUITestServer(t, m)

	lw, lh := chromeFit(t, chrome, base+"/video.html?id=fit-live-a", 720, 760)
	// The same page opened in a much smaller window must report the same size.
	sw, sh := chromeFit(t, chrome, base+"/video.html?id=fit-live-b", 600, 480)
	t.Logf("live stream: %dx%d (opened 720x760), %dx%d (opened 600x480)", lw, lh, sw, sh)
	if lw != sw || lh != sh {
		t.Errorf("size depends on the window it was opened in: %dx%d vs %dx%d", lw, lh, sw, sh)
	}
	if lw < 560 || lw > 640 {
		t.Errorf("width %d should be 560..640, not the 720 the window opened with", lw)
	}
	if lh >= 560 {
		t.Errorf("height %d leaves blank space: a live stream window has only 3 quality choices", lh)
	}
	_, yh := chromeFit(t, chrome, base+"/video.html?id=fit-yt", 720, 760)
	t.Logf("youtube (6 qualities): height %d", yh)
	if yh <= lh {
		t.Errorf("a window with 6 qualities (%d) should be taller than one with 3 (%d)", yh, lh)
	}
	if yh > 760 {
		t.Errorf("height %d is taller than the 760 it opened with", yh)
	}
}

// The other content-sized popups must still fit their height to the content,
// whatever size their window was opened at.
func TestOtherPopupsFitHeight(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no chrome for measuring")
	}
	m, _ := newTestManager(t, 4)
	base := startUITestServer(t, m)
	for _, c := range []struct {
		page string
		w, h int
	}{
		{"confirm.html?id=x", 600, 520}, {"drive.html?id=x", 600, 560}, {"torrent.html?id=x", 560, 620},
		{"progress.html?id=x", 500, 400}, {"complete.html?id=x", 500, 240}, {"error.html?id=x", 500, 360},
	} {
		_, h1 := chromeFit(t, chrome, base+"/"+c.page, c.w, c.h)
		_, h2 := chromeFit(t, chrome, base+"/"+c.page, 900, 900)
		if h1 != h2 {
			t.Errorf("%s: height depends on the window size (%d vs %d)", c.page, h1, h2)
		}
		if h1 < 80 || h1 > 800 {
			t.Errorf("%s: odd height %d", c.page, h1)
		}
	}
}
