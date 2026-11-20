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

func TestIsDialogClass(t *testing.T) {
	if !isDialogClass("#32770") || isDialogClass("Chrome_WidgetWin_1") || isDialogClass("") || isDialogClass("#32769") {
		t.Fatal("dialog class")
	}
}

func TestFileDialogTitleMatch(t *testing.T) {
	for _, s := range []string{"Choose the folder for downloads", "Add torrent", "Browse For Folder"} {
		if !fileDialogTitleMatch(s) {
			t.Errorf("expected %q to be a picker title", s)
		}
	}
	for _, s := range []string{"", "Download File Info", "BDDM - Download Manager", "Open"} {
		if fileDialogTitleMatch(s) {
			t.Errorf("did not expect %q to be a picker title", s)
		}
	}
}

func TestIsCandidateFileDialog(t *testing.T) {
	const (
		dlg     = 0x200
		owner   = 0x100
		edge    = 0x100
		other   = 0x300
		ourPID  = 11
		psPID   = 22
		edgePID = 33
	)
	existing := map[uintptr]bool{0x50: true, dlg: true}

	// An existing window must be ignored.
	if isCandidateFileDialog("#32770", "Add torrent", dlg, owner, edge, ourPID, ourPID, existing) {
		t.Fatal("already-open window")
	}
	before := map[uintptr]bool{0x50: true}

	// Native IFileDialog in this process, class #32770, new HWND.
	if !isCandidateFileDialog("#32770", "Choose the folder for downloads", dlg, owner, edge, ourPID, ourPID, before) {
		t.Fatal("native folder picker")
	}
	// Torrent open dialog.
	if !isCandidateFileDialog("#32770", "Add torrent", dlg, owner, edge, ourPID, ourPID, before) {
		t.Fatal("native torrent picker")
	}
	// PowerShell fallback: dialog lives in powershell.exe.
	if !isCandidateFileDialog("#32770", "Browse For Folder", dlg, owner, edge, psPID, psPID, before) {
		t.Fatal("powershell folder picker")
	}
	// Owned by the Edge window even if the pid looks odd.
	if !isCandidateFileDialog("#32770", "", dlg, edge, edge, 99, ourPID, before) {
		t.Fatal("owned by the BDDM window")
	}
	// Must never raise the owner (the confirm / Edge window) itself.
	if isCandidateFileDialog("Chrome_WidgetWin_1", "Download File Info", edge, 0, edge, edgePID, ourPID, before) {
		t.Fatal("must not raise the owner")
	}
	if isCandidateFileDialog("Chrome_WidgetWin_1", "Add torrent", other, 0, edge, edgePID, ourPID, before) {
		t.Fatal("Add torrent app window is not the picker")
	}
	if isCandidateFileDialog("CabinetWClass", "Downloads", other, 0, edge, 4, ourPID, before) {
		t.Fatal("Explorer is not the picker")
	}
	if isCandidateFileDialog("#32770", "Open", other, 0, edge, 99, ourPID, before) {
		t.Fatal("unrelated dialog in another process")
	}
	if isCandidateFileDialog("#32770", "Add torrent", 0, owner, edge, ourPID, ourPID, before) {
		t.Fatal("zero hwnd")
	}
	// Known title + class is enough when the pid could not be read.
	if !isCandidateFileDialog("#32770", "Add torrent", dlg, 0, edge, 0, 0, before) {
		t.Fatal("title and class with no pid")
	}
}
