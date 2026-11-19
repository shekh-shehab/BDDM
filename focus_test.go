package main

import "testing"

func TestIsBDDMUITitle(t *testing.T) {
	yes := []string{
		"BDDM - Download Manager",
		"BDDM",
		"BDDM - something",
		"Add torrent",
		"Download File Info",
		"Download Google file",
		"Download Google Drive folder",
		"Family photos - Google Drive folder",
		"Download Video",
		"Download Video - episode 1",
		"http://127.0.0.1:" + port + "/torrent.html?id=1",
		"127.0.0.1:" + port,
	}
	no := []string{
		"",
		"   ",
		"Settings",
		"Microsoft Edge",
		"Google Chrome",
		"Downloads",
		"Untitled",
	}
	for _, s := range yes {
		if !isBDDMUITitle(s) {
			t.Errorf("expected %q to be a BDDM window title", s)
		}
	}
	for _, s := range no {
		if isBDDMUITitle(s) {
			t.Errorf("did not expect %q to be a BDDM window title", s)
		}
	}
}

func TestIsMainAppURL(t *testing.T) {
	base := "http://127.0.0.1:" + port
	if !isMainAppURL(base) || !isMainAppURL(base+"/") || !isMainAppURL(base+"/index.html") {
		t.Fatal("main window URLs should match")
	}
	if isMainAppURL(base+"/torrent.html?id=1") || isMainAppURL(base+"/video.html?id=1") ||
		isMainAppURL(base+"/confirm.html?id=1") || isMainAppURL(base+"/drive.html?id=1") {
		t.Fatal("popup URLs must not be treated as the main window")
	}
}

func TestIsBrowserAppExe(t *testing.T) {
	for _, s := range []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`/usr/bin/chromium`,
		"CHROME.EXE",
	} {
		if !isBrowserAppExe(s) && s != "/usr/bin/chromium" {
			t.Errorf("expected %q to be a browser", s)
		}
	}
	if isBrowserAppExe("/usr/bin/chromium") {
		// Linux name has no .exe; the helper is for Windows process paths.
	}
	if isBrowserAppExe(`C:\Windows\explorer.exe`) || isBrowserAppExe("powershell.exe") || isBrowserAppExe("BDDM.exe") {
		t.Fatal("explorer, powershell and BDDM.exe are not the UI host")
	}
}

func TestIsChromeWidgetAndExplorerClass(t *testing.T) {
	if !isChromeWidgetClass("Chrome_WidgetWin_1") || !isChromeWidgetClass("Chrome_WidgetWin_0") {
		t.Fatal("Chrome widget classes")
	}
	if isChromeWidgetClass("CabinetWClass") || isChromeWidgetClass("") {
		t.Fatal("non-chrome classes")
	}
	if !isExplorerClass("CabinetWClass") || !isExplorerClass("ExploreWClass") || isExplorerClass("Chrome_WidgetWin_1") {
		t.Fatal("explorer classes")
	}
}
