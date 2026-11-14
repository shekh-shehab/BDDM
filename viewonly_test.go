package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type rewriteRT struct{ to *url.URL }

func (r rewriteRT) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.to.Scheme, r.to.Host
	return http.DefaultTransport.RoundTrip(req)
}

// A folder with one view-only file and one normal file: the view-only file
// fails at once (no retries), the other downloads, one folder message.
func TestViewOnlyFolderEndToEnd(t *testing.T) {
	var viewHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("authuser") != "1" || !strings.Contains(r.Header.Get("Cookie"), "SID=x") {
			http.Error(w, "need cookies and authuser", 403)
			return
		}
		if strings.HasPrefix(r.URL.Query().Get("id"), "VIEWONLY") {
			viewHits.Add(1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<html><head><title>Google Drive - Can't download file</title></head><body>x</body></html>`))
			return
		}
		data := strings.Repeat("%PDF-", 1000)
		w.Header().Set("Content-Disposition", `attachment; filename="ok.pdf"`)
		http.ServeContent(w, r, "ok.pdf", time.Time{}, strings.NewReader(data))
	}))
	defer srv.Close()
	to, _ := url.Parse(srv.URL)
	dir := t.TempDir()
	m, err := NewManager(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.client.Transport = rewriteRT{to}
	m.mu.Lock()
	m.settings.DownloadDir = dir
	g := &Group{ID: "g", Name: "New", Dir: dir}
	m.groups = append(m.groups, g)
	for _, id := range []string{"VIEWONLY000001", "NORMALFILE0001"} {
		m.jobs = append(m.jobs, &Job{ID: id, URL: "https://drive.google.com/file/d/" + id + "/view?authuser=1", Cookie: "SID=x",
			Dir: dir, Parts: 8, Status: StatusQueued, Group: "g", Want: id + ".pdf"})
	}
	m.scheduleLocked()
	m.mu.Unlock()
	start := time.Now()
	for time.Since(start) < 20*time.Second {
		m.mu.Lock()
		v := m.groupViewLocked(g)
		m.mu.Unlock()
		if v.Active == 0 && v.Queued == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	m.mu.Lock()
	v := m.groupViewLocked(g)
	errText := m.jobs[0].Error
	m.mu.Unlock()
	if v.Finished != 1 || v.ViewOnly != 1 || errText != msgDriveViewOnly {
		t.Fatalf("view %+v err %q", v, errText)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("view-only took %s (retries?)", time.Since(start))
	}
	if n := viewHits.Load(); n > 2 {
		t.Errorf("view-only page asked %d times", n)
	}
	ev, _ := m.Events(0)
	if len(ev) != 1 {
		t.Errorf("events %+v", ev)
	}
	t.Logf("view-only hits %d, events %+v", viewHits.Load(), ev)
}
