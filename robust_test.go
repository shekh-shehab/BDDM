package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// flexServer serves one file that the test can replace (a new version with
// another ETag), whose link token can expire, and that can refuse more than
// maxConns connections at once with 503.
type flexServer struct {
	mu       sync.Mutex
	data     []byte
	etag     string
	token    string // "" = any link works
	maxConns int32  // 0 = no limit
	rate     int64  // bytes/s per connection
	active   atomic.Int32
	refused  atomic.Int32
	served   atomic.Int64 // file bytes sent
	gen      atomic.Int64 // bumped on every change: open answers are cut off
}

func (fs *flexServer) set(data []byte, etag string) {
	fs.mu.Lock()
	fs.data, fs.etag = data, etag
	fs.mu.Unlock()
	fs.gen.Add(1)
}

func (fs *flexServer) setToken(tok string) {
	fs.mu.Lock()
	fs.token = tok
	fs.mu.Unlock()
	fs.gen.Add(1)
}

func (fs *flexServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	data, etag, token := fs.data, fs.etag, fs.token
	fs.mu.Unlock()
	gen := fs.gen.Load()
	if token != "" && r.URL.Query().Get("token") != token {
		http.Error(w, "expired", http.StatusForbidden)
		return
	}
	if n := fs.active.Add(1); fs.maxConns > 0 && n > fs.maxConns && r.Method == http.MethodGet {
		fs.active.Add(-1)
		fs.refused.Add(1)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	defer fs.active.Add(-1)
	size := int64(len(data))
	from, to := int64(0), size-1
	partial := false
	if rh := r.Header.Get("Range"); strings.HasPrefix(rh, "bytes=") {
		a, b, _ := strings.Cut(strings.TrimPrefix(rh, "bytes="), "-")
		from, _ = strconv.ParseInt(a, 10, 64)
		if b != "" {
			to, _ = strconv.ParseInt(b, 10, 64)
		}
		to = min(to, size-1)
		partial = true
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Length", strconv.FormatInt(to-from+1, 10))
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, to, size))
		w.WriteHeader(http.StatusPartialContent)
	}
	if r.Method == http.MethodHead {
		return
	}
	const chunk = 16 * 1024
	start := time.Now()
	var sent int64
	for off := from; off <= to; off += chunk {
		if fs.gen.Load() != gen {
			panic(http.ErrAbortHandler) // the server cut the connection
		}
		end := min(off+chunk, to+1)
		if _, err := w.Write(data[off:end]); err != nil {
			return
		}
		sent += end - off
		fs.served.Add(end - off)
		if fs.rate > 0 {
			if want := time.Duration(float64(sent) / float64(fs.rate) * float64(time.Second)); want > time.Since(start) {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(want - time.Since(start)):
				}
			}
		}
	}
}

func jobView(m *Manager, id string) JobView {
	for _, j := range m.History("") {
		if j.ID == id {
			return j
		}
	}
	return JobView{}
}

func waitDone(t *testing.T, m *Manager, id string, timeout time.Duration) JobView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		v := jobView(m, id)
		if v.Status == StatusDone || v.Status == StatusError {
			return v
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timeout")
	return JobView{}
}

