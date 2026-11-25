package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Default main-window size in CSS pixels (DIP). Chromium --window-size
// uses this unit; Windows placement converts it with the monitor DPI.
const (
	mainWindowWidth  = 900
	mainWindowHeight = 650
)

const mainWindowDefaultMarker = "main-window-default-1.5.2"

// bddmConfigDirOverride, when set (tests), replaces the usual config folder.
var bddmConfigDirOverride string

func bddmConfigDir() string {
	if bddmConfigDirOverride != "" {
		return bddmConfigDirOverride
	}
	if d, err := os.UserConfigDir(); err == nil && d != "" {
		return filepath.Join(d, "BDDM")
	}
	return filepath.Join(".", "BDDM-data")
}

// appUserDataDir is a dedicated Edge/Chrome profile for BDDM app windows
// (not the tray popup). A fresh profile cannot inherit a maximized or
// huge size saved in the user's default browser profile.
func appUserDataDir() string {
	return filepath.Join(bddmConfigDir(), "chromium-app")
}

func mainWindowDefaultMarkerPath() string {
	return filepath.Join(bddmConfigDir(), mainWindowDefaultMarker)
}

func shouldForceMainWindowDefault() bool {
	_, err := os.Stat(mainWindowDefaultMarkerPath())
	return err != nil
}

func markMainWindowDefaultApplied() {
	dir := bddmConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	_ = os.WriteFile(mainWindowDefaultMarkerPath(), []byte("1.5.2\n"), 0644)
}

// appChromiumArgs launches a BDDM --app window in the dedicated profile.
// withPos adds --window-position (used when forcing the default placement).
func appChromiumArgs(url, userDataDir string, w, h, x, y int, withPos bool) []string {
	if w < 1 {
		w = mainWindowWidth
	}
	if h < 1 {
		h = mainWindowHeight
	}
	args := []string{
		"--user-data-dir=" + userDataDir,
		"--no-first-run",
		"--no-default-browser-check",
		fmt.Sprintf("--window-size=%d,%d", w, h),
	}
	if withPos {
		args = append(args, fmt.Sprintf("--window-position=%d,%d", x, y))
	}
	return append(args, "--app="+url)
}

// mainWindowCenteredRect places a 900x650 (or dipW x dipH) window in the
// centre of the work area. dipW/dipH are CSS pixels; dpi is 96 at 100%.
// The result is in physical pixels, clamped to the work area.
func mainWindowCenteredRect(workL, workT, workR, workB, dipW, dipH, dpi int) (x, y, pw, ph int) {
	if dpi < 96 {
		dpi = 96
	}
	if dipW < 1 {
		dipW = mainWindowWidth
	}
	if dipH < 1 {
		dipH = mainWindowHeight
	}
	pw, ph = dipToPhysical(dipW, dpi), dipToPhysical(dipH, dpi)
	if workR <= workL || workB <= workT {
		return 0, 0, pw, ph
	}
	margin := dipToPhysical(16, dpi)
	if margin < 8 {
		margin = 8
	}
	workW, workH := workR-workL, workB-workT
	if pw > workW-2*margin && workW > 2*margin {
		pw = workW - 2*margin
	}
	if ph > workH-2*margin && workH > 2*margin {
		ph = workH - 2*margin
	}
	x = workL + (workW-pw)/2
	y = workT + (workH-ph)/2
	if x < workL+margin {
		x = workL + margin
	}
	if y < workT+margin {
		y = workT + margin
	}
	if x+pw > workR-margin {
		x = workR - margin - pw
	}
	if y+ph > workB-margin {
		y = workB - margin - ph
	}
	if x < workL {
		x = workL
	}
	if y < workT {
		y = workT
	}
	return
}

// popupUserDataDir is a dedicated Edge/Chrome profile for compact popups
// (video, confirm, progress, complete, error, Drive, torrent). It is not
// the main-window profile and not the tray profile, so a large saved size
// cannot leak into these windows.
func popupUserDataDir() string {
	return filepath.Join(bddmConfigDir(), "chromium-popup")
}

func popupURLPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Path != "" {
		return u.Path
	}
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	return raw
}

func isPopupAppURL(u string) bool {
	p := strings.ToLower(popupURLPath(u))
	if p == "" {
		return false
	}
	for _, page := range []string{
		"/video.html", "/confirm.html", "/progress.html", "/complete.html",
		"/error.html", "/drive.html", "/torrent.html",
	} {
		if p == page || strings.HasSuffix(p, page) {
			return true
		}
	}
	return false
}

func isPopupWindowTitle(t string) bool {
	t = strings.TrimSpace(t)
	if t == "" {
		return false
	}
	for _, n := range []string{
		"Download Video",
		"Download File Info",
		"Download progress",
		"Download complete",
		"Download failed",
		"Download Google Drive folder",
		"Download Google file",
		"Add torrent",
	} {
		if t == n || strings.HasPrefix(t, n+" ") || strings.HasPrefix(t, n+" -") {
			return true
		}
	}
	return false
}

// popupFitRect places a popup whose content is contentDipW x contentDipH
// (CSS pixels) plus a measured window frame (physical pixels). The result
// is a physical-pixel rectangle, centred on and clamped to the work area.
func popupFitRect(workL, workT, workR, workB, contentDipW, contentDipH, frameW, frameH, dpi int) (x, y, pw, ph int) {
	if dpi < 96 {
		dpi = 96
	}
	if contentDipW < 200 {
		contentDipW = 200
	}
	if contentDipH < 120 {
		contentDipH = 120
	}
	if frameW < 0 {
		frameW = 0
	}
	if frameH < 0 {
		frameH = 0
	}
	pw = dipToPhysical(contentDipW, dpi) + frameW
	ph = dipToPhysical(contentDipH, dpi) + frameH
	if workR <= workL || workB <= workT {
		return 0, 0, pw, ph
	}
	margin := dipToPhysical(16, dpi)
	if margin < 8 {
		margin = 8
	}
	workW, workH := workR-workL, workB-workT
	if pw > workW-2*margin && workW > 2*margin {
		pw = workW - 2*margin
	}
	if ph > workH-2*margin && workH > 2*margin {
		ph = workH - 2*margin
	}
	x = workL + (workW-pw)/2
	y = workT + (workH-ph)/2
	if x < workL+margin {
		x = workL + margin
	}
	if y < workT+margin {
		y = workT + margin
	}
	if x+pw > workR-margin {
		x = workR - margin - pw
	}
	if y+ph > workB-margin {
		y = workB - margin - ph
	}
	if x < workL {
		x = workL
	}
	if y < workT {
		y = workT
	}
	return
}
