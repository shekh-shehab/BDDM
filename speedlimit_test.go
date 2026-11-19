package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSpeedStepMockupLabels(t *testing.T) {
	cases := []struct {
		pos  int
		want string
	}{
		{2, "100 KB/s"},
		{3, "200 KB/s"},
		{4, "2 MB/s"},
		{5, "5 MB/s"},
		{10, "Unlimited"},
		{99, "Unlimited"},
	}
	for _, c := range cases {
		if got := formatSpeedLimit(speedStepBPS(c.pos)); got != c.want {
			t.Errorf("step %d: got %q, want %q", c.pos, got, c.want)
		}
		if speedStepIndex(speedStepBPS(c.pos)) != c.pos && c.pos <= 10 {
			t.Errorf("index for step %d = %d", c.pos, speedStepIndex(speedStepBPS(c.pos)))
		}
	}
}

func TestEffectiveLimit(t *testing.T) {
	if got := effectiveLimit(nil, 5<<20); got != 5<<20 {
		t.Fatalf("inherit global: %d", got)
	}
	if got := effectiveLimit(int64ptr(2<<20), 5<<20); got != 2<<20 {
		t.Fatalf("override: %d", got)
	}
	if got := effectiveLimit(int64ptr(0), 5<<20); got != 0 {
		t.Fatalf("override to unlimited: %d", got)
	}
	if got := effectiveLimit(nil, -10); got != 0 {
		t.Fatalf("negative global: %d", got)
	}
}

func TestTokenBucketRate(t *testing.T) {
	const n, bps = 80 * 1024, 40 * 1024
	r := &limitedReader{
		ctx: context.Background(),
		r:   bytes.NewReader(bytes.Repeat([]byte("x"), n)),
		b:   newTokenBucket(bps),
	}
	start := time.Now()
	got, err := io.Copy(io.Discard, r)
	elapsed := time.Since(start)
	if err != nil || got != n {
		t.Fatalf("copy: n=%d err=%v", got, err)
	}
	// Burst is about one second, so 80 KiB at 40 KiB/s takes ~1s.
	if elapsed < 700*time.Millisecond {
		t.Fatalf("too fast: %v (limit not applied?)", elapsed)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("too slow: %v", elapsed)
	}
}

