package main

import (
	"fmt"
	"os"
	"path/filepath"
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
