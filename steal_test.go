package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testServer serves data with Range support. Every connection gets about
// fastRate bytes/s, except requests that start exactly at slowStart, which
// get slowRate (one "bad" connection, like a slow CDN node).
type testServer struct {
	data               []byte
	fastRate, slowRate int64
	slowStart          int64
	requests           atomic.Int64
}

func (ts *testServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ts.requests.Add(1)
	size := int64(len(ts.data))
	from, to := int64(0), size-1
	partial := false
	if rh := r.Header.Get("Range"); strings.HasPrefix(rh, "bytes=") {
		a, b, _ := strings.Cut(strings.TrimPrefix(rh, "bytes="), "-")
		from, _ = strconv.ParseInt(a, 10, 64)
		if b != "" {
			to, _ = strconv.ParseInt(b, 10, 64)
		}
		if to >= size {
			to = size - 1
		}
		partial = true
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(to-from+1, 10))
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, to, size))
		w.WriteHeader(http.StatusPartialContent)
	}
	if r.Method == http.MethodHead {
		return
	}
	rate := ts.fastRate
	if partial && from == ts.slowStart && to-from > 0 {
		rate = ts.slowRate
	}
	const chunk = 16 * 1024
	start := time.Now()
	var sent int64
	for off := from; off <= to; off += chunk {
		end := off + chunk
		if end > to+1 {
			end = to + 1
		}
		if _, err := w.Write(ts.data[off:end]); err != nil {
			return
		}
		sent += end - off
		if want := time.Duration(float64(sent) / float64(rate) * float64(time.Second)); want > time.Since(start) {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(want - time.Since(start)):
			}
		}
	}
}

func newTestManager(t *testing.T, parts int) (*Manager, string) {
	t.Helper()
	base := t.TempDir()
	m, err := NewManager(filepath.Join(base, "cfg"), nil)
	if err != nil {
		t.Fatal(err)
	}
	dl := filepath.Join(base, "dl")
	if _, err := m.SetSettings(Settings{DownloadDir: dl, Parts: parts, MaxActive: 3}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Shutdown)
	return m, dl
}

