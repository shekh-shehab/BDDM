package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Items 83 and 84: the main window sidebar lists All / Active / Completed /
// Error / Torrents (not file-type categories). The top cards are Speed,
// Time left and Upload. Time left is the longest ETA among running jobs.

func TestSidebarFiltersReplaceFileTypes(t *testing.T) {
	index := readUI(t, "index.html")
	for _, want := range []string{
		`data-filter="all" data-tab="downloads">All`,
		`data-filter="active" data-tab="downloads">Active`,
		`data-filter="done" data-tab="downloads">Completed`,
		`data-filter="error" data-tab="downloads">Error`,
		`data-filter="torrent" data-tab="downloads">Torrents`,
		`id="statTimeLeft"`,
		`id="statUpload"`,
		">Time left<",
		">Upload<",
		"Folders by file type",
		`id="cats"`,
	} {
		if !strings.Contains(index, want) {
			t.Errorf("ui/index.html missing %q", want)
		}
	}
	for _, old := range []string{
		`data-filter="Video"`,
		`data-filter="Audio"`,
		`data-filter="Documents"`,
		`data-filter="Programs"`,
		`id="navVideo"`,
		`id="navAudio"`,
		`id="navDocuments"`,
		`id="navPrograms"`,
		`id="statActive"`,
		"Completed today",
	} {
		if strings.Contains(index, old) {
			t.Errorf("ui/index.html should no longer contain %q", old)
		}
	}
	assertEnglishUI(t, "ui/index.html", index)
}

func seedSidebarJobs(t *testing.T, m *Manager, dl string) {
	t.Helper()
	now := time.Now()
	parts := &SegInfo{Kind: segKindParts, Total: 8, Finished: 3, Active: 3, Retrying: 1, Segs: []SegView{
		{W: 100, Done: 100, State: segDone}, {W: 100, Done: 100, State: segDone}, {W: 60, Done: 60, State: segDone},
		{W: 40, Done: 15, State: segActive}, {W: 100, Done: 55, State: segActive}, {W: 100, Done: 30, State: segActive},
		{W: 100, Done: 12, State: segRetry}, {W: 100, Done: 0, State: segWait},
	}}
	state := make([]int8, 400)
	for i := 0; i < 210; i++ {
		state[i] = fragDone
	}
	state[210], state[211], state[212] = fragBusy, fragBusy, fragBusy
	state[213] = fragRetry
	frags := hlsSegInfo(state, "video")

	mk := func(id, name, url, kind, status string, ago time.Duration, size, done int64) *Job {
		j := &Job{ID: id, URL: url, Name: name, Kind: kind, Status: status, Size: size, Dir: dl, Started: now.Add(-ago)}
		j.done.Store(done)
		return j
	}
	iso := mk("r-parts", "Ubuntu-desktop-amd64.iso", "https://example.com/ubuntu.iso", "", StatusDownloading, time.Minute, 5<<30, 2<<30)
	iso.liveMu.Lock()
	iso.speed = 6.2 * 1024 * 1024
	iso.liveMu.Unlock()
	iso.setSegs(parts)

	hls := mk("r-hls", "Live event recording.mp4", "https://example.com/live.m3u8", "video", StatusDownloading, 2*time.Minute, 0, 310<<20)
	hls.liveMu.Lock()
	hls.speed = 3.1 * 1024 * 1024
	hls.liveMu.Unlock()
	hls.setSegs(frags)

	// Remaining bytes and speed chosen so the ETA is 29m 40s (1780s), the
	// longest of the running jobs, which is what Time left should show.
	const torEta = int64(29*60 + 40)
	torSpeed := 1.8 * 1024 * 1024
	torDone := int64(2362232012)
	torSize := torDone + int64(float64(torEta)*torSpeed)
	tor := mk("r-tor", "ubuntu-24.04.1-desktop-amd64", "magnet:?xt=urn:btih:abc", kindTorrent, StatusDownloading, 30*time.Minute, torSize, torDone)
	tor.Torrent = &TorrentInfo{Files: 1}
	tor.liveMu.Lock()
	tor.speed, tor.upSpeed, tor.peers = torSpeed, 120*1024, 12
	tor.liveMu.Unlock()

	pdf := mk("c-pdf", "User Guide.pdf", "https://example.com/guide.pdf", "", StatusDone, 2*time.Hour, 2488320, 2488320)
	pdf.Finished = now.Add(-5 * time.Minute)

	zip := mk("c-zip", "Family-photos.zip", "https://example.com/photos.zip", "", StatusDone, 8*time.Hour, 1845493760, 1845493760)
	zip.Finished = now.Add(-7 * time.Hour)

	oldTor := mk("c-tor", "ubuntu-22.04.5-desktop-amd64", "magnet:?xt=urn:btih:def", kindTorrent, StatusDone, 27*time.Hour, 4764729344, 4764729344)
	oldTor.Torrent = &TorrentInfo{Files: 1, Uploaded: 890 << 20}
	oldTor.Finished = now.Add(-25 * time.Hour)

	errJob := mk("e-zip", "archive.zip", "https://example.com/missing.zip", "", StatusError, 3*time.Hour, 0, 0)
	errJob.Error = "The server returned 404."

	paused := mk("p-mp3", "Midnight Drive.mp3", "https://example.com/song.mp3", "", StatusPaused, 40*time.Minute, 9437184, 3145728)

	m.mu.Lock()
	m.jobs = append([]*Job{iso, hls, tor, pdf, zip, oldTor, errJob, paused}, m.jobs...)
	m.mu.Unlock()
}

