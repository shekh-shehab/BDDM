package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.4.2", "1.4.3", -1},
		{"1.4.3", "1.4.2", 1},
		{"1.4.3", "1.4.3", 0},
		{"v1.4.3", "1.4.3", 0},
		{"V1.4.3", "v1.4.3", 0},
		{"1.4.2", "v1.4.3", -1},
		{"1.4", "1.4.0", 0},
		{"1.4.0", "1.4", 0},
		{"1.4.2.1", "1.4.2", 1},
		{"1.4.2", "1.4.2.1", -1},
		{"1.10.0", "1.9.9", 1},
		{"2.0.0", "1.99.99", 1},
		{"1.4.3-beta", "1.4.3", -1},
		{"1.4.3", "1.4.3-beta", 1},
		{"1.4.3-alpha", "1.4.3-beta", -1},
		{"", "1.0.0", -1},
		{"1.0.0", "", 1},
		{"  v1.4.3  ", "1.4.3", 0},
	}
	for _, c := range cases {
		got := compareVersions(c.a, c.b)
		if got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParseLatestRelease(t *testing.T) {
	ok := []byte(`{"tag_name":"v1.5.0","name":"BDDM 1.5.0","html_url":"https://github.com/shekh-shehab/BDDM/releases/tag/v1.5.0","draft":false,"prerelease":false}`)
	info, err := parseLatestRelease(ok)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "1.5.0" || info.Tag != "v1.5.0" || !strings.Contains(info.URL, "v1.5.0") {
		t.Fatalf("parsed: %+v", info)
	}

	if _, err := parseLatestRelease([]byte(`{"tag_name":"v1.5.0","html_url":"https://example.com","draft":true}`)); err == nil {
		t.Fatal("draft should be rejected")
	}
	if _, err := parseLatestRelease([]byte(`{"tag_name":"v1.5.0","html_url":"https://example.com","prerelease":true}`)); err == nil {
		t.Fatal("prerelease should be rejected")
	}
	if _, err := parseLatestRelease([]byte(`{"html_url":"https://example.com"}`)); err == nil {
		t.Fatal("missing tag should be rejected")
	}
	if _, err := parseLatestRelease([]byte(`{"tag_name":"v1.5.0"}`)); err == nil {
		t.Fatal("missing url should be rejected")
	}
	if _, err := parseLatestRelease([]byte(`not json`)); err == nil {
		t.Fatal("bad json should be rejected")
	}
	// tag without a leading v
	info, err = parseLatestRelease([]byte(`{"tag_name":"1.4.3","html_url":"https://github.com/shekh-shehab/BDDM/releases/tag/1.4.3"}`))
	if err != nil || info.Version != "1.4.3" {
		t.Fatalf("tag without v: %+v %v", info, err)
	}
}

func TestFetchLatestRelease(t *testing.T) {
	var sawAuth atomic.Bool
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuth.Store(true)
		}
		gotUA = r.Header.Get("User-Agent")
		if r.URL.Path != "/releases/latest" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v1.5.0", "html_url": "https://github.com/shekh-shehab/BDDM/releases/tag/v1.5.0",
		})
	}))
	defer srv.Close()

	info, err := fetchLatestRelease(srv.Client(), srv.URL+"/releases/latest", "1.4.3")
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "1.5.0" {
		t.Fatalf("version: %s", info.Version)
	}
	if sawAuth.Load() {
		t.Fatal("request must not send a token")
	}
	if !strings.HasPrefix(gotUA, "BDDM/") {
		t.Fatalf("User-Agent: %q", gotUA)
	}

	// Same version: fetch still succeeds; the caller decides it is not newer.
	info, err = fetchLatestRelease(srv.Client(), srv.URL+"/releases/latest", "1.5.0")
	if err != nil || info.Version != "1.5.0" {
		t.Fatalf("same version: %+v %v", info, err)
	}
}

func TestFetchLatestReleaseErrors(t *testing.T) {
	codes := []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError}
	for _, code := range codes {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "no", code)
		}))
		_, err := fetchLatestRelease(srv.Client(), srv.URL, "1.4.3")
		srv.Close()
		if err == nil {
			t.Fatalf("HTTP %d: want error", code)
		}
	}

	// Offline / connection refused: closed server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	if _, err := fetchLatestRelease(&http.Client{Timeout: time.Second}, url, "1.4.3"); err == nil {
		t.Fatal("closed server: want error")
	}
}

