package main

// A13: a stream (HLS) video continues where it stopped after Pause and
// after the internet connection was lost. Needs yt-dlp and ffmpeg (skipped
// without them).

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func needVideoTools(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		if home, _ := os.UserHomeDir(); fileExists(filepath.Join(home, "bin", "yt-dlp")) {
			t.Setenv("PATH", filepath.Join(home, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
	}
	for _, n := range []string{"yt-dlp", "ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(n); err != nil {
			t.Skip(n + " is not installed")
		}
	}
}

// hlsServer serves an HLS stream slowly and counts the pieces asked for.
type hlsServer struct {
	dir  string
	segs atomic.Int64
	srv  *http.Server
	addr string
}

func (h *hlsServer) start(t *testing.T, addr string) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	h.addr = ln.Addr().String()
	fs := http.FileServer(http.Dir(h.dir))
	h.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".ts") {
			h.segs.Add(1)
			time.Sleep(250 * time.Millisecond)
		}
		fs.ServeHTTP(w, r)
	})}
	go h.srv.Serve(ln)
}

func makeHLS(t *testing.T, seconds int) string {
	dir := t.TempDir()
	cmd := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=320x180:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440", "-t", strconv.Itoa(seconds), "-c:v", "libx264", "-preset", "ultrafast",
		"-c:a", "aac", "-g", "25", "-f", "hls", "-hls_time", "1", "-hls_list_size", "0",
		"-hls_segment_filename", filepath.Join(dir, "seg%03d.ts"), filepath.Join(dir, "index.m3u8"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	return dir
}

func addStreamJob(m *Manager, u, dir, title string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := &Job{ID: newID(), URL: u, Kind: "video", Stream: "hls", Name: title, Dir: dir, Parts: 4,
		Status: StatusQueued, Started: time.Now(), Video: &VideoOptions{Quality: "best", Title: title}}
	m.jobs = append(m.jobs, j)
	m.scheduleLocked()
	return j.ID
}

func videoSeconds(t *testing.T, p string) float64 {
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", p).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", p, err)
	}
	f, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return f
}

func TestVideoResumeHLS(t *testing.T) {
	needVideoTools(t)
	const secs = 30
	hs := &hlsServer{dir: makeHLS(t, secs)}
	hs.start(t, "127.0.0.1:0")
	defer func() { hs.srv.Close() }()
	m, dl := newTestManager(t, 4)
	if !m.tools.Ready() {
		t.Skip("video tools not found by BDDM")
	}

	// 1. Pause in the middle, then Resume.
	id := addStreamJob(m, "http://"+hs.addr+"/index.m3u8", dl, "Clip one")
	deadline := time.Now().Add(time.Minute)
	for hs.segs.Load() < 10 {
		if time.Now().After(deadline) {
			t.Fatalf("no progress: %+v", jobView(m, id))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := m.Pause(id); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, id, StatusPaused, 30*time.Second)
	m.mu.Lock()
	pj := m.findLocked(id)
	m.mu.Unlock()
	m.waitStopped(pj)
	paused := hs.segs.Load()
	if err := m.Resume(id); err != nil {
		t.Fatal(err)
	}
	v := waitDone(t, m, id, 2*time.Minute)
	if v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	total := hs.segs.Load()
	t.Logf("pause/resume: %d pieces before the pause, %d in all (stream has %d)", paused, total, secs)
	if total > secs+6 {
		t.Fatalf("started over: %d pieces downloaded for %d", total, secs)
	}
	if d := videoSeconds(t, filepath.Join(dl, "Clip one.mp4")); d < secs-1.5 {
		t.Fatalf("the video is %.1f s long, want %d", d, secs)
	}

	// 2. The connection drops in the middle and comes back.
	var online atomic.Bool
	online.Store(true)
	oldOnline, oldEvery := netOnline, onlineEvery
	netOnline = func(ctx context.Context, server string) bool { return online.Load() }
	onlineEvery = 200 * time.Millisecond
	defer func() { netOnline, onlineEvery = oldOnline, oldEvery }()
	hs.segs.Store(0)
	id = addStreamJob(m, "http://"+hs.addr+"/index.m3u8", dl, "Clip two")
	for hs.segs.Load() < 10 {
		time.Sleep(20 * time.Millisecond)
	}
	online.Store(false)
	hs.srv.Close()
	deadline = time.Now().Add(time.Minute)
	for !jobView(m, id).NetWait {
		if v := jobView(m, id); time.Now().After(deadline) || v.Status == StatusError {
			t.Fatalf("not waiting for the network: %s %q %q", v.Status, v.Message, v.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	before := hs.segs.Load()
	hs.start(t, hs.addr)
	online.Store(true)
	v = waitDone(t, m, id, 2*time.Minute)
	if v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	total = hs.segs.Load()
	t.Logf("network loss: %d pieces before, %d in all", before, total)
	if total > secs+8 {
		t.Fatalf("started over: %d pieces for %d", total, secs)
	}
	if d := videoSeconds(t, filepath.Join(dl, "Clip two.mp4")); d < secs-1.5 {
		t.Fatalf("the video is %.1f s long (pieces missing?), want %d", d, secs)
	}
}
