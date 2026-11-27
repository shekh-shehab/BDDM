package main

// webview.go: BDDM windows are embedded Microsoft WebView2 windows owned by
// BDDM.exe (bug 76). Each window runs in a small helper process
// ("BDDM.exe -window <url> ..."), so a WebView2 failure can never stop the
// download engine. When the WebView2 runtime is missing, BDDM falls back to
// an Edge --app window with sync and sign-in turned off.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Window kinds passed to the helper process.
const (
	windowKindMain  = "main"  // the main BDDM window (remembers its size)
	windowKindPage  = "page"  // other full pages: Settings, Add URL
	windowKindPopup = "popup" // compact dialogs, sized to their content
	windowKindTray  = "tray"  // the tray popup: no frame, closes on blur
)

const (
	bddmWebViewClass = "BDDMWebView"
	goWebView2Class  = "webview"
	// Evergreen WebView2 runtime bootstrapper (Microsoft).
	webview2RuntimeURL = "https://go.microsoft.com/fwlink/p/?LinkId=2124703"
	webview2NoticeText = "BDDM needs the Microsoft Edge WebView2 Runtime to show its windows.\n\n" +
		"It was not found on this PC, so BDDM is using a Microsoft Edge window for now " +
		"(sync and sign-in are turned off).\n\n" +
		"Do you want to download the WebView2 Runtime from Microsoft now?"
)

// windowKindForURL picks how a BDDM page is shown.
func windowKindForURL(u string) string {
	switch {
	case isMainAppURL(u):
		return windowKindMain
	case strings.HasSuffix(strings.ToLower(popupURLPath(u)), "/tray.html"):
		return windowKindTray
	case isPopupAppURL(u):
		return windowKindPopup
	}
	return windowKindPage
}

func validWindowKind(k string) bool {
	switch k {
	case windowKindMain, windowKindPage, windowKindPopup, windowKindTray:
		return true
	}
	return false
}

// windowTitleForURL is the first window title, before the page reports
// its own document.title. BDDM finds its windows by these titles.
func windowTitleForURL(u string) string {
	p := strings.ToLower(popupURLPath(u))
	switch {
	case strings.HasSuffix(p, "/tray.html"):
		return "BDDM tray"
	case strings.HasSuffix(p, "/video.html"):
		return "Download Video"
	case strings.HasSuffix(p, "/confirm.html"):
		return "Download File Info"
	case strings.HasSuffix(p, "/progress.html"):
		return "Download progress"
	case strings.HasSuffix(p, "/complete.html"):
		return "Download complete"
	case strings.HasSuffix(p, "/error.html"):
		return "Download failed"
	case strings.HasSuffix(p, "/drive.html"):
		return "Download Google Drive folder"
	case strings.HasSuffix(p, "/torrent.html"):
		return "Add torrent"
	case strings.HasSuffix(p, "/profile.html"):
		return "Download profile media"
	}
	return "BDDM - Download Manager"
}

// isLocalBDDMURL keeps the helper on BDDM's own pages (IPv4 127.0.0.1 only,
// never localhost/::1, with the exact BDDM port and any path or query string).
func isLocalBDDMURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "http" {
		return false
	}
	return u.Hostname() == "127.0.0.1" && u.Port() == port
}

// windowRect is a physical-pixel window rectangle.
type windowRect struct{ X, Y, W, H int }

func (r windowRect) String() string { return fmt.Sprintf("%d,%d,%d,%d", r.X, r.Y, r.W, r.H) }

func parseWindowRect(s string) (windowRect, bool) {
	parts := strings.Split(strings.TrimSpace(s), ",")
	if len(parts) != 4 {
		return windowRect{}, false
	}
	var v [4]int
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return windowRect{}, false
		}
		v[i] = n
	}
	r := windowRect{v[0], v[1], v[2], v[3]}
	if r.W < 50 || r.H < 50 || r.W > 20000 || r.H > 20000 {
		return windowRect{}, false
	}
	return r, true
}

// windowHostArgs are the helper-process arguments for one window.
func windowHostArgs(u, kind string, r windowRect) []string {
	return []string{"-window", u, "-window-kind", kind, "-window-rect", r.String()}
}

// windowHostThemeArgs passes the saved theme, so the title bar has the
// right colour before the page loads.
func windowHostThemeArgs(theme string) []string {
	return []string{"-window-theme", normalizeTheme(theme)}
}

var webviewDevTools atomic.Bool

func setWebViewDevTools(on bool) { webviewDevTools.Store(on) }

