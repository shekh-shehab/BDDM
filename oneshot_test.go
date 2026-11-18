package main

// 1.4.2 part 3: Send Anywhere links work only once: BDDM asks for them
// exactly once (no size check, no Range, one connection).

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOneShotLinks(t *testing.T) {
	for _, c := range []struct {
		link, ref string
		want      bool
	}{
		{"https://send-anywhere.com/web/downloads/9Q0CD44B", "", true},
		{"https://abc.send-anywhere.com/webfile/xyz", "", true},
		{"https://52.1.2.3/webfile/xyz?device_key=1", "https://send-anywhere.com/web/downloads/9Q0CD44B", true},
		{"http://sendanywhe.re/9Q0CD44B", "", true},
		{"https://example.com/send-anywhere.com.zip", "", false},
		{"https://notsend-anywhere.com/f", "", false},
	} {
		if got := isOneShot(c.link, c.ref); got != c.want {
			t.Errorf("isOneShot(%q, %q) = %v", c.link, c.ref, got)
		}
	}
	if !oneShotPage("http://sendanywhe.re/9Q0CD44B") || !oneShotPage("https://send-anywhere.com/web/downloads/9Q0CD44B") ||
		oneShotPage("https://abc.send-anywhere.com/webfile/xyz") {
		t.Error("oneShotPage")
	}
}

// onceServer gives its file to the first request only.
type onceServer struct {
	n      atomic.Int64
	mu     sync.Mutex
	ranges []string
	srv    *httptest.Server
}

func newOnceServer(t *testing.T, data []byte) *onceServer {
	o := &onceServer{}
	o.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		o.ranges = append(o.ranges, r.Method+" "+r.Header.Get("Range"))
		o.mu.Unlock()
		if o.n.Add(1) > 1 {
			http.Error(w, "transfer finished", http.StatusGone)
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Disposition", `attachment; filename="holiday photos.zip"`)
		http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(data))
	}))
	t.Cleanup(o.srv.Close)
	return o
}

func TestSendAnywhereAskedOnce(t *testing.T) {
	data := bytes.Repeat([]byte("sa"), 3<<20) // 6 MB: would get several connections
	ref := "https://send-anywhere.com/web/downloads/9Q0CD44B"
	m, _ := newTestManager(t, 8)

	o := newOnceServer(t, data)
	v, err := m.Add(AddRequest{URL: o.srv.URL + "/webfile/ABC", Referer: ref})
	if err != nil {
		t.Fatal(err)
	}
	done := waitDone(t, m, v.ID, time.Minute)
	if done.Status != StatusDone {
		t.Fatalf("%s: %s (requests %v)", done.Status, done.Error, o.ranges)
	}
	if o.n.Load() != 1 || o.ranges[0] != "GET " {
		t.Fatalf("asked %d times: %v", o.n.Load(), o.ranges)
	}
	got, _ := os.ReadFile(filepath.Join(done.Dir, done.Name))
	if !bytes.Equal(got, data) || done.Name != "holiday photos.zip" {
		t.Fatalf("file %q, %d bytes", done.Name, len(got))
	}

	// The "Download File Info" window does not look at the link first.
	o2 := newOnceServer(t, data)
	pv, err := m.AddPending(AddRequest{URL: o2.srv.URL + "/webfile/DEF", Referer: ref, Filename: "holiday photos.zip"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if p, _ := m.Pending(pv.ID); p.Checking || p.Name != "holiday photos.zip" {
		t.Fatalf("window: %+v", p)
	}
	if n := o2.n.Load(); n != 0 {
		t.Fatalf("the window asked the server %d times", n)
	}
	if _, err := m.StartPending(pv.ID, "", "", false, ""); err != nil {
		t.Fatal(err)
	}
	if done := waitDone(t, m, pv.ID, time.Minute); done.Status != StatusDone || o2.n.Load() != 1 {
		t.Fatalf("%s %s, %d requests", done.Status, done.Error, o2.n.Load())
	}

	// A share page cannot be downloaded by BDDM: a plain explanation.
	if _, err := m.Add(AddRequest{URL: "http://sendanywhe.re/9Q0CD44B"}); err == nil || err.Error() != msgOneShotPage {
		t.Fatalf("share page: %v", err)
	}
}
