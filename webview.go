package main

// webview.go: BDDM windows are embedded Microsoft WebView2 windows owned by
// BDDM.exe (bug 76). Each window runs in a small helper process
// ("BDDM.exe -window <url> ..."), so a WebView2 failure can never stop the
// download engine. When the WebView2 runtime is missing, BDDM falls back to
// an Edge --app window with sync and sign-in turned off.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	}
	return "BDDM - Download Manager"
}

// isLocalBDDMURL keeps the helper on BDDM's own pages.
func isLocalBDDMURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "http" {
		return false
	}
	h := u.Hostname()
	return (h == "127.0.0.1" || h == "localhost") && u.Port() == port
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
	if class != bddmWebViewClass || exe == "" {
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

// webviewInitScript runs before every BDDM page: window.close() closes the
// BDDM window, and the window title follows document.title.
const webviewInitScript = `(function () {
  var post = function (m) { try { window.chrome.webview.postMessage(m); } catch (e) {} };
  window.close = function () { post("bddm:close"); };
  var last = "";
  var sendTitle = function () {
    var t = document.title || "";
    if (t && t !== last) { last = t; post("bddm:title:" + t); }
  };
  document.addEventListener("DOMContentLoaded", function () {
    sendTitle();
    var el = document.querySelector("title");
    if (el && window.MutationObserver) new MutationObserver(sendTitle).observe(el, { childList: true, characterData: true, subtree: true });
  });
  window.addEventListener("load", sendTitle);
  setInterval(sendTitle, 1000);
})();`

// parseHostMessage decodes a message posted by webviewInitScript.
func parseHostMessage(m string) (cmd, arg string) {
	switch {
	case m == "bddm:close":
		return "close", ""
	case strings.HasPrefix(m, "bddm:title:"):
		t := strings.TrimSpace(strings.TrimPrefix(m, "bddm:title:"))
		if len(t) > 200 {
			t = t[:200]
		}
		return "title", t
	}
	return "", ""
}
