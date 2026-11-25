package main

// 1.4.1: requests that look like the browser's own (headers.go), against
// local servers that check headers, cookies, redirects and request counts.

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const ffUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:145.0) Gecko/20100101 Firefox/145.0"

// gate checks every request with ok (403 when it says no) before xs
// serves it, and remembers the headers of every request.
type gate struct {
	xs   *xServer
	ok   func(r *http.Request) string // "" = allowed, else why not
	mu   sync.Mutex
	seen []http.Header
	head atomic.Int64
	gets atomic.Int64
}

func (g *gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.seen = append(g.seen, r.Header.Clone())
	g.mu.Unlock()
	if r.Method == http.MethodHead {
		g.head.Add(1)
	} else {
		g.gets.Add(1)
	}
	if g.ok != nil {
		if why := g.ok(r); why != "" {
			http.Error(w, why, http.StatusForbidden)
			return
		}
	}
	g.xs.ServeHTTP(w, r)
}

func (g *gate) headers() []http.Header {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]http.Header(nil), g.seen...)
}

// Fix 1 + 10 + 11: a site that only serves the browser's own User-Agent and
// languages (like Cloudflare's clearance cookie, which is bound to the UA).
func TestBrowserUserAgentAndLanguage(t *testing.T) {
	data := randomData(t, 3<<20)
	g := &gate{xs: &xServer{data: data}, ok: func(r *http.Request) string {
		if r.Header.Get("User-Agent") != ffUA {
			return "user agent"
		}
		if r.Header.Get("Accept-Language") != "bn-BD,bn;q=0.9,en;q=0.8" || r.Header.Get("Accept") == "" {
			return "accept headers"
		}
		return ""
	}}
	srv := httptest.NewServer(g)
	defer srv.Close()
	m, dl := newTestManager(t, 4)

	j, _ := m.Add(AddRequest{URL: srv.URL + "/a.bin", Force: true, UserAgent: ffUA, AcceptLanguage: "bn-BD,bn;q=0.9,en;q=0.8"})
	if v := waitDone(t, m, j.ID, time.Minute); v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	checkFile(t, filepath.Join(dl, "a.bin"), data)

	// Without the browser's details (an old extension): refused, and the
	// message says so plainly, with what to do.
	j, _ = m.Add(AddRequest{URL: srv.URL + "/b.bin", Force: true})
	v := waitDone(t, m, j.ID, time.Minute)
	if v.Status != StatusError || !strings.Contains(v.Reason, "refused") || !strings.Contains(v.Reason, "403 Forbidden") ||
		!strings.Contains(v.Fix, "again from the website") || !strings.Contains(v.Fix, "hold Alt") {
		t.Fatalf("%s reason=%q fix=%q", v.Status, v.Reason, v.Fix)
	}
	if strings.Contains(strings.ToLower(v.Reason), "probably expired") {
		t.Fatalf("misleading reason: %q", v.Reason)
	}
}

// Fix 2: the headers the browser sent (Origin, a player's token header)
// are copied; mirrors never get them; history.json never has them.
func TestCapturedHeadersNotForMirrors(t *testing.T) {
	data := randomData(t, 8<<20)
	main := &gate{xs: &xServer{data: data, rate: 4 << 20}, ok: func(r *http.Request) string {
		if r.Header.Get("Origin") != "https://player.example" || r.Header.Get("X-Player-Token") != "secret-t0k" {
			return "player headers missing"
		}
		return ""
	}}
	mirror := &gate{xs: &xServer{data: data, rate: 4 << 20}}
	sm, sx := httptest.NewServer(main), httptest.NewServer(mirror)
	defer sm.Close()
	defer sx.Close()
	m, dl := newTestManager(t, 4)
	j, err := m.Add(AddRequest{URL: sm.URL + "/v.mp4", Force: true, Referer: "https://player.example/embed/1",
		Cookie: "sid=cookie-secret", UserAgent: ffUA, Mirrors: []string{sx.URL + "/v.mp4"},
		Headers: map[string]string{"Origin": "https://player.example", "X-Player-Token": "secret-t0k",
			"Host": "evil", "Range": "bytes=5-", "Sec-Fetch-Mode": "cors", "Bad\nName": "x", "X-Long": strings.Repeat("a", 9000)}})
	if err != nil {
		t.Fatal(err)
	}
	v := waitDone(t, m, j.ID, time.Minute)
	if v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	checkFile(t, filepath.Join(dl, "v.mp4"), data)
	if mirror.gets.Load() == 0 {
		t.Fatal("the mirror was not used")
	}
	for _, h := range mirror.headers() {
		if h.Get("X-Player-Token") != "" || h.Get("Origin") != "" || h.Get("Cookie") != "" {
			t.Fatalf("the mirror got the browser's private headers: %v", h)
		}
		if h.Get("User-Agent") != ffUA {
			t.Fatalf("mirror user agent %q", h.Get("User-Agent"))
		}
	}
	for _, h := range main.headers() {
		if h.Get("X-Long") != "" || h.Get("Sec-Fetch-Mode") != "" {
			t.Fatalf("headers that should be dropped were sent: %v", h)
		}
	}
	m.mu.Lock()
	m.saveHistoryLocked()
	m.mu.Unlock()
	m.Shutdown()
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(dl), "cfg", "*.json"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), "secret-t0k") || strings.Contains(string(b), "X-Player-Token") {
			t.Fatalf("%s has the captured headers", filepath.Base(f))
		}
	}
	if len(files) == 0 {
		t.Fatal("no history file to check")
	}
}

