package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowKindForURL(t *testing.T) {
	base := "http://127.0.0.1:" + port
	cases := map[string]string{
		base:                         windowKindMain,
		base + "/":                   windowKindMain,
		base + "/?tab=settings":      windowKindPage,
		base + "/?add=1":             windowKindPage,
		base + "/tray.html":          windowKindTray,
		base + "/video.html?id=1":    windowKindPopup,
		base + "/confirm.html?id=1":  windowKindPopup,
		base + "/progress.html?id=1": windowKindPopup,
		base + "/complete.html?id=1": windowKindPopup,
		base + "/error.html?id=1":    windowKindPopup,
		base + "/drive.html?id=1":    windowKindPopup,
		base + "/torrent.html?id=1":  windowKindPopup,
	}
	for u, want := range cases {
		if got := windowKindForURL(u); got != want {
			t.Errorf("windowKindForURL(%q) = %q, want %q", u, got, want)
		}
	}
}

func TestWindowTitleForURL(t *testing.T) {
	base := "http://127.0.0.1:" + port
	cases := map[string]string{
		base + "/":                   "BDDM - Download Manager",
		base + "/tray.html":          "BDDM tray",
		base + "/video.html?id=1":    "Download Video",
		base + "/confirm.html?id=1":  "Download File Info",
		base + "/progress.html?id=1": "Download progress",
		base + "/complete.html?id=1": "Download complete",
		base + "/error.html?id=1":    "Download failed",
		base + "/drive.html?id=1":    "Download Google Drive folder",
		base + "/torrent.html?id=1":  "Add torrent",
	}
	for u, want := range cases {
		got := windowTitleForURL(u)
		if got != want {
			t.Errorf("windowTitleForURL(%q) = %q, want %q", u, got, want)
		}
		if !isBDDMUITitle(got) {
			t.Errorf("%q must be recognised as a BDDM window title", got)
		}
	}
}

func TestIsLocalBDDMURL(t *testing.T) {
	for _, u := range []string{
		"http://127.0.0.1:" + port,
		"http://127.0.0.1:" + port + "/",
		"http://127.0.0.1:" + port + "/?tab=settings&scheme=dark",
		"http://127.0.0.1:" + port + "/video.html?id=abc123&scheme=light",
		"http://127.0.0.1:" + port + "/tray.html",
	} {
		if !isLocalBDDMURL(u) {
			t.Errorf("expected local %q", u)
		}
	}
	for _, u := range []string{
		"http://localhost:" + port + "/video.html?id=1",
		"http://[::1]:" + port + "/",
		"https://127.0.0.1:" + port + "/",
		"http://127.0.0.1:1/",
		"http://example.com:" + port + "/",
		"file:///C:/x.html",
		"javascript:alert(1)",
		"",
	} {
		if isLocalBDDMURL(u) {
			t.Errorf("did not expect local %q", u)
		}
	}
}

func TestWebViewDevToolsAndErrorStatus(t *testing.T) {
	setWebViewDevTools(false)
	t.Setenv("BDDM_DEVTOOLS", "")
	if webviewDevToolsEnabled() {
		t.Fatal("devtools should be off by default")
	}
	t.Setenv("BDDM_DEVTOOLS", "1")
	if !webviewDevToolsEnabled() {
		t.Fatal("BDDM_DEVTOOLS=1 should enable devtools")
	}
	t.Setenv("BDDM_DEVTOOLS", "")
	setWebViewDevTools(true)
	if !webviewDevToolsEnabled() {
		t.Fatal("-devtools flag should enable devtools")
	}
	setWebViewDevTools(false)
	setWebViewTestMode(false)
	t.Setenv("BDDM_WV2_TEST", "")
	if webviewTestModeEnabled() {
		t.Fatal("wv2test should be off by default")
	}
	t.Setenv("BDDM_WV2_TEST", "1")
	if !webviewTestModeEnabled() {
		t.Fatal("BDDM_WV2_TEST=1 should enable test mode")
	}
	t.Setenv("BDDM_WV2_TEST", "")
	setWebViewTestMode(true)
	if !webviewTestModeEnabled() {
		t.Fatal("-wv2test should enable test mode")
	}
	setWebViewTestMode(false)
	if !strings.Contains(webviewTestHTML, "color:#d00") || !strings.Contains(webviewTestHTML, ">test<") {
		t.Fatal("webviewTestHTML missing red test heading")
	}
	if got := webErrorStatusName(12); got != "CannotConnect" {
		t.Fatalf("webErrorStatusName(12) = %q", got)
	}
	if got := webErrorStatusName(14); got != "OperationCanceled" {
		t.Fatalf("webErrorStatusName(14) = %q", got)
	}
}

