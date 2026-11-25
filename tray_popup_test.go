package main

import (
	"strings"
	"testing"
)

func TestTrayPopupRect(t *testing.T) {
	x, y, w, h := trayPopupRect(1900, 1060, 0, 0, 1920, 1080, 260, 248, 96)
	if w != 260 || h != 248 {
		t.Fatalf("100%% size %dx%d, want 260x248", w, h)
	}
	if x < 8 || x+w > 1920-8 {
		t.Fatalf("100%% x=%d w=%d not inside the work area", x, w)
	}
	if y+h > 1060 {
		t.Fatalf("popup should sit above the tray cursor, y=%d h=%d cursor=1060", y, h)
	}
	if y < 8 {
		t.Fatalf("100%% y=%d went above the work area", y)
	}

	x, y, w, h = trayPopupRect(1900, 1060, 0, 0, 1920, 1080, 260, 248, 144)
	if w != 390 || h != 372 {
		t.Fatalf("150%% size %dx%d, want 390x372", w, h)
	}
	margin := 8 * 144 / 96
	if x < margin || x+w > 1920-margin {
		t.Fatalf("150%% x=%d w=%d margin=%d not inside the work area", x, w, margin)
	}
	if y+h > 1060 {
		t.Fatalf("150%% popup should sit above the cursor, y=%d h=%d", y, h)
	}

	_, _, w, h = trayPopupRect(200, 200, 0, 0, 1920, 1080, 260, 248, 192)
	if w != 520 || h != 496 {
		t.Fatalf("200%% size %dx%d, want 520x496", w, h)
	}

	x, y, w, h = trayPopupRect(5, 5, 0, 0, 1920, 1080, 260, 248, 96)
	if x < 8 || y < 8 {
		t.Fatalf("top-left clamp got %d,%d", x, y)
	}
	if x+w > 1920-8 || y+h > 1080-8 {
		t.Fatalf("top-left clamp overflow %d,%d %dx%d", x, y, w, h)
	}

	// No usable work area: still size the window, but do not clamp.
	x, y, w, h = trayPopupRect(100, 100, 0, 0, 0, 0, 260, 248, 96)
	if w != 260 || h != 248 {
		t.Fatalf("empty work area size %dx%d", w, h)
	}
	if y != 100-248-8 {
		t.Fatalf("empty work area should leave y unclamped, got %d", y)
	}

	_, _, w, h = trayPopupRect(10, 10, 0, 0, 800, 600, 0, 0, 50)
	if w != trayPopupWidth || h != trayPopupHeight {
		t.Fatalf("defaults at invalid dpi: %dx%d", w, h)
	}
}

func TestDipConvert(t *testing.T) {
	if got := dipToPhysical(260, 96); got != 260 {
		t.Fatalf("100%%: %d", got)
	}
	if got := dipToPhysical(260, 144); got != 390 {
		t.Fatalf("150%%: %d", got)
	}
	if got := physicalToDIP(390, 144); got != 260 {
		t.Fatalf("150%% back: %d", got)
	}
	if got := dipToPhysical(100, 0); got != 100 {
		t.Fatalf("dpi below 96 should be treated as 96, got %d", got)
	}
	if got := physicalToDIP(100, 80); got != 100 {
		t.Fatalf("physicalToDIP dpi below 96: %d", got)
	}
}

func TestTrayChromiumArgs(t *testing.T) {
	dir := `C:\Users\x\AppData\Roaming\BDDM\chromium-tray`
	url := "http://127.0.0.1:9614/tray.html"
	args := trayChromiumArgs(url, dir, 260, 248, 100, 200)
	joined := strings.Join(args, "\n")
	for _, want := range []string{
		"--user-data-dir=" + dir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-sync",
		"--window-size=260,248",
		"--window-position=100,200",
		"--app=" + url,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
	if strings.Contains(dir, "chromium-tray") == false {
		t.Fatal("test fixture path")
	}
	got := trayUserDataDir()
	if !strings.Contains(got, "edge-fallback") || !strings.Contains(got, "tray") || !strings.Contains(got, "BDDM") {
		t.Fatalf("trayUserDataDir = %q", got)
	}
	if strings.Contains(got, "chromium-tray") {
		t.Fatalf("tray profile must not be the browser's default profile: %q", got)
	}
}

func TestTrayPopupHTMLCompact(t *testing.T) {
	page := readUI(t, "tray.html")
	if strings.Contains(page, "height: 100%") || strings.Contains(page, "height:100%") {
		t.Fatal("tray popup must size to its content, not stretch to fill a large window")
	}
	if strings.Contains(page, "window.open") {
		t.Fatal("tray popup must not use window.open; that would open pages in the tray Chromium profile")
	}
	for _, want := range []string{
		"/api/open",
		`addEventListener("blur"`,
		"No active downloads",
		"width: 260px",
		trayMenuShow, trayMenuAddURL, trayMenuPauseAll, trayMenuSettings, trayMenuQuit,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("ui/tray.html missing %q", want)
		}
	}
}