// Fix 7: the page's link redirects to a CDN on another host; the CDN gets
// its own cookies (and not those of other sites).
func TestCookiesFollowRedirectToCDN(t *testing.T) {
	data := randomData(t, 4<<20)
	cdn := &gate{xs: &xServer{data: data}, ok: func(r *http.Request) string {
		c := r.Header.Get("Cookie")
		if !strings.Contains(c, "cdn_auth=yes") {
			return "no cdn cookie: " + c
		}
		if strings.Contains(c, "page_sid") || strings.Contains(c, "other") {
			return "cookies of another site were sent: " + c
		}
		return ""
	}}
	sc := httptest.NewServer(cdn)
	defer sc.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(sc.URL, "http://"))
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Cookie"), "page_sid=1") {
			http.Error(w, "page cookie missing", 403)
			return
		}
		http.Redirect(w, r, "http://localhost:"+port+"/files/movie.bin?sig=abc", http.StatusFound)
	}))
	defer page.Close()
	m, dl := newTestManager(t, 4)
	j, _ := m.Add(AddRequest{URL: page.URL + "/get/42", Force: true, Cookie: "page_sid=1", Filename: "movie.bin",
		Cookies: []hostCookie{
			{Name: "page_sid", Value: "1", Domain: "127.0.0.1", HostOnly: true},
			{Name: "cdn_auth", Value: "yes", Domain: "localhost", Path: "/files", HostOnly: true},
			{Name: "cdn_other_path", Value: "x", Domain: "localhost", Path: "/elsewhere", HostOnly: true},
			{Name: "other", Value: "z", Domain: "example.com"},
		}})
	v := waitDone(t, m, j.ID, time.Minute)
	if v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	checkFile(t, filepath.Join(dl, "movie.bin"), data)
	for _, h := range cdn.headers() {
		if strings.Contains(h.Get("Cookie"), "cdn_other_path") {
			t.Fatalf("a cookie of another path was sent: %q", h.Get("Cookie"))
		}
	}
}