func TestWindowRectArgsRoundTrip(t *testing.T) {
	r := windowRect{-5, 20, 900, 650}
	args := windowHostArgs("http://127.0.0.1:"+port+"/", windowKindMain, r)
	want := []string{"-window", "http://127.0.0.1:" + port + "/", "-window-kind", "main", "-window-rect", "-5,20,900,650"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("args = %v", args)
	}
	got, ok := parseWindowRect(args[5])
	if !ok || got != r {
		t.Fatalf("parse = %+v %v", got, ok)
	}
	for _, bad := range []string{"", "1,2,3", "a,b,c,d", "0,0,10,10", "0,0,99999,500"} {
		if _, ok := parseWindowRect(bad); ok {
			t.Errorf("parseWindowRect(%q) should fail", bad)
		}
	}
	for _, k := range []string{windowKindMain, windowKindPage, windowKindPopup, windowKindTray} {
		if !validWindowKind(k) {
			t.Errorf("kind %q should be valid", k)
		}
	}
	if validWindowKind("evil") {
		t.Fatal("unknown kind accepted")
	}
}

func TestWebViewDataDirs(t *testing.T) {
	prev := bddmConfigDirOverride
	bddmConfigDirOverride = ""
	defer func() { bddmConfigDirOverride = prev }()
	t.Setenv("LOCALAPPDATA", filepath.Join(t.TempDir(), "Local"))
	got := webviewDataDir()
	if !strings.HasSuffix(got, filepath.Join("Local", "BDDM", "webview2")) {
		t.Fatalf("webviewDataDir = %q", got)
	}
	if fb := edgeFallbackDir("app"); !strings.Contains(fb, filepath.Join("BDDM", "edge-fallback")) {
		t.Fatalf("edgeFallbackDir = %q", fb)
	}
	if ld := webviewLogDir(); !strings.HasSuffix(ld, filepath.Join("Local", "BDDM", "logs")) {
		t.Fatalf("webviewLogDir = %q", ld)
	}
	if lp := webviewLogPath(); !strings.HasSuffix(lp, filepath.Join("Local", "BDDM", "logs", "window-host.log")) {
		t.Fatalf("webviewLogPath = %q", lp)
	}
}

func TestEdgeFallbackFlagsDisableSync(t *testing.T) {
	joined := strings.Join(edgeFallbackFlags(), " ")
	for _, want := range []string{"--no-first-run", "--no-default-browser-check", "--disable-sync", "msImplicitSignin"} {
		if !strings.Contains(joined, want) {
			t.Errorf("fallback flags missing %q: %s", want, joined)
		}
	}
	if !strings.Contains(webview2RuntimeURL, "go.microsoft.com") || !strings.Contains(webview2NoticeText, "WebView2 Runtime") {
		t.Fatal("runtime notice must point to Microsoft's Evergreen download")
	}
	assertEnglishUI(t, "webview2 notice", webview2NoticeText)
}