// webviewDevToolsEnabled is true when -devtools or BDDM_DEVTOOLS=1 is set.
func webviewDevToolsEnabled() bool {
	if webviewDevTools.Load() {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BDDM_DEVTOOLS"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

var webviewTestMode atomic.Bool

func setWebViewTestMode(on bool) { webviewTestMode.Store(on) }

// webviewTestModeEnabled is true when -wv2test or BDDM_WV2_TEST=1 is set.
// It renders an inline red "test" heading via NavigateToString so layout
// problems can be told apart from HTTP/navigation ones.
func webviewTestModeEnabled() bool {
	if webviewTestMode.Load() {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BDDM_WV2_TEST"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

const webviewTestHTML = "<!doctype html><html><head><meta charset=\"utf-8\"><title>BDDM WebView2 Test</title></head><body style=\"margin:24px;font-family:'Segoe UI',sans-serif;background:#fff;color:#111\"><h1 style=\"color:#d00;margin:0 0 8px\">test</h1><p>BDDM WebView2 NavigateToString OK</p></body></html>"

func webErrorStatusName(status int32) string {
	names := [...]string{
		"Unknown",
		"CertificateCommonNameIsIncorrect",
		"CertificateExpired",
		"ClientCertificateContainsErrors",
		"CertificateRevoked",
		"CertificateIsInvalid",
		"ServerUnreachable",
		"Timeout",
		"ErrorHttpInvalidServerResponse",
		"ConnectionAborted",
		"ConnectionReset",
		"Disconnected",
		"CannotConnect",
		"HostNameNotResolved",
		"OperationCanceled",
		"RedirectFailed",
		"UnexpectedError",
	}
	if status >= 0 && int(status) < len(names) {
		return names[status]
	}
	return fmt.Sprintf("Status(%d)", status)
}

// localDataDir is %LOCALAPPDATA%\BDDM (browser caches belong there, not in
// the roaming profile).
func localDataDir() string {
	if bddmConfigDirOverride != "" {
		return bddmConfigDirOverride
	}
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return filepath.Join(d, "BDDM")
	}
	if d, err := os.UserCacheDir(); err == nil && d != "" {
		return filepath.Join(d, "BDDM")
	}
	return filepath.Join(".", "BDDM-data")
}

// webviewDataDir is the WebView2 user-data folder shared by all BDDM windows.
func webviewDataDir() string { return filepath.Join(localDataDir(), "webview2") }

// webviewLogDir is %LOCALAPPDATA%\BDDM\logs for WebView2 window host logs.
func webviewLogDir() string { return filepath.Join(localDataDir(), "logs") }

// webviewLogPath is the WebView2 helper log file.
func webviewLogPath() string { return filepath.Join(webviewLogDir(), "window-host.log") }

// waitForServerReady waits until the local BDDM HTTP server answers /api/ping
// on 127.0.0.1 (never localhost/::1).
func waitForServerReady(rawURL string, timeout time.Duration) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Hostname() != "127.0.0.1" || u.Port() == "" {
		return false
	}
	pingURL := "http://127.0.0.1:" + u.Port() + "/api/ping"
	client := &http.Client{Timeout: 400 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	for {
		resp, err := client.Get(pingURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// edgeFallbackDir is the Edge profile used only when WebView2 is missing.
func edgeFallbackDir(kind string) string {
	return filepath.Join(localDataDir(), "edge-fallback", kind)
}

// edgeFallbackFlags turn off sync, sign-in and first-run pages in the Edge
// --app fallback.
func edgeFallbackFlags() []string {
	return []string{
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-sync",
		"--disable-features=msImplicitSignin,msEdgeSyncConsent,msSignInIntroPage,EdgeAccountSignIn",
		"--disable-background-networking",
	}
}

// oldChromiumProfileDirs are the Edge profiles from 1.5.1-1.5.3 that made
// Edge start Microsoft account sync. They are removed on upgrade.
func oldChromiumProfileDirs() []string {
	base := bddmConfigDir()
	return []string{
		filepath.Join(base, "chromium-app"),
		filepath.Join(base, "chromium-tray"),
		filepath.Join(base, "chromium-popup"),
	}
}

// cleanupOldChromiumProfiles deletes the old profile folders. A folder that
// Edge still holds open is left for the next start.
func cleanupOldChromiumProfiles() (removed int) {
	for _, d := range oldChromiumProfileDirs() {
		if _, err := os.Stat(d); err != nil {
			continue
		}
		if err := os.RemoveAll(d); err == nil {
			removed++
		}
	}
	return removed
}

// ---------------------------------------------------------------------------
// Main window size: remembered after the user moves or resizes it.
// ---------------------------------------------------------------------------

type savedWindow struct {
	X   int `json:"x"`
	Y   int `json:"y"`
	W   int `json:"w"` // physical pixels at DPI
	H   int `json:"h"`
	DPI int `json:"dpi"`
}

func mainWindowStatePath() string { return filepath.Join(bddmConfigDir(), "main-window.json") }

func loadMainWindowState() (savedWindow, bool) {
	b, err := os.ReadFile(mainWindowStatePath())
	if err != nil {
		return savedWindow{}, false
	}
	var s savedWindow
	if json.Unmarshal(b, &s) != nil || s.W <= 0 || s.H <= 0 {
		return savedWindow{}, false
	}
	return s, true
}

func saveMainWindowState(s savedWindow) error {
	if s.W <= 0 || s.H <= 0 {
		return nil
	}
	if err := os.MkdirAll(bddmConfigDir(), 0755); err != nil {
		return err
	}
	b, _ := json.Marshal(s)
	return os.WriteFile(mainWindowStatePath(), b, 0644)
}

// restoreMainRect turns a saved size into a rectangle for the current work
// area and DPI. It rejects sizes that are too small, too big (a maximized
// or full-screen leftover), or mostly off-screen.
func restoreMainRect(s savedWindow, workL, workT, workR, workB, dpi int) (windowRect, bool) {
	if dpi < 96 {
		dpi = 96
	}
	if s.DPI < 96 {
		s.DPI = 96
	}
	if workR <= workL || workB <= workT || s.W <= 0 || s.H <= 0 {
		return windowRect{}, false
	}
	w, h := s.W*dpi/s.DPI, s.H*dpi/s.DPI
	minW, minH := dipToPhysical(480, dpi), dipToPhysical(360, dpi)
	workW, workH := workR-workL, workB-workT
	if w < minW || h < minH || w >= workW || h >= workH {
		return windowRect{}, false
	}
	x, y := s.X, s.Y
	// At least half of the window must be on the work area.
	visW := minInt(x+w, workR) - maxInt(x, workL)
	visH := minInt(y+h, workB) - maxInt(y, workT)
	if visW < w/2 || visH < h/2 {
		return windowRect{}, false
	}
	if x < workL {
		x = workL
	}
	if y < workT {
		y = workT
	}
	if x+w > workR {
		x = workR - w
	}
	if y+h > workB {
		y = workB - h
	}
	return windowRect{x, y, w, h}, true
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// isBDDMHostWindow reports whether a top-level window is one of BDDM's own
// WebView2 windows (class BDDMWebView in a BDDM.exe process).
func isBDDMHostWindow(class, exe, selfExe string) bool {
	if (class != bddmWebViewClass && class != goWebView2Class) || exe == "" {
		return false
	}
	base := func(p string) string {
		if i := strings.LastIndexAny(p, `/\`); i >= 0 {
			p = p[i+1:]
		}
		return strings.ToLower(p)
	}
	if selfExe != "" {
		return base(exe) == base(selfExe)
	}
	return base(exe) == "bddm.exe"
}

// webviewBackgroundARGB is WEBVIEW2_DEFAULT_BACKGROUND_COLOR / ICoreWebView2Controller2
// DefaultBackgroundColor (AARRGGBB). Alpha is FF; the RGB matches theme.css --page.
func webviewBackgroundARGB(dark bool) string {
	if dark {
		return "FF14161A"
	}
	return "FFE6E9EE"
}

// webviewInitScript runs before every BDDM page: window.close() closes the
// BDDM window, and the window title follows document.title. "painted" is
// posted after two animation frames so the tray host can show only then.
const webviewInitScript = `(function () {
  var post = function (m) { try { window.chrome.webview.postMessage(m); } catch (e) {} };
  window.close = function () {
    if (window.bddmHostClose) { try { window.bddmHostClose(); return; } catch (e) {} }
    post("bddm:close");
  };
  var sentReady = false;
  var sentPainted = false;
  var sendPainted = function () {
    if (sentPainted) return;
    sentPainted = true;
    if (window.bddmHostStage) { try { window.bddmHostStage("painted"); } catch (e) {} }
    post("bddm:painted");
  };
  var afterPaint = function (fn) {
    if (typeof requestAnimationFrame === "function") {
      requestAnimationFrame(function () { requestAnimationFrame(fn); });
    } else if (typeof setTimeout === "function") {
      setTimeout(fn, 0);
    } else {
      fn();
    }
  };
  var sendReady = function () {
    if (sentReady) return;
    if (document.body && (document.body.children.length > 0 || (document.body.innerText || "").trim().length > 0)) {
      sentReady = true;
      if (window.bddmHostStage) { try { window.bddmHostStage("dom_ready"); } catch (e) {} }
      post("bddm:ready");
      afterPaint(sendPainted);
    }
  };
  var last = "";
  var sendTitle = function () {
    var t = document.title || "";
    if (t && t !== last) {
      last = t;
      if (window.bddmHostTitle) { try { window.bddmHostTitle(t); } catch (e) {} }
      post("bddm:title:" + t);
    }
  };
  document.addEventListener("DOMContentLoaded", function () {
    sendTitle();
    sendReady();
    var el = document.querySelector("title");
    if (el && window.MutationObserver) new MutationObserver(sendTitle).observe(el, { childList: true, characterData: true, subtree: true });
  });
  window.addEventListener("load", function () { sendTitle(); sendReady(); });
  setInterval(function () { sendTitle(); sendReady(); }, 500);
})();`

// parseHostMessage decodes a message posted by webviewInitScript.
func parseHostMessage(m string) (cmd, arg string) {
	switch {
	case m == "bddm:close":
		return "close", ""
	case m == "bddm:ready":
		return "ready", ""
	case m == "bddm:painted":
		return "painted", ""
	case strings.HasPrefix(m, "bddm:title:"):
		t := strings.TrimSpace(strings.TrimPrefix(m, "bddm:title:"))
		if len(t) > 200 {
			t = t[:200]
		}
		return "title", t
	case m == "bddm:theme:dark":
		return "theme", themeDark
	case m == "bddm:theme:light":
		return "theme", themeLight
	}
	return "", ""
}
