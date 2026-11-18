package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