// Fix 5: no HEAD request, and a small file needs just one request (the
// first answer becomes the file); with the "Download File Info" window,
// the window's look at the link is reused when Start is pressed.
func TestFewerRequests(t *testing.T) {
	data := randomData(t, 600<<10)
	g := &gate{xs: &xServer{data: data}}
	srv := httptest.NewServer(g)
	defer srv.Close()
	m, dl := newTestManager(t, 4)
	j, _ := m.Add(AddRequest{URL: srv.URL + "/small.bin", Force: true})
	if v := waitDone(t, m, j.ID, time.Minute); v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	checkFile(t, filepath.Join(dl, "small.bin"), data)
	if g.head.Load() != 0 || g.gets.Load() != 1 {
		t.Fatalf("small file: %d HEAD and %d GET requests, want 0 and 1", g.head.Load(), g.gets.Load())
	}

	// A bigger file in 4 parts: the first answer is part 1's start.
	big := randomData(t, 8<<20)
	g2 := &gate{xs: &xServer{data: big}}
	srv2 := httptest.NewServer(g2)
	defer srv2.Close()
	j, _ = m.Add(AddRequest{URL: srv2.URL + "/big.bin", Force: true})
	if v := waitDone(t, m, j.ID, time.Minute); v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	checkFile(t, filepath.Join(dl, "big.bin"), big)
	if g2.head.Load() != 0 || g2.gets.Load() > 5 {
		t.Fatalf("4 parts: %d HEAD and %d GET requests, want 0 and at most 5", g2.head.Load(), g2.gets.Load())
	}
	t.Logf("8 MB in 4 parts: %d requests; served %d bytes", g2.gets.Load(), g2.xs.served.Load())
	if g2.xs.served.Load() > int64(len(big))+(3<<20) {
		t.Fatalf("too much data asked for: %d", g2.xs.served.Load())
	}

	// The "Download File Info" window.
	g3 := &gate{xs: &xServer{data: data}}
	srv3 := httptest.NewServer(g3)
	defer srv3.Close()
	pj, err := m.AddPending(AddRequest{URL: srv3.URL + "/asked.bin", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		p, err := m.Pending(pj.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !p.Checking {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the window never finished checking")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := m.StartPending(pj.ID, "", "", false, ""); err != nil {
		t.Fatal(err)
	}
	if v := waitDone(t, m, pj.ID, time.Minute); v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	if g3.head.Load() != 0 || g3.gets.Load() != 2 {
		t.Fatalf("with the window: %d HEAD and %d GET requests, want 0 and 2", g3.head.Load(), g3.gets.Load())
	}
}

// A server that refuses any Range header still works (asked again
// without it), and resuming does not fetch the start of the file again.
func TestProbeWithoutRanges(t *testing.T) {
	data := randomData(t, 2<<20)
	g := &gate{xs: &xServer{data: data}, ok: func(r *http.Request) string {
		if r.Header.Get("Range") != "" {
			return "no ranges here"
		}
		return ""
	}}
	srv := httptest.NewServer(g)
	defer srv.Close()
	m, dl := newTestManager(t, 4)
	j, _ := m.Add(AddRequest{URL: srv.URL + "/norange.bin", Force: true})
	if v := waitDone(t, m, j.ID, time.Minute); v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	checkFile(t, filepath.Join(dl, "norange.bin"), data)
}

// Fix 3: a video file itself goes to the fast engine; pages and streams
// still go to the video window (yt-dlp).
func TestDirectMediaUsesEngine(t *testing.T) {
	cases := []struct {
		req   AddRequest
		video bool
	}{
		{AddRequest{URL: "https://cdn.example/v/clip.mp4?sig=1", Kind: "video"}, false},
		{AddRequest{URL: "https://cdn.example/stream/abc", Kind: "media"}, false},
		{AddRequest{URL: "https://cdn.example/a/song.mp3", Kind: "video"}, false},
		{AddRequest{URL: "https://cdn.example/live/index.m3u8", Kind: "video"}, true},
		{AddRequest{URL: "https://cdn.example/x/clip.mp4", Kind: "video", Stream: "hls"}, true},
		{AddRequest{URL: "https://cdn.example/x/clip.mp4", Kind: "video", Dialog: true}, true},
		{AddRequest{URL: "https://cdn.example/x/clip.mp4", Kind: "media", Dialog: true}, true},
		{AddRequest{URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", Kind: "video"}, true},
		{AddRequest{URL: "https://vimeo.com/123456", Kind: "video"}, true},
		{AddRequest{URL: "https://cdn.example/file.zip"}, false},
	}
	for _, c := range cases {
		if got := isVideoRequest(c.req); got != c.video {
			t.Errorf("%s (kind %q): video=%v, want %v", c.req.URL, c.req.Kind, got, c.video)
		}
	}
}

// 1.4.2: the Referer the browser really sent wins; without it a stream
// gets what the browser sends by default (only the site for another site).
func TestBetterReferer(t *testing.T) {
	h := map[string]string{"Referer": "https://site.example/watch/123?t=4"}
	if got := betterReferer("https://site.example/", "https://cdn.example/a.mp4", h, false); got != "https://site.example/watch/123?t=4" {
		t.Fatalf("got %q", got)
	}
	real := map[string]string{"referer": "https://megaplay.buzz/"}
	frame := "https://megaplay.buzz/stream/s-2/810433/sub?s=tcdn"
	stream := "https://megap.shiora.top/2d0a/798a/master.m3u8"
	if got := betterReferer(frame, stream, real, true); got != "https://megaplay.buzz/" {
		t.Fatalf("the browser's real referer was not used: %q", got)
	}
	if got := betterReferer(frame, stream, nil, true); got != "https://megaplay.buzz/" {
		t.Fatalf("cross-site stream without a captured referer: %q", got)
	}
	if got := betterReferer("https://megaplay.buzz/p#x", "https://megaplay.buzz/a.m3u8", nil, true); got != "https://megaplay.buzz/p" {
		t.Fatalf("same-site stream: %q", got)
	}
	if got := betterReferer("https://site.example/page", "https://cdn.example/f.zip", nil, false); got != "https://site.example/page" {
		t.Fatalf("a file download's referer changed: %q", got)
	}
	if got := policyReferer("https://a.example/x", "http://b.example/y"); got != "" {
		t.Fatalf("https -> http should send no referer, got %q", got)
	}
}

func TestCookieMatching(t *testing.T) {
	set := cleanCookies([]hostCookie{
		{Name: "a", Value: "1", Domain: ".example.com"},
		{Name: "b", Value: "2", Domain: "www.example.com", HostOnly: true},
		{Name: "s", Value: "3", Domain: "example.com", Secure: true},
		{Name: "p", Value: "4", Domain: "example.com", Path: "/dl"},
		{Name: "bad;", Value: "x", Domain: "example.com"},
		{Name: "n", Value: "5", Domain: "notexample.com"},
	})
	if len(set) != 5 {
		t.Fatalf("cleanCookies kept %d", len(set))
	}
	names := func(raw string) string {
		u, _ := url.Parse(raw)
		var out []string
		for _, c := range set.forURL(u) {
			out = append(out, c.Name)
		}
		return strings.Join(out, ",")
	}
	for raw, want := range map[string]string{
		"https://cdn.example.com/dl/x":  "p,a,s",
		"http://cdn.example.com/x":      "a",
		"https://www.example.com/x":     "a,b,s",
		"https://example.com/dlx":       "a,s",
		"https://evil-example.com/dl/x": "",
	} {
		if got := names(raw); got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
	if got := mergeCookieHeader("a=browser; z=9", set); got != "a=browser; z=9; b=2; s=3; p=4; n=5" {
		t.Errorf("merge: %q", got)
	}
}

// Fix 4: yt-dlp gets the browser's user agent, referer, Origin, the
// player's headers and a temporary cookies.txt (every host's cookies),
// which is deleted afterwards.
func TestYtdlpBrowserArgs(t *testing.T) {
	m, _ := newTestManager(t, 4)
	j := &Job{URL: "https://cdn.example/live/master.m3u8", Kind: "video", Stream: "hls",
		Referer: "https://site.example/watch/1", Cookie: "sid=abc"}
	j.applyBrowser(AddRequest{UserAgent: ffUA, Headers: map[string]string{"Origin": "https://player.example", "X-Player-Token": "t0k"},
		Cookies: []hostCookie{{Name: "edge", Value: "e1", Domain: ".segments.example", Path: "/"}}})
	args, done := m.baseArgs(j)
	joined := strings.Join(args, "\x00")
	for _, want := range []string{"--user-agent\x00" + ffUA, "--referer\x00https://site.example/watch/1",
		"--add-header\x00Origin:https://player.example", "--add-header\x00X-Player-Token:t0k"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, args)
		}
	}
	if strings.Contains(joined, "Cookie:") {
		t.Errorf("cookies passed as a header: %q", args)
	}
	cookieFile := ""
	for i, a := range args {
		if a == "--cookies" && i+1 < len(args) {
			cookieFile = args[i+1]
		}
	}
	b, err := os.ReadFile(cookieFile)
	if err != nil {
		t.Fatalf("cookie file: %v (%q)", err, args)
	}
	s := string(b)
	if !strings.HasPrefix(s, "# Netscape HTTP Cookie File") || !strings.Contains(s, "cdn.example\tFALSE\t/\tFALSE\t") ||
		!strings.Contains(s, "\tsid\tabc") || !strings.Contains(s, ".segments.example\tTRUE\t/\tFALSE\t") {
		t.Fatalf("cookie file:\n%s", s)
	}
	done()
	if fileExists(cookieFile) {
		t.Fatal("the cookie file was not deleted")
	}

	// YouTube keeps yt-dlp's own headers and no cookies.
	y := &Job{URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", Kind: "video", Cookie: "x=1"}
	y.applyBrowser(AddRequest{UserAgent: ffUA})
	yargs, ydone := m.baseArgs(y)
	defer ydone()
	if s := strings.Join(yargs, " "); strings.Contains(s, "--cookies") || strings.Contains(s, "--user-agent") {
		t.Fatalf("YouTube args changed: %q", yargs)
	}
}

// Fix 4 end to end with the real yt-dlp: the playlist needs the player's
// headers, the pieces are on another host that needs its own cookie.
func TestStreamWithCDNCookies(t *testing.T) {
	needVideoTools(t)
	dir := makeHLS(t, 4)
	var segs atomic.Int64
	segSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Cookie"), "edge=e1") || r.Header.Get("User-Agent") != ffUA {
			http.Error(w, "cdn: no cookie", 403)
			return
		}
		segs.Add(1)
		http.ServeFile(w, r, filepath.Join(dir, filepath.Base(r.URL.Path)))
	}))
	defer segSrv.Close()
	segHost := strings.TrimPrefix(segSrv.URL, "http://")
	plSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "https://player.example" || r.Header.Get("X-Player-Token") != "t0k" || r.Header.Get("User-Agent") != ffUA {
			http.Error(w, "playlist: player headers missing", 403)
			return
		}
		b, _ := os.ReadFile(filepath.Join(dir, "index.m3u8"))
		var out []string
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasSuffix(line, ".ts") {
				line = "http://" + segHost + "/" + line
			}
			out = append(out, line)
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprint(w, strings.Join(out, "\n"))
	}))
	defer plSrv.Close()
	// The playlist is on "localhost", the pieces on 127.0.0.1.
	plURL := strings.Replace(plSrv.URL, "127.0.0.1", "localhost", 1) + "/index.m3u8"
	m, dl := newTestManager(t, 4)
	if !m.tools.Ready() {
		t.Skip("video tools not found by BDDM")
	}
	m.mu.Lock()
	j := &Job{ID: newID(), URL: plURL, Kind: "video", Stream: "hls", Name: "CDN clip", Dir: dl, Parts: 4,
		Referer: "https://site.example/watch/1", Status: StatusQueued, Started: time.Now(),
		Video: &VideoOptions{Quality: "best", Title: "CDN clip"}}
	j.applyBrowser(AddRequest{UserAgent: ffUA, Headers: map[string]string{"Origin": "https://player.example", "X-Player-Token": "t0k"},
		Cookies: []hostCookie{{Name: "edge", Value: "e1", Domain: "127.0.0.1", Path: "/", HostOnly: true}}})
	m.jobs = append(m.jobs, j)
	m.scheduleLocked()
	m.mu.Unlock()
	v := waitDone(t, m, j.ID, 2*time.Minute)
	if v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	if segs.Load() == 0 {
		t.Fatal("no pieces were downloaded")
	}
	if d := videoSeconds(t, filepath.Join(dl, "CDN clip.mp4")); d < 3 {
		t.Fatalf("the video is %.1f s long", d)
	}
	left, _ := filepath.Glob(filepath.Join(os.TempDir(), "bddm-cookies-*.txt"))
	for _, f := range left {
		if st, err := os.Stat(f); err == nil && time.Since(st.ModTime()) < time.Minute {
			t.Fatalf("a cookie file was left behind: %s", f)
		}
	}
}

// Fix 11 for videos: a refused stream is explained plainly, with what to do.
func TestStreamRefusedMessage(t *testing.T) {
	j := &Job{URL: "https://cdn.example/a.m3u8", Stream: "hls"}
	msg := friendlyJobError(j, "ERROR: [generic] Unable to download webpage: HTTP Error 403: Forbidden")
	reason, fix := explainError(msg)
	if !strings.Contains(reason, "403 Forbidden") || !strings.Contains(fix, "hold Alt") {
		t.Fatalf("reason=%q fix=%q", reason, fix)
	}
	if msg := friendlyJobError(j, "HTTP Error 410: Gone"); msg != msgExpiredStream {
		t.Fatalf("410: %q", msg)
	}
}