func TestUpdaterNoticeAndDismiss(t *testing.T) {
	var mu sync.Mutex
	var notices []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("must not send a token")
		}
		io.WriteString(w, `{"tag_name":"v1.5.0","html_url":"https://github.com/shekh-shehab/BDDM/releases/tag/v1.5.0"}`)
	}))
	defer srv.Close()

	u := newUpdater("1.4.3", func() bool { return true }, func() bool { return true }, func(title, body string) {
		mu.Lock()
		notices = append(notices, title+": "+body)
		mu.Unlock()
	})
	u.client = srv.Client()
	u.apiURL = srv.URL

	s := u.Check(false)
	if !s.Available || s.Version != "1.5.0" || s.Current != "1.4.3" {
		t.Fatalf("after check: %+v", s)
	}
	// Give the notification goroutine a moment.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(notices)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	if len(notices) != 1 || !strings.Contains(notices[0], "v1.5.0") {
		t.Fatalf("notice: %v", notices)
	}
	mu.Unlock()

	// A second check must not show another tray notice.
	u.Check(false)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(notices) != 1 {
		t.Fatalf("notice shown more than once: %v", notices)
	}
	mu.Unlock()

	s = u.Dismiss()
	if s.Available {
		t.Fatal("notice should be hidden after Later")
	}
	// Still hidden on another check of the same version, until a restart
	// (this Updater is the same process).
	s = u.Check(true)
	if s.Available {
		t.Fatal("dismissed notice came back")
	}
	if !strings.Contains(s.Message, "v1.5.0") {
		t.Fatalf("manual message: %q", s.Message)
	}
}

func TestUpdaterNoNoticeWhenCurrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"tag_name":"v1.4.3","html_url":"https://github.com/shekh-shehab/BDDM/releases/tag/v1.4.3"}`)
	}))
	defer srv.Close()
	var notices int
	u := newUpdater("1.4.3", nil, nil, func(title, body string) { notices++ })
	u.client, u.apiURL = srv.Client(), srv.URL
	s := u.Check(true)
	if s.Available || s.Version != "1.4.3" {
		t.Fatalf("status: %+v", s)
	}
	if s.Message != "You have the latest version." {
		t.Fatalf("message: %q", s.Message)
	}
	time.Sleep(30 * time.Millisecond)
	if notices != 0 {
		t.Fatal("should not notify when already up to date")
	}
}

func TestUpdaterSilentOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limit", http.StatusForbidden)
	}))
	defer srv.Close()
	var notices int
	u := newUpdater("1.4.3", nil, nil, func(title, body string) { notices++ })
	u.client, u.apiURL = srv.Client(), srv.URL
	s := u.Check(false)
	if s.Available || s.Message != "" {
		t.Fatalf("auto check should stay quiet: %+v", s)
	}
	s = u.Check(true)
	if s.Message != "Could not check right now." {
		t.Fatalf("manual message: %q", s.Message)
	}
	if notices != 0 {
		t.Fatal("should not notify on failure")
	}
}

func TestUpdaterHonorsAutomaticSetting(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, `{"tag_name":"v1.5.0","html_url":"https://github.com/shekh-shehab/BDDM/releases/tag/v1.5.0"}`)
	}))
	defer srv.Close()
	on := false
	u := newUpdater("1.4.3", func() bool { return on }, nil, nil)
	u.client, u.apiURL, u.period = srv.Client(), srv.URL, 50*time.Millisecond
	u.Start()
	time.Sleep(120 * time.Millisecond)
	if hits.Load() != 0 {
		t.Fatalf("checked while automatic checking is off: %d", hits.Load())
	}
	// A manual check still works.
	s := u.Check(true)
	if !s.Available {
		t.Fatalf("manual check: %+v", s)
	}
	u.Stop()
	select {
	case <-u.stopped:
	case <-time.After(time.Second):
		t.Fatal("updater did not stop")
	}
}

func TestSelectSetupAssets(t *testing.T) {
	setup, sum := selectSetupAssets([]githubAsset{
		{Name: "INSTALL.txt", Size: 100, BrowserDownloadURL: "https://example.com/INSTALL.txt"},
		{Name: "BDDM-Setup.exe", Size: 3456, BrowserDownloadURL: "https://example.com/BDDM-Setup.exe"},
		{Name: "BDDM-Setup.exe.sha256", Size: 80, BrowserDownloadURL: "https://example.com/BDDM-Setup.exe.sha256"},
		{Name: "BDDM-chromium-extension.zip", Size: 10, BrowserDownloadURL: "https://example.com/c.zip"},
	})
	if setup == nil || setup.Size != 3456 || !strings.HasSuffix(setup.URL, "BDDM-Setup.exe") {
		t.Fatalf("setup: %+v", setup)
	}
	if sum == nil || !strings.HasSuffix(sum.URL, "sha256") {
		t.Fatalf("sum: %+v", sum)
	}

	// Case-insensitive name; missing checksum is fine.
	setup, sum = selectSetupAssets([]githubAsset{
		{Name: "bddm-setup.EXE", Size: 2, BrowserDownloadURL: "https://example.com/setup"},
	})
	if setup == nil || setup.Size != 2 || sum != nil {
		t.Fatalf("case: %+v %+v", setup, sum)
	}

	setup, sum = selectSetupAssets([]githubAsset{
		{Name: "notes.txt", Size: 1, BrowserDownloadURL: "https://example.com/notes.txt"},
		{Name: "BDDM-Setup.exe", Size: 9, BrowserDownloadURL: ""}, // no URL: skip
	})
	if setup != nil || sum != nil {
		t.Fatalf("missing url should be skipped: %+v %+v", setup, sum)
	}
}

func TestParseLatestReleaseAssets(t *testing.T) {
	body := []byte(`{
		"tag_name":"v1.5.0",
		"html_url":"https://github.com/shekh-shehab/BDDM/releases/tag/v1.5.0",
		"assets":[
			{"name":"BDDM-Setup.exe","size":12,"browser_download_url":"https://example.com/BDDM-Setup.exe"},
			{"name":"BDDM-Setup.exe.sha256","size":80,"browser_download_url":"https://example.com/BDDM-Setup.exe.sha256"}
		]
	}`)
	info, err := parseLatestRelease(body)
	if err != nil {
		t.Fatal(err)
	}
	if info.Setup == nil || info.Setup.Size != 12 || info.SHA256 == nil {
		t.Fatalf("assets: %+v", info)
	}
}

func TestParseSHA256File(t *testing.T) {
	const hash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // SHA-256 of empty
	cases := []string{
		hash,
		hash + "  BDDM-Setup.exe",
		hash + " *BDDM-Setup.exe",
		"SHA256 (BDDM-Setup.exe) = " + hash,
		strings.ToUpper(hash) + "  BDDM-Setup.exe\n",
	}
	for _, in := range cases {
		got, err := parseSHA256File([]byte(in))
		if err != nil || got != hash {
			t.Errorf("parseSHA256File(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := parseSHA256File([]byte("")); err == nil {
		t.Fatal("empty checksum should fail")
	}
	if _, err := parseSHA256File([]byte("not-a-hash")); err == nil {
		t.Fatal("garbage checksum should fail")
	}
}

func TestVerifySetupFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "BDDM-Setup.exe")
	data := []byte("BDDM setup test payload")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])

	if err := verifySetupFile(path, int64(len(data)), ""); err != nil {
		t.Fatalf("size only: %v", err)
	}
	if err := verifySetupFile(path, int64(len(data)), hexSum); err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := verifySetupFile(path, int64(len(data))+1, ""); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("size mismatch: %v", err)
	}
	if err := verifySetupFile(path, int64(len(data)), strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("hash mismatch: %v", err)
	}
	if err := verifySetupFile(filepath.Join(dir, "missing"), 1, ""); err == nil {
		t.Fatal("missing file should fail")
	}
}

func releaseJSON(t *testing.T, base, setupURL, sumURL string, setupSize int) []byte {
	t.Helper()
	assets := []map[string]any{{
		"name": "BDDM-Setup.exe", "size": setupSize, "browser_download_url": setupURL,
	}}
	if sumURL != "" {
		assets = append(assets, map[string]any{
			"name": "BDDM-Setup.exe.sha256", "size": 80, "browser_download_url": sumURL,
		})
	}
	body, _ := json.Marshal(map[string]any{
		"tag_name": "v1.5.0",
		"html_url": "https://github.com/shekh-shehab/BDDM/releases/tag/v1.5.0",
		"assets":   assets,
	})
	return body
}

func TestInstallDownloadAndVerify(t *testing.T) {
	payload := []byte("fake-bddm-setup-bytes")
	sum := sha256.Sum256(payload)
	hexSum := hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("must not send a token")
		}
		w.Write(releaseJSON(t, srv.URL, srv.URL+"/files/BDDM-Setup.exe", srv.URL+"/files/BDDM-Setup.exe.sha256", len(payload)))
	})
	mux.HandleFunc("/files/BDDM-Setup.exe", func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	})
	mux.HandleFunc("/files/BDDM-Setup.exe.sha256", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, hexSum+"  BDDM-Setup.exe\n")
	})

	var started string
	var quit atomic.Bool
	u := newUpdater("1.4.4", nil, nil, nil)
	u.client, u.dlClient, u.apiURL, u.dir = srv.Client(), srv.Client(), srv.URL+"/releases/latest", t.TempDir()
	u.runSetup = func(path string) error { started = path; return nil }
	u.quitFn = func() { quit.Store(true) }

	if _, err := fetchLatestRelease(u.client, u.apiURL, "1.4.4"); err != nil {
		t.Fatal(err)
	}
	s := u.Check(true)
	if !s.Available || !s.CanInstall {
		t.Fatalf("status after check: %+v", s)
	}
	u.Install()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := u.Status()
		if started != "" && quit.Load() {
			break
		}
		if st.State == "error" {
			t.Fatalf("install error: %s", st.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if started == "" || !quit.Load() {
		t.Fatalf("setup not started: %q quit=%v status=%+v", started, quit.Load(), u.Status())
	}
	got, err := os.ReadFile(started)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("installed file: %v %q", err, got)
	}
}

func TestInstallSizeOnlyWithoutHash(t *testing.T) {
	payload := []byte("setup-without-hash")
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Write(releaseJSON(t, srv.URL, srv.URL+"/files/BDDM-Setup.exe", "", len(payload)))
	})
	mux.HandleFunc("/files/BDDM-Setup.exe", func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	})
	var started bool
	u := newUpdater("1.4.4", nil, nil, nil)
	u.client, u.dlClient, u.apiURL, u.dir = srv.Client(), srv.Client(), srv.URL+"/releases/latest", t.TempDir()
	u.runSetup = func(path string) error { started = true; return nil }
	u.quitFn = func() {}
	u.Check(false)
	u.Install()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !started {
		if u.Status().State == "error" {
			t.Fatalf("error: %s", u.Status().Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !started {
		t.Fatalf("should install when only the size matches: %+v", u.Status())
	}
}

func TestInstallRejectsBadSizeAndHash(t *testing.T) {
	payload := []byte("abc") // 3 bytes
	sum := sha256.Sum256(payload)
	good := hex.EncodeToString(sum[:])
	bad := strings.Repeat("ab", 32)

	try := func(name string, advertisedSize int, sumHex string, wantSub string) {
		t.Helper()
		mux := http.NewServeMux()
		srv := httptest.NewServer(mux)
		defer srv.Close()
		sumURL := ""
		if sumHex != "" {
			sumURL = srv.URL + "/files/BDDM-Setup.exe.sha256"
		}
		mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
			w.Write(releaseJSON(t, srv.URL, srv.URL+"/files/BDDM-Setup.exe", sumURL, advertisedSize))
		})
		mux.HandleFunc("/files/BDDM-Setup.exe", func(w http.ResponseWriter, r *http.Request) {
			w.Write(payload)
		})
		mux.HandleFunc("/files/BDDM-Setup.exe.sha256", func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, sumHex+"  BDDM-Setup.exe\n")
		})
		var started bool
		u := newUpdater("1.4.4", nil, nil, nil)
		u.client, u.dlClient, u.apiURL, u.dir = srv.Client(), srv.Client(), srv.URL+"/releases/latest", t.TempDir()
		u.runSetup = func(path string) error { started = true; return nil }
		u.Check(false)
		u.Install()
		deadline := time.Now().Add(5 * time.Second)
		var st UpdateStatus
		for time.Now().Before(deadline) {
			st = u.Status()
			if st.State == "error" || started {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if started || st.State != "error" || !strings.Contains(st.Error, wantSub) {
			t.Fatalf("%s: started=%v status=%+v", name, started, st)
		}
	}
	try("size", 99, "", "size")
	try("hash", len(payload), bad, "checksum")
	_ = good
}

func TestInstallCancel(t *testing.T) {
	started := make(chan struct{})
	block := make(chan struct{})
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Write(releaseJSON(t, srv.URL, srv.URL+"/files/BDDM-Setup.exe", "", 1<<20))
	})
	mux.HandleFunc("/files/BDDM-Setup.exe", func(w http.ResponseWriter, r *http.Request) {
		close(started)
		w.Write([]byte("xx"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-block
		w.Write(make([]byte, 1<<20-2))
	})
	var ranSetup bool
	u := newUpdater("1.4.4", nil, nil, nil)
	u.client, u.dlClient, u.apiURL, u.dir = srv.Client(), srv.Client(), srv.URL+"/releases/latest", t.TempDir()
	u.runSetup = func(path string) error { ranSetup = true; return nil }
	u.Check(false)
	u.Install()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("download did not start")
	}
	u.Cancel()
	close(block)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st := u.Status()
		if st.State == "" && !ranSetup {
			return
		}
		if ranSetup || st.State == "installing" {
			t.Fatal("cancelled download still installed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := u.Status()
	if ranSetup || st.State == "error" && st.Error == "" {
		t.Fatalf("after cancel: %+v", st)
	}
	if st.State != "" && st.State != "error" {
		// A late size-mismatch error after cancel is also acceptable if the
		// handler unblocked and the context already ended; require no setup.
		if ranSetup {
			t.Fatalf("after cancel: %+v", st)
		}
	}
}
