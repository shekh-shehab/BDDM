package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMainWindowDefaultSize(t *testing.T) {
	if mainWindowWidth != 900 || mainWindowHeight != 650 {
		t.Fatalf("default main window size %dx%d, want 900x650", mainWindowWidth, mainWindowHeight)
	}
}

func TestMainWindowCenteredRect(t *testing.T) {
	x, y, w, h := mainWindowCenteredRect(0, 0, 1920, 1040, 900, 650, 96)
	if w != 900 || h != 650 {
		t.Fatalf("100%% size %dx%d, want 900x650", w, h)
	}
	if x != (1920-900)/2 || y != (1040-650)/2 {
		t.Fatalf("100%% origin %d,%d, want centred 510,195", x, y)
	}

	x, y, w, h = mainWindowCenteredRect(0, 0, 1920, 1080, 900, 650, 144)
	if w != 1350 || h != 975 {
		t.Fatalf("150%% size %dx%d, want 1350x975", w, h)
	}
	if x < 0 || y < 0 || x+w > 1920 || y+h > 1080 {
		t.Fatalf("150%% %d,%d %dx%d outside the work area", x, y, w, h)
	}

	// A small laptop work area must shrink the window rather than overflow.
	x, y, w, h = mainWindowCenteredRect(0, 0, 800, 500, 900, 650, 96)
	if w >= 800 || h >= 500 {
		t.Fatalf("small work area should shrink the window, got %dx%d", w, h)
	}
	if x < 0 || y < 0 || x+w > 800 || y+h > 500 {
		t.Fatalf("shrunk window %d,%d %dx%d overflowed 800x500", x, y, w, h)
	}

	_, _, w, h = mainWindowCenteredRect(0, 0, 0, 0, 0, 0, 50)
	if w != 900 || h != 650 {
		t.Fatalf("defaults at invalid dpi / empty work area: %dx%d", w, h)
	}
}

func TestAppChromiumArgs(t *testing.T) {
	dir := `C:\Users\x\AppData\Roaming\BDDM\chromium-app`
	url := "http://127.0.0.1:9614/"
	args := appChromiumArgs(url, dir, 900, 650, 100, 80, true)
	joined := strings.Join(args, "\n")
	for _, want := range []string{
		"--user-data-dir=" + dir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-sync",
		"--window-size=900,650",
		"--window-position=100,80",
		"--app=" + url,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
	noPos := strings.Join(appChromiumArgs(url, dir, 900, 650, 0, 0, false), "\n")
	if strings.Contains(noPos, "--window-position=") {
		t.Fatalf("position should be omitted when withPos is false: %s", noPos)
	}
	got := appUserDataDir()
	if !strings.Contains(got, "edge-fallback") || !strings.Contains(got, "BDDM") || strings.Contains(got, "chromium-app") {
		t.Fatalf("appUserDataDir = %q", got)
	}
	if got == trayUserDataDir() {
		t.Fatalf("main window profile must not be the tray profile: %q", got)
	}
}

func TestForceMainWindowDefaultOnce(t *testing.T) {
	dir := t.TempDir()
	prev := bddmConfigDirOverride
	bddmConfigDirOverride = dir
	defer func() { bddmConfigDirOverride = prev }()

	if !shouldForceMainWindowDefault() {
		t.Fatal("expected to force the default size before the marker exists")
	}
	markMainWindowDefaultApplied()
	if shouldForceMainWindowDefault() {
		t.Fatal("should not force the default size again after it has been applied")
	}
	raw, err := os.ReadFile(filepath.Join(dir, mainWindowDefaultMarker))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "1.5.2") {
		t.Fatalf("marker contents: %q", raw)
	}
}

func TestIsPopupAppURL(t *testing.T) {
	base := "http://127.0.0.1:9614"
	for _, u := range []string{
		base + "/video.html?id=1",
		base + "/confirm.html?id=1",
		base + "/progress.html?id=1",
		base + "/complete.html?id=1",
		base + "/error.html?id=1",
		base + "/drive.html?id=1",
		base + "/torrent.html?id=1",
		"/video.html",
	} {
		if !isPopupAppURL(u) {
			t.Errorf("expected popup URL %q", u)
		}
	}
	for _, u := range []string{
		base, base + "/", base + "/index.html", base + "/?add=1", base + "/?tab=settings",
		base + "/tray.html", "http://127.0.0.1:9614/tray.html",
	} {
		if isPopupAppURL(u) {
			t.Errorf("did not expect popup URL %q", u)
		}
	}
}

func TestIsPopupWindowTitle(t *testing.T) {
	for _, s := range []string{"Download Video", "Download Video - Never Gonna Give You Up", "Download File Info", "Download progress", "Download complete", "Download failed", "Add torrent", "Download Google Drive folder"} {
		if !isPopupWindowTitle(s) {
			t.Errorf("expected popup title %q", s)
		}
	}
	for _, s := range []string{"", "BDDM - Download Manager", "BDDM tray", "Settings", "Microsoft Edge"} {
		if isPopupWindowTitle(s) {
			t.Errorf("did not expect popup title %q", s)
		}
	}
}

func TestPopupUserDataDir(t *testing.T) {
	got := popupUserDataDir()
	if !strings.Contains(got, "edge-fallback") || !strings.Contains(got, "popup") || !strings.Contains(got, "BDDM") {
		t.Fatalf("popupUserDataDir = %q", got)
	}
	if got == appUserDataDir() || got == trayUserDataDir() {
		t.Fatalf("popup profile must be separate from main and tray: %q", got)
	}
}

func TestPopupFitRect(t *testing.T) {
	// 720x520 content, 16x39 frame, 100% DPI, 1920x1040 work area.
	x, y, w, h := popupFitRect(0, 0, 1920, 1040, 720, 520, 16, 39, 96)
	if w != 720+16 || h != 520+39 {
		t.Fatalf("100%% size %dx%d, want 736x559", w, h)
	}
	if x != (1920-w)/2 || y != (1040-h)/2 {
		t.Fatalf("100%% origin %d,%d, not centred for %dx%d", x, y, w, h)
	}

	// 150% DPI: content 720dip = 1080 physical + 16 frame.
	x, y, w, h = popupFitRect(0, 0, 1920, 1080, 720, 520, 24, 58, 144)
	if w != 1080+24 || h != 780+58 {
		t.Fatalf("150%% size %dx%d, want 1104x838", w, h)
	}
	if x < 0 || y < 0 || x+w > 1920 || y+h > 1080 {
		t.Fatalf("150%% %d,%d %dx%d outside the work area", x, y, w, h)
	}

	// Taller than the work area is capped so the page can scroll.
	_, _, w, h = popupFitRect(0, 0, 800, 500, 720, 900, 16, 39, 96)
	if h >= 500 {
		t.Fatalf("capped height should be inside 500px work area, got %d", h)
	}
	if w >= 800 {
		t.Fatalf("capped width should be inside 800px work area, got %d", w)
	}
}