func TestCleanupOldChromiumProfiles(t *testing.T) {
	dir := t.TempDir()
	prev := bddmConfigDirOverride
	bddmConfigDirOverride = dir
	defer func() { bddmConfigDirOverride = prev }()
	for _, d := range oldChromiumProfileDirs() {
		if err := os.MkdirAll(filepath.Join(d, "Default"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(dir, "settings.json")
	_ = os.WriteFile(keep, []byte("{}"), 0644)
	if n := cleanupOldChromiumProfiles(); n != 3 {
		t.Fatalf("removed %d, want 3", n)
	}
	for _, d := range oldChromiumProfileDirs() {
		if _, err := os.Stat(d); err == nil {
			t.Errorf("%s still exists", d)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("cleanup must not touch other BDDM files")
	}
	if n := cleanupOldChromiumProfiles(); n != 0 {
		t.Fatalf("second cleanup removed %d", n)
	}
}

func TestMainWindowStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	prev := bddmConfigDirOverride
	bddmConfigDirOverride = dir
	defer func() { bddmConfigDirOverride = prev }()
	if _, ok := loadMainWindowState(); ok {
		t.Fatal("no state expected yet")
	}
	s := savedWindow{X: 100, Y: 80, W: 1000, H: 700, DPI: 96}
	if err := saveMainWindowState(s); err != nil {
		t.Fatal(err)
	}
	got, ok := loadMainWindowState()
	if !ok || got != s {
		t.Fatalf("loaded %+v %v", got, ok)
	}
}

func TestRestoreMainRect(t *testing.T) {
	// The user's own size is kept.
	r, ok := restoreMainRect(savedWindow{X: 100, Y: 80, W: 1000, H: 700, DPI: 96}, 0, 0, 1920, 1040, 96)
	if !ok || r != (windowRect{100, 80, 1000, 700}) {
		t.Fatalf("restore = %+v %v", r, ok)
	}
	// Scaled when the DPI changed.
	r, ok = restoreMainRect(savedWindow{X: 100, Y: 80, W: 1000, H: 700, DPI: 96}, 0, 0, 2880, 1560, 144)
	if !ok || r.W != 1500 || r.H != 1050 {
		t.Fatalf("dpi restore = %+v %v", r, ok)
	}
	// Maximized / full-screen leftovers, tiny sizes and off-screen windows fall back to the default.
	for _, s := range []savedWindow{
		{X: 0, Y: 0, W: 1920, H: 1040, DPI: 96},
		{X: 0, Y: 0, W: 300, H: 200, DPI: 96},
		{X: 3000, Y: 80, W: 1000, H: 700, DPI: 96},
		{X: 100, Y: 80, W: 0, H: 700, DPI: 96},
	} {
		if _, ok := restoreMainRect(s, 0, 0, 1920, 1040, 96); ok {
			t.Errorf("restoreMainRect(%+v) should fall back to the default", s)
		}
	}
	// Partly off-screen but mostly visible: pulled inside the work area.
	r, ok = restoreMainRect(savedWindow{X: 1200, Y: 80, W: 1000, H: 700, DPI: 96}, 0, 0, 1920, 1040, 96)
	if !ok || r.X+r.W > 1920 {
		t.Fatalf("clamp = %+v %v", r, ok)
	}
}

func TestIsBDDMHostWindow(t *testing.T) {
	self := `C:\Users\x\AppData\Local\Programs\BDDM\BDDM.exe`
	for _, cls := range []string{goWebView2Class, bddmWebViewClass} {
		if !isBDDMHostWindow(cls, self, self) {
			t.Fatalf("own WebView2 window (%s) not recognised", cls)
		}
	}
	if isBDDMHostWindow("Chrome_WidgetWin_1", self, self) {
		t.Fatal("other window classes are not BDDM host windows")
	}
	if isBDDMHostWindow(bddmWebViewClass, `C:\Other\app.exe`, self) {
		t.Fatal("another program using the same class name must not match")
	}
	if !isBDDMHostWindow(bddmWebViewClass, `C:\x\bddm.EXE`, "") {
		t.Fatal("fallback to BDDM.exe name")
	}
}

func TestWebViewInitScriptAndMessages(t *testing.T) {
	for _, want := range []string{"window.close = function", "bddm:close", "bddm:ready", "bddm:painted", "requestAnimationFrame", "bddm:title:", "chrome.webview.postMessage", "document.title"} {
		if !strings.Contains(webviewInitScript, want) {
			t.Errorf("init script missing %q", want)
		}
	}
	if c, _ := parseHostMessage("bddm:close"); c != "close" {
		t.Fatal("close message")
	}
	if c, _ := parseHostMessage("bddm:ready"); c != "ready" {
		t.Fatal("ready message")
	}
	if c, _ := parseHostMessage("bddm:painted"); c != "painted" {
		t.Fatal("painted message")
	}
	if c, a := parseHostMessage("bddm:title:Download Video - Song"); c != "title" || a != "Download Video - Song" {
		t.Fatalf("title message %q %q", c, a)
	}
	if c, a := parseHostMessage("bddm:title:" + strings.Repeat("x", 500)); c != "title" || len(a) != 200 {
		t.Fatal("long titles are cut")
	}
	if c, _ := parseHostMessage(`{"id":1,"method":"x"}`); c != "" {
		t.Fatal("other messages are ignored")
	}
}

func TestWaitForServerReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ping" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"app":"BDDM"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	if !waitForServerReady(srv.URL+"/?tab=settings", time.Second) {
		t.Fatal("expected server ready")
	}
	srv.Close()
	if waitForServerReady(srv.URL+"/", 150*time.Millisecond) {
		t.Fatal("closed server must time out")
	}
	if waitForServerReady("not-a-url", 50*time.Millisecond) {
		t.Fatal("bad URL must return false")
	}
}

func TestWebViewInitScriptRunsCleanly(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	prelude := strings.Join([]string{
		"const posted = [];",
		"global.window = {",
		"  chrome: { webview: { postMessage: (m) => posted.push(m) } },",
		"  addEventListener: (ev, fn) => { if (ev === 'load') fn(); },",
		"  requestAnimationFrame: (fn) => fn(),",
		"  MutationObserver: function() { return { observe: () => {} }; },",
		"};",
		"global.MutationObserver = global.window.MutationObserver;",
		"global.document = {",
		"  title: 'BDDM - Download Manager',",
		"  body: null,",
		"  addEventListener: (ev, fn) => { if (ev === 'DOMContentLoaded') fn(); },",
		"  querySelector: () => ({}),",
		"};",
		"global.setInterval = () => 1;",
		"global.setTimeout = (fn) => fn();",
		"global.requestAnimationFrame = (fn) => fn();",
	}, "\n")
	tail := strings.Join([]string{
		"global.document.body = { children: [{}], innerText: 'Downloads' };",
		webviewInitScript,
		"window.close();",
		"if (!posted.includes('bddm:close')) throw new Error('missing bddm:close: ' + JSON.stringify(posted));",
		"if (!posted.some(m => m.startsWith('bddm:title:'))) throw new Error('missing title: ' + JSON.stringify(posted));",
		"if (!posted.includes('bddm:ready')) throw new Error('missing ready: ' + JSON.stringify(posted));",
		"if (!posted.includes('bddm:painted')) throw new Error('missing painted: ' + JSON.stringify(posted));",
	}, "\n")
	cmd := exec.Command(node, "-e", prelude+"\n"+webviewInitScript+"\n"+tail)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init script failed: %v\n%s", err, out)
	}
}

func TestWebViewBackgroundARGB(t *testing.T) {
	if got := webviewBackgroundARGB(true); got != "FF14161A" {
		t.Fatalf("dark: %s", got)
	}
	if got := webviewBackgroundARGB(false); got != "FFE6E9EE" {
		t.Fatalf("light: %s", got)
	}
	if len(webviewBackgroundARGB(true)) != 8 || len(webviewBackgroundARGB(false)) != 8 {
		t.Fatal("AARRGGBB must be 8 hex digits")
	}
}
