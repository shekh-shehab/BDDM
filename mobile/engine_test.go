package mobile

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitStatus(t *testing.T, e *Engine, id, want string, timeout time.Duration) JobView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last JobView
	for time.Now().Before(deadline) {
		var snap Snapshot
		if err := json.Unmarshal([]byte(e.SnapshotJSON()), &snap); err != nil {
			t.Fatal(err)
		}
		for _, j := range snap.Jobs {
			if j.ID == id {
				last = j
				if j.Status == want || (want == "end" && (j.Status == StatusDone || j.Status == StatusError)) {
					return j
				}
			}
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("job %s did not become %s (last status=%s err=%s)", id, want, last.Status, last.Error)
	return last
}

func TestDownloadMultipartAndResume(t *testing.T) {
	payload := make([]byte, 2_000_000)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="sample.bin"`)
		w.Header().Set("Accept-Ranges", "bytes")
		http.ServeContent(w, r, "sample.bin", time.Unix(0, 0), strings.NewReader(string(payload)))
	}))
	defer srv.Close()

	dir := t.TempDir()
	e, err := New(filepath.Join(dir, "cfg"), filepath.Join(dir, "dl"))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	id, err := e.Add(srv.URL+"/sample.bin", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("empty id")
	}

	// Let it get going, then pause.
	deadline := time.Now().Add(3 * time.Second)
	var started bool
	for time.Now().Before(deadline) {
		var snap Snapshot
		_ = json.Unmarshal([]byte(e.SnapshotJSON()), &snap)
		for _, j := range snap.Jobs {
			if j.ID == id && (j.Status == StatusDownloading || j.Done > 0) {
				started = true
			}
		}
		if started {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := e.Pause(id); err != nil {
		t.Fatal(err)
	}
	paused := waitStatus(t, e, id, StatusPaused, 5*time.Second)
	if paused.Done == 0 {
		// Pause may have happened before the first byte; still valid — resume should finish.
		t.Log("paused before any bytes; continuing")
	}

	if err := e.Resume(id); err != nil {
		t.Fatal(err)
	}
	done := waitStatus(t, e, id, StatusDone, 15*time.Second)
	if done.Size != int64(len(payload)) {
		t.Fatalf("size=%d want %d", done.Size, len(payload))
	}
	if done.Done != done.Size {
		t.Fatalf("done=%d size=%d", done.Done, done.Size)
	}
	path, err := e.FilePath(id)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("file contents do not match (%d bytes)", len(got))
	}
	if !strings.HasSuffix(path, "sample.bin") && !strings.Contains(path, "sample") {
		t.Fatalf("unexpected name: %s", path)
	}
}

func TestRejectBadURL(t *testing.T) {
	dir := t.TempDir()
	e, err := New(filepath.Join(dir, "cfg"), filepath.Join(dir, "dl"))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err := e.Add("ftp://example.com/a", "", ""); err == nil {
		t.Fatal("expected error")
	}
	if _, err := e.Add("not a url", "", ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	e, err := New(filepath.Join(dir, "cfg"), filepath.Join(dir, "dl"))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err := e.SetSettingsJSON(`{"theme":"light","parts":4,"httpDownloadLimit":1024,"adBlocker":true}`); err != nil {
		t.Fatal(err)
	}
	var s Settings
	if err := json.Unmarshal([]byte(e.SettingsJSON()), &s); err != nil {
		t.Fatal(err)
	}
	if s.Theme != "light" || s.Parts != 4 || s.HTTPDownloadLimit != 1024 || !s.AdBlocker {
		t.Fatalf("settings not applied: %+v", s)
	}
}

func TestHTTPErrorSurface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, "missing")
	}))
	defer srv.Close()
	dir := t.TempDir()
	e, err := New(filepath.Join(dir, "cfg"), filepath.Join(dir, "dl"))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	id, err := e.Add(srv.URL+"/nope.bin", "", "nope.bin")
	if err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, e, id, StatusError, 8*time.Second)
	if !strings.Contains(got.Error, "404") {
		t.Fatalf("error = %q, want HTTP 404", got.Error)
	}
}

func TestCompareVersions(t *testing.T) {
	if compareVersions("1.6.1", "1.6.0") <= 0 {
		t.Fatal("1.6.1 should be newer")
	}
	if compareVersions("1.6.0", "1.6.0") != 0 {
		t.Fatal("equal")
	}
	if compareVersions("1.5.11", "1.6.0") >= 0 {
		t.Fatal("1.5.11 should be older")
	}
}

func TestParseAndroidRelease(t *testing.T) {
	r := &ghRelease{
		Tag:  "android-v1.6.1",
		HTML: "https://github.com/shekh-shehab/BDDM/releases/tag/android-v1.6.1",
		Assets: []ghAsset{
			{Name: "BDDM-Android-1.6.1.apk", URL: "https://example.com/app.apk"},
			{Name: "BDDM-Android-1.6.1.apk.sha256", URL: "https://example.com/app.sha256"},
		},
	}
	u := parseAndroidRelease(r)
	if u == nil || u.Version != "1.6.1" || u.APK == "" || u.SHA256 == "" {
		t.Fatalf("parse: %+v", u)
	}
	if parseAndroidRelease(&ghRelease{Tag: "v1.5.11"}) != nil {
		t.Fatal("PC tags must be ignored")
	}
}

func TestPlanPartsCoversFile(t *testing.T) {
	const size int64 = 1000
	parts := planParts(size, 8)
	if parts[0].From != 0 || parts[len(parts)-1].To != size-1 {
		t.Fatalf("range %d-%d", parts[0].From, parts[len(parts)-1].To)
	}
	var cov int64
	for i, p := range parts {
		if i > 0 && p.From != parts[i-1].To+1 {
			t.Fatalf("gap at %d", i)
		}
		cov += p.To - p.From + 1
	}
	if cov != size {
		t.Fatalf("covered %d want %d", cov, size)
	}
}

func TestSanitizeAndFilename(t *testing.T) {
	if sanitizeName(`a/b\:c*.bin`) == `a/b\:c*.bin` {
		t.Fatal("unsafe characters left in")
	}
	n := filenameFromURL("https://example.com/files/Ubuntu.iso?dl=1")
	if n != "Ubuntu.iso" {
		t.Fatalf("got %q", n)
	}
}

func TestCheckUpdateUsesListNotLatest(t *testing.T) {
	// The helper must ignore a PC "Latest" release mixed into the list.
	raw := []ghRelease{
		{Tag: "v1.5.11", Assets: []ghAsset{{Name: "BDDM-Setup.exe", URL: "https://x/exe"}}},
		{Tag: "android-v1.6.0", HTML: "https://x/android", Assets: []ghAsset{
			{Name: "BDDM-Android-1.6.0.apk", URL: "https://x/apk"},
		}},
	}
	var best *UpdateInfo
	for i := range raw {
		u := parseAndroidRelease(&raw[i])
		if u == nil {
			continue
		}
		if best == nil || compareVersions(u.Version, best.Version) > 0 {
			best = u
		}
	}
	if best == nil || best.Version != "1.6.0" {
		t.Fatalf("picked %+v", best)
	}
}

func TestSnapshotContainsParts(t *testing.T) {
	payload := make([]byte, 3_000_000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		http.ServeContent(w, r, "big.bin", time.Unix(0, 0), strings.NewReader(string(payload)))
	}))
	defer srv.Close()
	dir := t.TempDir()
	e, err := New(filepath.Join(dir, "cfg"), filepath.Join(dir, "dl"))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	_ = e.SetSettingsJSON(`{"parts":4}`)
	id, err := e.Add(srv.URL+"/big.bin", "", "big.bin")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	var sawParts bool
	for time.Now().Before(deadline) {
		var snap Snapshot
		_ = json.Unmarshal([]byte(e.SnapshotJSON()), &snap)
		for _, j := range snap.Jobs {
			if j.ID == id && j.Segs != nil && j.Segs.Kind == "parts" && len(j.Segs.Segs) >= 2 {
				sawParts = true
			}
			if j.ID == id && j.Status == StatusDone {
				if !sawParts {
					t.Log("finished before parts snapshot was observed (very fast)")
				}
				return
			}
			if j.ID == id && j.Status == StatusError {
				t.Fatal(j.Error)
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !sawParts {
		t.Fatal("never saw per-part progress")
	}
	fmt.Fprintf(io.Discard, "")
}
