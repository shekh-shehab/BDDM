package main

import (
	"strings"
	"time"
)

// Known titles of BDDM's Edge/Chrome app windows (the <title> of each UI page,
// plus the titles some pages set later). Used to find those windows so a
// dialog can be owned by them and so they can be brought to the front.
var bddmWindowTitles = []string{
	"BDDM - Download Manager",
	"BDDM",
	"Add torrent",
	"Download File Info",
	"Download Google file",
	"Download Google Drive folder",
	"Download Video",
	"Download progress",
	"Download complete",
	"Download failed",
	"Download profile media",
}

// isBDDMUITitle reports whether title belongs to a BDDM app window.
func isBDDMUITitle(title string) bool {
	t := strings.TrimSpace(title)
	if t == "" {
		return false
	}
	if strings.Contains(t, "127.0.0.1:"+port) || strings.Contains(t, "localhost:"+port) {
		return true
	}
	if strings.HasPrefix(t, "BDDM") {
		return true
	}
	if strings.HasPrefix(t, "Download Video") {
		return true
	}
	if strings.HasSuffix(t, "Google Drive folder") || strings.Contains(t, " - Google Drive folder") {
		return true
	}
	for _, want := range bddmWindowTitles {
		if t == want || strings.HasPrefix(t, want+" ") || strings.HasPrefix(t, want+" -") {
			return true
		}
	}
	return false
}

// isMainAppURL is the main BDDM window (not a popup such as Add torrent).
func isMainAppURL(u string) bool {
	base := "http://127.0.0.1:" + port
	s := strings.TrimRight(strings.TrimSpace(u), "/")
	switch s {
	case base, base + "/index.html", "http://localhost:" + port, "http://localhost:" + port + "/index.html":
		return true
	}
	return false
}

// isBrowserAppExe reports whether exe is Edge or Chrome (the hosts of our UI).
func isBrowserAppExe(exe string) bool {
	name := exe
	if i := strings.LastIndexAny(exe, `/\`); i >= 0 {
		name = exe[i+1:]
	}
	switch strings.ToLower(name) {
	case "msedge.exe", "chrome.exe", "chromium.exe", "chromium-browser.exe":
		return true
	}
	return false
}

// isChromeWidgetClass is the top-level window class of an Edge/Chrome app window.
func isChromeWidgetClass(class string) bool {
	return strings.HasPrefix(class, "Chrome_WidgetWin_")
}

// isExplorerClass is a Windows Explorer folder window.
func isExplorerClass(class string) bool {
	return class == "CabinetWClass" || class == "ExploreWClass"
}

// dialogWindowClass is the standard class of a Windows folder or file picker
// (#32770). IFileOpenDialog and the PowerShell WinForms dialogs use it.
const dialogWindowClass = "#32770"

// fileDialogTitles are the titles BDDM sets on its folder and torrent pickers,
// plus the default title of the classic "Browse For Folder" dialog.
var fileDialogTitles = []string{
	"Choose the folder for downloads",
	"Add torrent",
	"Browse For Folder",
}

func isDialogClass(class string) bool {
	switch class {
	case dialogWindowClass, // #32770: classic / in-process IFileDialog
		"XamlExplorerHostIslandWindow",
		"WinUIDesktopWin32WindowClass",
		"Windows.UI.Core.CoreWindow",
		"Microsoft.UI.Content.DesktopChildSiteBridge",
		"ApplicationFrameWindow":
		return true
	}
	return false
}

func fileDialogTitleMatch(title string) bool {
	t := strings.TrimSpace(title)
	if t == "" {
		return false
	}
	for _, want := range fileDialogTitles {
		if t == want {
			return true
		}
	}
	return false
}

// isCandidateFileDialog reports whether a visible window is a folder or file
// picker we just opened, and not the BDDM Edge/Chrome window that should
// stay behind it. before lists HWNDs that already existed when Show started.
// dialogPID is the process that creates the dialog (BDDM.exe, or powershell
// for the fallback). expectedOwner is the BDDM app window, if any.
func isCandidateFileDialog(class, title string, hwnd, winOwner, expectedOwner uintptr, pid, dialogPID uint32, before map[uintptr]bool) bool {
	if hwnd == 0 || hwnd == expectedOwner {
		return false
	}
	if before[hwnd] {
		return false
	}
	if isChromeWidgetClass(class) {
		return false
	}
	byClass := isDialogClass(class)
	byTitle := fileDialogTitleMatch(title)
	if !byClass && !byTitle {
		return false
	}
	// Do not raise a random Explorer window. A Windows 11 folder picker
	// may be Explorer-hosted, but it uses the title we set.
	if isExplorerClass(class) && !byTitle {
		return false
	}
	if dialogPID != 0 && pid == dialogPID {
		return true
	}
	if expectedOwner != 0 && winOwner == expectedOwner {
		return true
	}
	// Known title: accept even when the picker is hosted in another
	// process and is not class #32770 (common on recent Windows).
	return byTitle
}

// shouldRaiseDialogOver is true when the folder/file picker should be
// brought in front of the current foreground window. The first time we
// see the picker we always raise it. After that we only raise it if a
// BDDM window (usually the one the user clicked Browse in) has come back
// on top — we do not fight the user if they switched to another app.
func shouldRaiseDialogOver(fg, dialog, owner uintptr, fgIsBDDM, alreadyRaised bool) bool {
	if dialog == 0 || fg == dialog {
		return false
	}
	if !alreadyRaised {
		return true
	}
	if owner != 0 && fg == owner {
		return true
	}
	return fgIsBDDM
}

// keepAppWindowFrontFor is how long we keep a newly opened BDDM window
// in front after it first appears, in case the previous window takes
// focus back (the same method used for the Add torrent window).
const keepAppWindowFrontFor = 2 * time.Second