func waitBytes(t *testing.T, m *Manager, id string, n int64) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for jobView(m, id).Done < n {
		if time.Now().After(deadline) {
			t.Fatal("download did not get going")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A new version of the file appears while downloading: the result must be
// the new version, never a mix of both.
func TestFileChangedWhileDownloading(t *testing.T) {
	v1, v2 := randomData(t, 8<<20), randomData(t, 8<<20)
	fs := &flexServer{rate: 2 << 20}
	fs.set(v1, `"v1"`)
	srv := httptest.NewServer(fs)
	defer srv.Close()
	m, dl := newTestManager(t, 4)
	j, err := m.Add(AddRequest{URL: srv.URL + "/f.bin", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	waitBytes(t, m, j.ID, 1<<20)
	fs.set(v2, `"v2"`)
	v := waitDone(t, m, j.ID, time.Minute)
	if v.Status != StatusDone {
		t.Fatalf("status %s: %s", v.Status, v.Error)
	}
	checkFile(t, filepath.Join(dl, "f.bin"), v2)
}

// Paused, then the server got another file (other size): resume must start
// again instead of joining old and new data.
func TestFileChangedOnResume(t *testing.T) {
	v1, v2 := randomData(t, 8<<20), randomData(t, 6<<20)
	fs := &flexServer{rate: 2 << 20}
	fs.set(v1, `"v1"`)
	srv := httptest.NewServer(fs)
	defer srv.Close()
	m, dl := newTestManager(t, 4)
	j, _ := m.Add(AddRequest{URL: srv.URL + "/f.bin", Force: true})
	waitBytes(t, m, j.ID, 2<<20)
	if err := m.Pause(j.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, j.ID, StatusPaused, 10*time.Second)
	fs.set(v2, `"v2"`)
	fs.rate = 0
	if err := m.Resume(j.ID); err != nil {
		t.Fatal(err)
	}
	v := waitDone(t, m, j.ID, time.Minute)
	if v.Status != StatusDone || v.Name != "f.bin" {
		t.Fatalf("status %s name %s: %s", v.Status, v.Name, v.Error)
	}
	checkFile(t, filepath.Join(dl, "f.bin"), v2)
}

// The link expires in the middle: the download stops with a clear message,
// and the same file clicked again in the browser (new link) continues it.
func TestExpiredLinkRefreshed(t *testing.T) {
	data := randomData(t, 8<<20)
	fs := &flexServer{rate: 2 << 20, token: "A"}
	fs.set(data, `"same"`)
	srv := httptest.NewServer(fs)
	defer srv.Close()
	m, dl := newTestManager(t, 4)
	j, _ := m.Add(AddRequest{URL: srv.URL + "/f.bin?token=A", Force: true})
	waitBytes(t, m, j.ID, 3<<20)
	fs.setToken("B") // the old link stops working
	v := waitDone(t, m, j.ID, time.Minute)
	if v.Status != StatusError || v.Error != msgLinkExpired {
		t.Fatalf("want the expired-link error, got %s: %q", v.Status, v.Error)
	}
	got := v.Done
	before := fs.served.Load()
	// The browser sends the new link (the extension's /add).
	r := m.RefreshLink(AddRequest{URL: srv.URL + "/f.bin?token=B"}, "f.bin", 0)
	if r == nil || r.ID != j.ID {
		t.Fatalf("the new link was not matched to the download: %+v", r)
	}
	v = waitDone(t, m, j.ID, time.Minute)
	if v.Status != StatusDone {
		t.Fatalf("after refresh: %s %s", v.Status, v.Error)
	}
	checkFile(t, filepath.Join(dl, "f.bin"), data)
	again := fs.served.Load() - before
	t.Logf("had %d bytes; %d more bytes after the new link (file %d)", got, again, len(data))
	if again > int64(len(data))-got+(1<<20) {
		t.Fatalf("it started again instead of continuing")
	}
	// Another file of the same name and another size is not matched.
	if m.RefreshLink(AddRequest{URL: srv.URL + "/x"}, "f.bin", 123) != nil {
		t.Fatal("a finished download was matched")
	}
}

// A server that allows only 2 connections (503 above that): the download
// finishes, and BDDM remembers to open fewer connections to it.
func TestServerConnectionLimit(t *testing.T) {
	data := randomData(t, 8<<20)
	fs := &flexServer{rate: 4 << 20, maxConns: 2}
	fs.set(data, `"x"`)
	srv := httptest.NewServer(fs)
	defer srv.Close()
	m, dl := newTestManager(t, 8)
	j, _ := m.Add(AddRequest{URL: srv.URL + "/f.bin", Force: true})
	v := waitDone(t, m, j.ID, time.Minute)
	if v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	checkFile(t, filepath.Join(dl, "f.bin"), data)
	u, _ := url.Parse(srv.URL)
	lim := hostLimit(u.Host, 8)
	t.Logf("refused %d requests; remembered limit %d", fs.refused.Load(), lim)
	if fs.refused.Load() == 0 || lim > 2 {
		t.Fatalf("limit not learned: %d", lim)
	}
	// The next download from that server starts with fewer connections.
	refusedBefore := fs.refused.Load()
	j2, _ := m.Add(AddRequest{URL: srv.URL + "/g.bin", Force: true})
	if v := waitDone(t, m, j2.ID, time.Minute); v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	if n := fs.refused.Load() - refusedBefore; n > 2 {
		t.Fatalf("second download was refused %d times", n)
	}
}

func TestHostLimitRecovers(t *testing.T) {
	hostLowered("slow.example", 2)
	if n := hostLimit("slow.example", 8); n != 2 {
		t.Fatalf("limit %d", n)
	}
	hostConns.Lock()
	hostConns.m["slow.example"].changed = time.Now().Add(-4 * hostConnRecover)
	hostConns.Unlock()
	if n := hostLimit("slow.example", 8); n != 6 {
		t.Fatalf("after 4 quiet periods: %d", n)
	}
}

// A file bigger than the free space fails at once with a clear message.
func TestNotEnoughDiskSpace(t *testing.T) {
	big := int64(1) << 52 // 4 PB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Header.Get("Range") == "" {
			w.Header().Set("Content-Length", strconv.FormatInt(big, 10))
			if r.Method != http.MethodHead {
				http.Error(w, "no", 500)
			}
			return
		}
		// The first request asks for the start of the file: the answer
		// tells the size (no data is needed to see it does not fit).
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", big))
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte{0})
	}))
	defer srv.Close()
	m, dl := newTestManager(t, 4)
	j, _ := m.Add(AddRequest{URL: srv.URL + "/huge.iso", Force: true})
	v := waitDone(t, m, j.ID, 30*time.Second)
	if v.Status != StatusError || !strings.HasPrefix(v.Error, "Not enough free space") {
		t.Fatalf("%s: %q", v.Status, v.Error)
	}
	if fileExists(filepath.Join(dl, "huge.iso.part")) {
		t.Fatal("a .part file was created")
	}
	if free, err := diskFree(dl); err != nil || free <= 0 {
		t.Fatalf("diskFree: %d %v", free, err)
	}
}

func TestFileChangedRules(t *testing.T) {
	st := &State{Size: 100, ETag: `W/"abc"`, LastModified: "Mon, 01 Jan 2024 00:00:00 GMT"}
	for _, c := range []struct {
		size          int64
		etag, lastMod string
		changed       bool
	}{
		{100, `"abc"`, "", false}, // weak/strong form of the same ETag
		{0, "", "", false},        // nothing known: not a change
		{101, "", "", true},
		{100, `"xyz"`, "", true},
		{100, "", "Tue, 02 Jan 2024 00:00:00 GMT", true},
	} {
		if got := fileChanged(st, c.size, c.etag, c.lastMod) != ""; got != c.changed {
			t.Errorf("%+v: changed=%v", c, got)
		}
	}
}

func TestWriteFileSync(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	if err := writeFileSync(p, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := writeFileSync(p, []byte("two")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "two" || fileExists(p+".tmp") {
		t.Fatalf("%q", b)
	}
}