func TestTokenBucketUnlimitedAndLiveChange(t *testing.T) {
	n := 200 * 1024
	b := newTokenBucket(8 * 1024) // slow
	r := &limitedReader{ctx: context.Background(), r: bytes.NewReader(bytes.Repeat([]byte("y"), n)), b: b}
	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		if _, err := io.Copy(io.Discard, r); err != nil {
			t.Errorf("copy: %v", err)
		}
		done <- time.Since(start)
	}()
	time.Sleep(120 * time.Millisecond)
	b.SetRate(0) // unlimited; remaining bytes should finish immediately
	select {
	case elapsed := <-done:
		if elapsed > 2*time.Second {
			t.Fatalf("live change did not apply: took %v", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("copy did not finish after lifting the limit")
	}

	data := bytes.Repeat([]byte("z"), 64*1024)
	start := time.Now()
	if _, err := io.Copy(io.Discard, &limitedReader{ctx: context.Background(), r: bytes.NewReader(data), b: newTokenBucket(0)}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("unlimited bucket was slow: %v", time.Since(start))
	}
}

func TestSettingsSpeedLimitPersist(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(filepath.Join(dir, "cfg"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := m.Settings()
	if s.HTTPDownloadLimit != 0 || s.TorrentDownloadLimit != 0 || s.TorrentUploadLimit != 0 {
		t.Fatalf("defaults should be unlimited: %+v", s)
	}
	s.HTTPDownloadLimit = 5 << 20
	s.TorrentDownloadLimit = 0
	s.TorrentUploadLimit = 200 << 10
	s.DownloadDir = filepath.Join(dir, "dl")
	if _, err := m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	m.Shutdown()

	m2, err := NewManager(filepath.Join(dir, "cfg"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Shutdown()
	s2 := m2.Settings()
	if s2.HTTPDownloadLimit != 5<<20 || s2.TorrentUploadLimit != 200<<10 || s2.TorrentDownloadLimit != 0 {
		t.Fatalf("persisted limits: %+v", s2)
	}
}

func TestHTTPDownloadSpeedLimitAndOverride(t *testing.T) {
	data := bytes.Repeat([]byte("ab"), 60*1024) // 120 KiB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Disposition", `attachment; filename="clip.bin"`)
		http.ServeContent(w, r, "clip.bin", time.Time{}, bytes.NewReader(data))
	}))
	t.Cleanup(srv.Close)

	m, _ := newTestManager(t, 4)
	s := m.Settings()
	s.HTTPDownloadLimit = 40 << 10 // 40 KB/s; 120 KiB with ~40 KiB burst ≈ 2s
	s.Parts = 2
	if _, err := m.SetSettings(s); err != nil {
		t.Fatal(err)
	}

	v, err := m.Add(AddRequest{URL: srv.URL + "/clip.bin"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	done := waitDone(t, m, v.ID, 30*time.Second)
	elapsed := time.Since(start)
	if done.Status != StatusDone {
		t.Fatalf("status %s: %s", done.Status, done.Error)
	}
	if elapsed < time.Second {
		t.Fatalf("global HTTP limit was not applied: finished in %v", elapsed)
	}

	// Per-download override to unlimited should be much faster.
	v2, err := m.Add(AddRequest{URL: srv.URL + "/clip.bin?n=2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetJobSpeedLimit(v2.ID, false, int64ptr(0), nil); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	done = waitDone(t, m, v2.ID, 30*time.Second)
	elapsed = time.Since(start)
	if done.Status != StatusDone {
		t.Fatalf("override status %s: %s", done.Status, done.Error)
	}
	if elapsed > time.Second {
		t.Fatalf("per-download unlimited override was still slow: %v", elapsed)
	}
	if done.DlLimit == nil || *done.DlLimit != 0 {
		t.Fatalf("job view should show the unlimited override: %+v", done.DlLimit)
	}
}

func TestJobSpeedLimitPersistsAndOff(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "cfg")
	m, err := NewManager(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := m.Settings()
	s.DownloadDir = filepath.Join(dir, "dl")
	if _, err := m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	// A finished job in history is enough to persist the cap.
	m.mu.Lock()
	m.jobs = append(m.jobs, &Job{ID: "job1", URL: "http://example.com/a.bin", Name: "a.bin", Dir: s.DownloadDir, Status: StatusDone})
	m.mu.Unlock()
	if _, err := m.SetJobSpeedLimit("job1", false, int64ptr(2<<20), int64ptr(100<<10)); err != nil {
		t.Fatal(err)
	}
	m.Shutdown()

	m2, err := NewManager(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Shutdown()
	var got *Job
	m2.mu.Lock()
	got = m2.findLocked("job1")
	m2.mu.Unlock()
	if got == nil || got.DlLimit == nil || *got.DlLimit != 2<<20 || got.UlLimit == nil || *got.UlLimit != 100<<10 {
		t.Fatalf("per-download limit was not persisted: %+v", got)
	}

	if _, err := m2.SetJobSpeedLimit("job1", true, nil, nil); err != nil {
		t.Fatal(err)
	}
	m2.mu.Lock()
	got = m2.findLocked("job1")
	m2.mu.Unlock()
	if got.DlLimit != nil || got.UlLimit != nil {
		t.Fatalf("Off should clear the override: dl=%v ul=%v", got.DlLimit, got.UlLimit)
	}
}

func TestSpeedLimitAPI(t *testing.T) {
	m, _ := newTestManager(t, 2)
	m.mu.Lock()
	m.jobs = append(m.jobs, &Job{ID: "j2", URL: "http://example.com/b.bin", Name: "b.bin", Status: StatusPaused})
	m.mu.Unlock()

	srv := httptest.NewServer(newServer(m, "0", func() {}, nil))
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(map[string]any{"httpDownloadLimit": 5 << 20, "torrentUploadLimit": 200 << 10})
	res, err := http.Post(srv.URL+"/api/settings", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("settings: %s", res.Status)
	}
	s := m.Settings()
	if s.HTTPDownloadLimit != 5<<20 || s.TorrentUploadLimit != 200<<10 {
		t.Fatalf("settings API: %+v", s)
	}

	body, _ = json.Marshal(map[string]any{"dlLimit": 2 << 20})
	res, err = http.Post(srv.URL+"/api/jobs/j2/speedlimit", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("speedlimit API: %s %s", res.Status, b)
	}
	m.mu.Lock()
	j := m.findLocked("j2")
	m.mu.Unlock()
	if j == nil || j.DlLimit == nil || *j.DlLimit != 2<<20 {
		t.Fatalf("job cap: %+v", j)
	}
}

func TestSpeedLimitSettingsFile(t *testing.T) {
	// The on-disk JSON uses the documented field names.
	dir := t.TempDir()
	cfg := filepath.Join(dir, "cfg")
	if err := os.MkdirAll(cfg, 0755); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"downloadDir":"` + filepath.ToSlash(filepath.Join(dir, "dl")) + `","parts":8,"maxActive":3,"httpDownloadLimit":5242880,"torrentUploadLimit":204800}`)
	if err := os.WriteFile(filepath.Join(cfg, "settings.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()
	s := m.Settings()
	if s.HTTPDownloadLimit != 5242880 || s.TorrentUploadLimit != 204800 {
		t.Fatalf("loaded settings.json: %+v", s)
	}
}

func TestTorrentGlobalDownloadLimit(t *testing.T) {
	data := bytes.Repeat([]byte("L"), 96*1024)
	tmp := t.TempDir()
	meta, mi, srcDir := writeTestTorrent(t, tmp, "limited.bin", data)
	seeder := startTestSeeder(t, mi, srcDir)

	m, dl := torrentTestManager(t)
	st := m.Settings()
	st.TorrentDownloadLimit = 24 * 1024 // 24 KB/s; 96 KiB with a 32 KiB burst is a few seconds
	if _, err := m.SetSettings(st); err != nil {
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
	start := time.Now()
	waitTorrentStatus(t, m, job.ID, StatusSeeding, 30*time.Second)
	elapsed := time.Since(start)
	if elapsed < 1500*time.Millisecond {
		t.Fatalf("global torrent download limit was not applied: finished in %v", elapsed)
	}
}