func waitStatus(t *testing.T, m *Manager, id string, want string, timeout time.Duration) JobView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, j := range m.History("") {
			if j.ID == id {
				if j.Status == want {
					return j
				}
				if j.Status == StatusError {
					t.Fatalf("job failed: %s", j.Error)
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", want)
	return JobView{}
}

func randomData(t *testing.T, n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func checkFile(t *testing.T, path string, data []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(got) != sha256.Sum256(data) {
		t.Fatalf("SHA256 mismatch (got %d bytes, want %d)", len(got), len(data))
	}
}

// checkCoverage verifies the parts cover [0,size) exactly once.
func checkCoverage(t *testing.T, parts []Part, size int64) {
	t.Helper()
	ps := append([]Part(nil), parts...)
	sort.Slice(ps, func(i, j int) bool { return ps[i].From < ps[j].From })
	next := int64(0)
	for _, p := range ps {
		if p.From != next {
			t.Fatalf("gap or overlap at %d (part starts %d): %+v", next, p.From, ps)
		}
		if p.Written > p.To-p.From+1 {
			t.Fatalf("part wrote past its end: %+v", p)
		}
		next = p.To + 1
	}
	if next != size {
		t.Fatalf("parts end at %d, size %d", next, size)
	}
}

func timedDownload(t *testing.T, steal bool, data []byte, parts int) (time.Duration, int64) {
	old, oldSlow := stealEnabled, slowReplace
	stealEnabled, slowReplace = steal, false // measure stealing alone
	defer func() { stealEnabled, slowReplace = old, oldSlow }()
	size := int64(len(data))
	ts := &testServer{data: data, fastRate: 8 << 20, slowRate: 512 << 10, slowStart: 2 * (size / int64(parts))}
	srv := httptest.NewServer(ts)
	defer srv.Close()
	m, dl := newTestManager(t, parts)
	start := time.Now()
	j, err := m.Add(AddRequest{URL: srv.URL + "/file.bin", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, j.ID, StatusDone, 2*time.Minute)
	took := time.Since(start)
	checkFile(t, filepath.Join(dl, "file.bin"), data)
	return took, ts.requests.Load()
}

func TestStealFasterAndIdentical(t *testing.T) {
	data := randomData(t, 48<<20)
	without, r1 := timedDownload(t, false, data, 6)
	with, r2 := timedDownload(t, true, data, 6)
	t.Logf("48 MB, 6 connections, one connection throttled to 512 KB/s, others 8 MB/s")
	t.Logf("without stealing: %v (%d requests)", without.Round(10*time.Millisecond), r1)
	t.Logf("with stealing:    %v (%d requests)", with.Round(10*time.Millisecond), r2)
	if with >= without*2/3 {
		t.Fatalf("stealing was not clearly faster: %v vs %v", with, without)
	}
}

func TestStealPauseResume(t *testing.T) {
	old, oldSlow := stateSyncEvery, slowReplace
	stateSyncEvery = time.Second // the test watches the progress file
	slowReplace = false          // the slow connection must stay slow to be split
	defer func() { stateSyncEvery, slowReplace = old, oldSlow }()
	data := randomData(t, 48<<20)
	size := int64(len(data))
	ts := &testServer{data: data, fastRate: 8 << 20, slowRate: 512 << 10, slowStart: 2 * (size / 6)}
	srv := httptest.NewServer(ts)
	defer srv.Close()
	m, dl := newTestManager(t, 6)
	j, err := m.Add(AddRequest{URL: srv.URL + "/file.bin", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dl, "file.bin.bddm.json")
	// Wait until at least one part was split, then pause mid-download.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if st, err := loadState(statePath); err == nil && len(st.Parts) > 7 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no steal happened")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := m.Pause(j.ID); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	job := m.findLocked(j.ID)
	m.mu.Unlock()
	m.waitStopped(job)
	st, err := loadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	checkCoverage(t, st.Parts, size)
	var written int64
	for _, p := range st.Parts {
		written += p.Written
	}
	v := waitStatus(t, m, j.ID, StatusPaused, 5*time.Second)
	t.Logf("paused with %d parts in the state file, %d of %d bytes written (job shows %d)", len(st.Parts), written, size, v.Done)
	if v.Done != written {
		t.Fatalf("progress %d != written %d", v.Done, written)
	}
	if err := m.Resume(j.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, j.ID, StatusDone, 2*time.Minute)
	checkFile(t, filepath.Join(dl, "file.bin"), data)
}

// An old .bddm.json (written before work stealing) must still resume.
func TestOldStateFileResumes(t *testing.T) {
	data := randomData(t, 12<<20)
	size := int64(len(data))
	ts := &testServer{data: data, fastRate: 32 << 20, slowRate: 32 << 20, slowStart: -1}
	srv := httptest.NewServer(ts)
	defer srv.Close()
	m, dl := newTestManager(t, 4)
	if err := os.MkdirAll(dl, 0755); err != nil {
		t.Fatal(err)
	}
	q := size / 4
	parts := []Part{{0, q - 1, q}, {q, 2*q - 1, 1000}, {2 * q, 3*q - 1, 0}, {3 * q, size - 1, q / 2}}
	f := make([]byte, size)
	for _, p := range parts {
		copy(f[p.From:p.From+p.Written], data[p.From:p.From+p.Written])
	}
	if err := os.WriteFile(filepath.Join(dl, "old.bin.part"), f, 0644); err != nil {
		t.Fatal(err)
	}
	st, _ := json.Marshal(State{URL: srv.URL + "/old.bin", Size: size, Name: "old.bin", Parts: parts})
	if err := os.WriteFile(filepath.Join(dl, "old.bin.bddm.json"), st, 0644); err != nil {
		t.Fatal(err)
	}
	j, err := m.Add(AddRequest{URL: srv.URL + "/old.bin", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, j.ID, StatusDone, time.Minute)
	checkFile(t, filepath.Join(dl, "old.bin"), data)
}

// The writer must stop at a part's new end even if it moves mid-read.
func TestPartWriterStopsAtNewEnd(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "w")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d := &download{job: &Job{}, File: f, State: &State{Size: 100, Parts: []Part{{From: 0, To: 99}}}}
	w := &partWriter{d: d, idx: 0}
	if n, err := w.Write(bytes.Repeat([]byte{1}, 30)); n != 30 || err != nil {
		t.Fatalf("first write: %d %v", n, err)
	}
	d.mu.Lock()
	d.State.Parts[0].To = 39 // someone took bytes 40..99
	d.mu.Unlock()
	n, err := w.Write(bytes.Repeat([]byte{2}, 30))
	if n != 10 || err != errPartDone {
		t.Fatalf("second write: %d %v (want 10, errPartDone)", n, err)
	}
	if n, err := w.Write([]byte{3}); n != 0 || err != errPartDone {
		t.Fatalf("third write: %d %v", n, err)
	}
	if got := d.job.done.Load(); got != 40 {
		t.Fatalf("progress %d, want 40", got)
	}
}