func fmtETATest(s int64) string {
	if s < 0 {
		return "-"
	}
	h, m, sec := s/3600, (s%3600)/60, s%60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}

func dumpSidebar(t *testing.T, chrome, base, filter string) string {
	t.Helper()
	url := base + "/?scheme=light"
	if filter != "" {
		url += "&filter=" + filter
	}
	cmd := execChromeDump(chrome, url, mainWindowWidth, mainWindowHeight)
	b, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSidebarStatusFiltersRenderAndScreenshot(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no chrome for a screenshot")
	}
	m, dl := newTestManager(t, 4)
	seedSidebarJobs(t, m, dl)
	base := startUITestServer(t, m)

	// Time left is the longest ETA among running jobs (the torrent).
	var longest int64 = -1
	for _, id := range []string{"r-parts", "r-hls", "r-tor"} {
		v := jobView(m, id)
		if v.Status == StatusDownloading && v.ETA > longest {
			longest = v.ETA
		}
	}
	wantTimeLeft := fmtETATest(longest)
	if wantTimeLeft == "-" || !strings.Contains(wantTimeLeft, "29m") {
		t.Fatalf("seeded torrent ETA should be about 29m 40s, got %q (eta=%d)", wantTimeLeft, longest)
	}

	active := dumpSidebar(t, chrome, base, "active")
	for _, want := range []string{
		`data-filter="active"`,
		`id="navAll">8<`,
		`id="navActive">3<`,
		`id="navDone">3<`,
		`id="navError">1<`,
		`id="navTorrent">2<`,
		`id="statTimeLeft">` + wantTimeLeft + `<`,
		`id="statUpload">`,
		"Ubuntu-desktop-amd64.iso",
		"Live event recording.mp4",
		"ubuntu-24.04.1-desktop-amd64",
		"Parts: 3 active, 1 retrying, 3 done",
		"Fragments (video): 210 of 400 done, 3 active, 1 retrying",
	} {
		if !strings.Contains(active, want) {
			t.Errorf("Active view missing %q", want)
		}
	}
	// The Active button is the selected one; file-type categories are gone.
	if !regexp.MustCompile(`data-filter="active"[^>]*class="nav on"`).MatchString(active) &&
		!regexp.MustCompile(`class="nav on"[^>]*data-filter="active"`).MatchString(active) {
		// class may be on the button as "nav on" with data-filter before or after.
		if !strings.Contains(active, `data-filter="active"`) || !strings.Contains(active, "Active") {
			t.Error("Active should be selected in the sidebar")
		} else if !strings.Contains(active, `class="nav on"`) {
			t.Error("a sidebar filter should have the selected style")
		}
	}
	for _, hidden := range []string{
		"User Guide.pdf", "Family-photos.zip", "ubuntu-22.04.5-desktop-amd64",
		"archive.zip", "Midnight Drive.mp3",
		`data-filter="Video"`, ">Videos<",
	} {
		if strings.Contains(active, hidden) {
			t.Errorf("Active view should not show %q", hidden)
		}
	}
	if strings.Contains(active, `id="statActive"`) {
		t.Error("the top Active card should be Time left, not Active")
	}
	assertEnglishUI(t, "active filter view", active)

	done := dumpSidebar(t, chrome, base, "done")
	for _, want := range []string{"User Guide.pdf", "Family-photos.zip", "ubuntu-22.04.5-desktop-amd64"} {
		if !strings.Contains(done, want) {
			t.Errorf("Completed view missing %q", want)
		}
	}
	for _, hidden := range []string{"Ubuntu-desktop-amd64.iso", "Live event recording.mp4", "archive.zip", "Midnight Drive.mp3"} {
		if strings.Contains(done, hidden) {
			t.Errorf("Completed view should not show %q", hidden)
		}
	}

	errView := dumpSidebar(t, chrome, base, "error")
	if !strings.Contains(errView, "archive.zip") {
		t.Error("Error view missing archive.zip")
	}
	if strings.Contains(errView, "Ubuntu-desktop-amd64.iso") || strings.Contains(errView, "User Guide.pdf") {
		t.Error("Error view should list only failed downloads")
	}

	torrents := dumpSidebar(t, chrome, base, "torrent")
	if !strings.Contains(torrents, "ubuntu-24.04.1-desktop-amd64") || !strings.Contains(torrents, "ubuntu-22.04.5-desktop-amd64") {
		t.Error("Torrents view should list both torrent downloads")
	}
	if strings.Contains(torrents, "Ubuntu-desktop-amd64.iso") || strings.Contains(torrents, "User Guide.pdf") {
		t.Error("Torrents view should list only torrents")
	}

	all := dumpSidebar(t, chrome, base, "all")
	for _, want := range []string{
		"Ubuntu-desktop-amd64.iso", "Live event recording.mp4", "ubuntu-24.04.1-desktop-amd64",
		"User Guide.pdf", "Family-photos.zip", "ubuntu-22.04.5-desktop-amd64",
		"archive.zip", "Midnight Drive.mp3",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("All view missing %q", want)
		}
	}

	// Nothing running: Time left is "-".
	idle, idleDir := newTestManager(t, 4)
	idleJob := &Job{ID: "idle-pdf", URL: "https://example.com/a.pdf", Name: "Notes.pdf", Status: StatusDone, Size: 100, Dir: idleDir, Started: time.Now().Add(-time.Hour), Finished: time.Now()}
	idleJob.done.Store(100)
	idle.mu.Lock()
	idle.jobs = append([]*Job{idleJob}, idle.jobs...)
	idle.mu.Unlock()
	idleBase := startUITestServer(t, idle)
	idleDOM := dumpSidebar(t, chrome, idleBase, "all")
	if !strings.Contains(idleDOM, `id="statTimeLeft">-<`) {
		t.Error("Time left should be '-' when nothing is running")
	}

	dir := filepath.Join("ui-shots")
	for _, theme := range []string{"light", "dark"} {
		out := filepath.Join(dir, "83-84-"+theme+".png")
		chromeShot(t, chrome, base+"/?scheme="+theme+"&filter=active", out, mainWindowWidth, mainWindowHeight)
	}
	if _, err := os.Stat(filepath.Join(dir, "83-84-light.png")); err != nil {
		t.Error(err)
	}
}
