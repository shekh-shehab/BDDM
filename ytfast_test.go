package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseYouTubeLinks(t *testing.T) {
	info := map[string]any{
		"id": "abc", "live_status": "not_live",
		"requested_formats": []any{
			map[string]any{"format_id": "137", "ext": "mp4", "protocol": "https", "url": "https://v.test/v", "vcodec": "avc1", "acodec": "none",
				"filesize": 1000.0, "http_headers": map[string]any{"User-Agent": "UA1"}, "downloader_options": map[string]any{"http_chunk_size": 10485760.0}},
			map[string]any{"format_id": "140", "ext": "m4a", "protocol": "https", "url": "https://v.test/a", "vcodec": "none", "acodec": "mp4a"},
		},
	}
	js, _ := json.Marshal(info)
	out := "BDDMN|/x/Title [abc].mp4\n" + string(js) + "\n"
	r, err := parseYouTubeLinks([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if r.final != "/x/Title [abc].mp4" || len(r.streams) != 2 {
		t.Fatalf("bad: %+v", r)
	}
	v, a := r.streams[0], r.streams[1]
	if v.what != "video" || a.what != "audio" || v.id != "137" || a.ext != "m4a" || v.size != 1000 || v.chunk != 10485760 || v.headers["User-Agent"] != "UA1" {
		t.Errorf("bad streams: %+v %+v", v, a)
	}
	if got := fastTempFile(r.final, "audio", "m4a"); got != "/x/Title [abc].bddm-a.m4a" {
		t.Errorf("temp name %q", got)
	}

	// Not for the fast path: fragmented formats and live videos.
	info["requested_formats"].([]any)[0].(map[string]any)["protocol"] = "m3u8_native"
	js, _ = json.Marshal(info)
	if _, err := parseYouTubeLinks([]byte("BDDMN|/x/a.mp4\n" + string(js))); !errors.Is(err, errNotFast) {
		t.Errorf("m3u8: want errNotFast, got %v", err)
	}
	js, _ = json.Marshal(map[string]any{"format_id": "18", "ext": "mp4", "protocol": "https", "url": "https://v.test/x", "is_live": true})
	if _, err := parseYouTubeLinks([]byte("BDDMN|/x/a.mp4\n" + string(js))); !errors.Is(err, errNotFast) {
		t.Errorf("live: want errNotFast, got %v", err)
	}
	// A single (video+audio) format.
	js, _ = json.Marshal(map[string]any{"format_id": "18", "ext": "mp4", "protocol": "https", "url": "https://v.test/x", "vcodec": "avc1", "acodec": "mp4a"})
	r, err = parseYouTubeLinks([]byte("BDDMN|/x/a.mp4\r\n" + string(js) + "\r\n"))
	if err != nil || len(r.streams) != 1 || r.streams[0].what != "video" {
		t.Errorf("single: %+v %v", r, err)
	}
}

func TestSplitChunks(t *testing.T) {
	p := splitChunks(25, 10)
	if len(p) != 3 || p[0] != (Part{From: 0, To: 9}) || p[2] != (Part{From: 20, To: 24}) {
		t.Errorf("got %+v", p)
	}
	if p := splitChunks(10, 10); len(p) != 1 || p[0].To != 9 {
		t.Errorf("got %+v", p)
	}
}

// slowReader makes the test server slow enough to pause in the middle.
type slowReader struct {
	r     io.ReadSeeker
	delay time.Duration
}

func (s *slowReader) Read(p []byte) (int, error) {
	time.Sleep(s.delay)
	if len(p) > 16*1024 {
		p = p[:16*1024]
	}
	return s.r.Read(p)
}
func (s *slowReader) Seek(o int64, w int) (int64, error) { return s.r.Seek(o, w) }

// TestFastYouTubeEndToEnd runs the whole fast path against a local server
// and a fake yt-dlp: links that expire in the middle, a pause and resume,
// and joining with ffmpeg.
func TestFastYouTubeEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as yt-dlp")
	}
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found")
	}
	base := t.TempDir()
	src := filepath.Join(base, "src")
	os.MkdirAll(src, 0755)
	mk := func(args ...string) {
		cmd := exec.Command(ff, append([]string{"-loglevel", "error", "-y"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v %s", err, out)
		}
	}
	mk("-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25,noise=alls=40:allf=t", "-t", "4", "-c:v", "libx264", "-preset", "ultrafast", "-crf", "18", "-an", filepath.Join(src, "v.mp4"))
	mk("-f", "lavfi", "-i", "sine", "-t", "8", "-c:a", "aac", "-vn", filepath.Join(src, "a.m4a"))

	var gen1Reqs atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "yes" {
			http.Error(w, "missing header", 403)
			return
		}
		if r.URL.Query().Get("gen") == "1" && r.Method == http.MethodGet && r.Header.Get("Range") != "bytes=0-0" {
			if gen1Reqs.Add(1) > 3 {
				http.Error(w, "expired", 403)
				return
			}
		}
		f, err := os.Open(filepath.Join(src, filepath.Base(r.URL.Path)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		http.ServeContent(w, r, "", time.Time{}, &slowReader{r: f, delay: 3 * time.Millisecond})
	}))
	defer srv.Close()

	cfg := filepath.Join(base, "cfg")
	out := filepath.Join(base, "out")
	m, err := NewManager(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(out, "Test Video [abc].mp4")
	counter := filepath.Join(base, "gen")
	fmtJSON := func(id, ext, name, vc, ac string) string {
		return fmt.Sprintf(`{"format_id":"%s","ext":"%s","protocol":"https","url":"%s/%s?gen=GEN","vcodec":"%s","acodec":"%s","http_headers":{"X-Test":"yes"},"downloader_options":{"http_chunk_size":262144}}`,
			id, ext, srv.URL, name, vc, ac)
	}
	js := `{"id":"abc","live_status":"not_live","requested_formats":[` + fmtJSON("137", "mp4", "v.mp4", "avc1", "none") + "," + fmtJSON("140", "m4a", "a.m4a", "none", "mp4a") + `]}`
	script := "#!/bin/sh\nn=$(cat " + counter + " 2>/dev/null || echo 0); n=$((n+1)); echo $n > " + counter + "\n" +
		"echo \"$@\" >> " + filepath.Join(base, "args.log") + "\n" +
		"echo 'BDDMN|" + final + "'\n" +
		"echo '" + strings.ReplaceAll(js, "GEN", "'$n'") + "'\n"
	os.MkdirAll(filepath.Join(cfg, "tools"), 0755)
	if err := os.WriteFile(filepath.Join(cfg, "tools", "yt-dlp"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	j := &Job{ID: "t1", URL: "https://www.youtube.com/watch?v=abc", Kind: "video", Dir: out, Parts: 4,
		Status: StatusDownloading, Video: &VideoOptions{Quality: "1080", Title: "Test Video"}}
	m.mu.Lock()
	m.jobs = append(m.jobs, j)
	ok := m.useFastYouTubeLocked(j)
	m.mu.Unlock()
	if !ok {
		t.Fatal("fast path not chosen")
	}

	// Pause in the middle of the download.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for j.done.Load() < 600*1024 {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	err = m.runVideo(ctx, j)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want a pause, got %v", err)
	}
	if j.Fast == nil || j.Fast.Off || len(j.Fast.Streams) != 2 {
		t.Fatalf("bad fast state after pause: %+v", j.Fast)
	}
	if !fileExists(j.Fast.Streams[0].File+".part") && !fileExists(j.Fast.Streams[0].File) {
		t.Fatalf("no partial file kept")
	}
	pausedAt := j.done.Load()

	// "Restart": reload the saved job from history, then resume.
	m.mu.Lock()
	m.saveHistoryLocked()
	m.mu.Unlock()
	data, _ := os.ReadFile(filepath.Join(cfg, "history.json"))
	var saved []*Job
	if err := json.Unmarshal(data, &saved); err != nil || len(saved) != 1 || saved[0].Fast == nil {
		t.Fatalf("history: %v %s", err, data)
	}
	j2 := saved[0]
	j2.done.Store(j2.Done)
	sizes := map[int64]bool{}
	stopWatch := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		for {
			select {
			case <-stopWatch:
				return
			case <-time.After(2 * time.Millisecond):
				m.mu.Lock()
				if j2.Size > 0 {
					sizes[j2.Size] = true
				}
				m.mu.Unlock()
			}
		}
	}()
	err = m.runVideo(context.Background(), j2)
	close(stopWatch)
	<-watchDone
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	// The total stays the same while downloading (it only changes once at
	// the end, to the size of the joined file).
	if len(sizes) > 2 {
		t.Errorf("total changed during the download: %v", sizes)
	}
	if j2.Fast.Off {
		t.Fatal("fell back to yt-dlp")
	}
	if !fileExists(final) {
		t.Fatalf("final file missing")
	}
	for _, s := range j2.Fast.Streams {
		for _, p := range []string{s.File, s.File + ".part", s.File + ".bddm.json"} {
			if fileExists(p) {
				t.Errorf("left over: %s", p)
			}
		}
	}
	probe, err := exec.Command(strings.TrimSuffix(ff, "ffmpeg")+"ffprobe", "-v", "error", "-show_entries", "stream=codec_type", "-of", "csv=p=0", final).Output()
	if err == nil && !(strings.Contains(string(probe), "video") && strings.Contains(string(probe), "audio")) {
		t.Errorf("joined file streams: %q", probe)
	}
	want, _ := os.Stat(filepath.Join(src, "v.mp4"))
	if j2.Size <= want.Size() {
		t.Errorf("size %d looks wrong", j2.Size)
	}
	args, _ := os.ReadFile(filepath.Join(base, "args.log"))
	if !strings.Contains(string(args), "-f 137+140") {
		t.Errorf("resume did not ask for the same formats:\n%s", args)
	}
	t.Logf("paused at %d bytes; yt-dlp asked %d times (expired links were refreshed: %v)", pausedAt,
		strings.Count(string(args), "\n"), gen1Reqs.Load() > 3)
}

func TestExpectedSizeAndConns(t *testing.T) {
	if got := expectedSize("NA|NA|NA"); got != 0 {
		t.Errorf("NA: %d", got)
	}
	if got := expectedSize("500|400|150"); got != 550 {
		t.Errorf("sum: %d", got)
	}
	if got := expectedSize("500|400|NA"); got != 500 {
		t.Errorf("partial: %d", got)
	}
	st := []FastStream{{Size: 900}, {Size: 100}}
	c := ytConns(16, st, []int{0, 1})
	if c[0]+c[1] != 16 || c[1] < 2 || c[0] < c[1] {
		t.Errorf("conns %v", c)
	}
	if c := ytConns(16, st, []int{1}); c[1] != 16 {
		t.Errorf("one left %v", c)
	}
}
