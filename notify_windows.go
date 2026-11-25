//go:build windows

package main

// notify_windows.go: Windows helpers — toast notifications, opening Explorer,
// the folder picker and the app window. Everything here runs in the
// background and never blocks downloads if it fails.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

const createNoWindow = 0x08000000

// hideConsole stops a black console window from flashing when we start
// PowerShell or cmd from the windowless BDDM.exe.
func hideConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}

// runPowerShell runs a script with Windows PowerShell 5.1 (built into Windows).
func runPowerShell(script string, timeout time.Duration) (string, error) {
	return runPowerShellStart(script, timeout, false, 0)
}

func runPowerShellDialog(script string, timeout time.Duration, owner uintptr) (string, error) {
	return runPowerShellStart(script, timeout, true, owner)
}

func runPowerShellStart(script string, timeout time.Duration, foreground bool, watchOwner uintptr) (string, error) {
	// -EncodedCommand takes UTF-16LE base64, so we never fight with quoting.
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, v := range u {
		b[2*i] = byte(v)
		b[2*i+1] = byte(v >> 8)
	}
	enc := base64.StdEncoding.EncodeToString(b)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-STA", "-ExecutionPolicy", "Bypass", "-EncodedCommand", enc)
	hideConsole(cmd)
	var out strings.Builder
	cmd.Stdout = &out
	var err error
	var before map[uintptr]bool
	stopWatch := func() {}
	if foreground {
		if watchOwner != 0 {
			clearTopmost(watchOwner)
		}
		before = snapshotPickerHWND()
		withForeground(func() { err = cmd.Start() })
		if cmd.Process != nil {
			allowSetForegroundWindow(uint32(cmd.Process.Pid))
			stopWatch = watchFileDialog(before, watchOwner, uint32(cmd.Process.Pid))
		}
	} else {
		err = cmd.Start()
	}
	defer stopWatch()
	if err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return out.String(), err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return out.String(), errors.New("powershell timed out")
	}
}

// notify shows a Windows toast under BDDM's own name and icon. If toasts
// are not available it falls back to a tray balloon. Errors are only logged.
func notify(title, body string) {
	logo, ico := toastIconPaths()
	script := windowsNotifyScript(title, body, logo, ico)
	if _, err := runPowerShell(script, 20*time.Second); err != nil {
		log.Printf("notification failed: %v", err)
	}
}

// runSilentSetup starts the downloaded BDDM-Setup.exe as a silent upgrade
// that puts BDDM back after it finishes.
func runSilentSetup(path string) error {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".exe") {
		return fmt.Errorf("the update file is not a setup program")
	}
	cmd := exec.Command(path, "/S", "/run")
	hideConsole(cmd)
	var err error
	withForeground(func() { err = cmd.Start() })
	if err == nil && cmd.Process != nil {
		allowSetForegroundWindow(uint32(cmd.Process.Pid))
	}
	return err
}

// openURL opens an http(s) address in the default browser.
func openURL(raw string) error {
	if err := validWebURL(raw); err != nil {
		return err
	}
	before := snapshotHWND()
	var err error
	withForeground(func() {
		cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", raw)
		err = cmd.Start()
	})
	if err != nil {
		return err
	}
	go activateNewWindows(before, isNewBrowserWindow, 3*time.Second)
	return nil
}

// openFolder opens Explorer at dir, selecting file when given.
func openFolder(dir, file string) error {
	var cmd *exec.Cmd
	if file != "" {
		cmd = exec.Command("explorer.exe")
		// Explorer needs exactly: /select,"C:\path\file" (Go's normal quoting breaks it).
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + file + `"`}
	} else {
		cmd = exec.Command("explorer.exe", dir)
	}
	before := snapshotHWND()
	var err error
	withForeground(func() {
		err = cmd.Start() // explorer often returns exit code 1, so don't wait
	})
	if err != nil {
		return err
	}
	go activateNewWindows(before, func(w enumWnd) bool { return isExplorerClass(w.class) }, 3*time.Second)
	return nil
}

// openFile opens a file with the default app.
func openFile(path string) error {
	if path == "" {
		return errors.New("file not found")
	}
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", path).Start()
}

// pickFolder shows the Windows folder dialog, owned by the BDDM window so
// it appears in front. Returns "" if cancelled.
func pickFolder(start string) (string, error) {
	owner := dialogOwnerHWND()
	path, err := pickFolderNative(owner, start)
	if err == nil {
		return path, nil
	}
	log.Printf("native folder picker failed (%v); trying the fallback dialog", err)
	return pickFolderPowerShell(owner, start)
}

// pickTorrentFile shows the Windows "Open" dialog for a .torrent file,
// owned by the BDDM window so it appears in front. Returns "" if cancelled.
func pickTorrentFile() (string, error) {
	owner := dialogOwnerHWND()
	path, err := pickTorrentFileNative(owner, "")
	if err == nil {
		return path, nil
	}
	log.Printf("native file picker failed (%v); trying the fallback dialog", err)
	return pickTorrentFilePowerShell(owner)
}

func powershellDialogOwner(hwnd uintptr) string {
	// A hidden TopMost form is not enough: the dialog is then owned by that
	// form, not by the Edge window, so it stays behind the BDDM UI. When we
	// have the real HWND we wrap it as IWin32Window.
	if hwnd == 0 {
		return `
$owner = New-Object System.Windows.Forms.Form -Property @{TopMost = $true; ShowInTaskbar = $false; Visible = $false}
$owner.Show(); $owner.Hide()
[WinFocus]::AllowSetForegroundWindow(-1)
`
	}
	return `
$hwnd = [IntPtr]` + fmt.Sprintf("%d", hwnd) + `
$owner = New-Object HwndOwner ($hwnd.ToInt64())
[WinFocus]::AllowSetForegroundWindow(-1)
`
}

const powershellDialogPrelude = `
Add-Type -AssemblyName System.Windows.Forms
Add-Type -TypeDefinition @"
using System;
using System.Windows.Forms;
using System.Runtime.InteropServices;
public class HwndOwner : IWin32Window {
  public HwndOwner(long h) { Handle = (IntPtr)h; }
  public IntPtr Handle { get; }
}
public class WinFocus {
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
  [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr h, int n);
  [DllImport("user32.dll")] public static extern bool SetWindowPos(IntPtr h, IntPtr i, int x, int y, int cx, int cy, uint f);
  [DllImport("user32.dll")] public static extern bool AllowSetForegroundWindow(int pid);
  [DllImport("user32.dll")] public static extern bool BringWindowToTop(IntPtr h);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern IntPtr FindWindow(string c, string t);
}
"@
function Raise-BddmDialog {
  foreach ($title in @('Choose the folder for downloads','Add torrent','Browse For Folder')) {
    $h = [WinFocus]::FindWindow('#32770', $title)
    if ($h -eq [IntPtr]::Zero) { $h = [WinFocus]::FindWindow($null, $title) }
    if ($h -ne [IntPtr]::Zero) {
      [WinFocus]::AllowSetForegroundWindow(-1)
      [void][WinFocus]::SetWindowPos($h, [IntPtr](-1), 0,0,0,0, 0x0003)
      [void][WinFocus]::BringWindowToTop($h)
      [void][WinFocus]::SetForegroundWindow($h)
      return $h
    }
  }
  return [IntPtr]::Zero
}
function Clear-BddmDialogTopMost {
  foreach ($title in @('Choose the folder for downloads','Add torrent','Browse For Folder')) {
    $h = [WinFocus]::FindWindow('#32770', $title)
    if ($h -eq [IntPtr]::Zero) { $h = [WinFocus]::FindWindow($null, $title) }
    if ($h -ne [IntPtr]::Zero) {
      [void][WinFocus]::SetWindowPos($h, [IntPtr](-2), 0,0,0,0, 0x0003)
    }
  }
}
$raiseTimer = New-Object System.Windows.Forms.Timer
$raiseTimer.Interval = 50
$raiseTimer.Add_Tick({
  try { Raise-BddmDialog } catch {}
})
$raiseTimer.Start()
`

func pickFolderPowerShell(hwnd uintptr, start string) (string, error) {
	script := powershellDialogPrelude + powershellDialogOwner(hwnd) + `
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.Description = 'Choose the folder for downloads'
$d.SelectedPath = ` + psQuote(start) + `
$d.ShowNewFolderButton = $true
try {
  if ($d.ShowDialog($owner) -eq [System.Windows.Forms.DialogResult]::OK) {
    [Console]::OutputEncoding = [System.Text.Encoding]::UTF8
    Write-Output $d.SelectedPath
  }
} finally { $raiseTimer.Stop(); $raiseTimer.Dispose(); Clear-BddmDialogTopMost }
`
	out, err := runPowerShellDialog(script, 10*time.Minute, hwnd)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func pickTorrentFilePowerShell(hwnd uintptr) (string, error) {
	script := powershellDialogPrelude + powershellDialogOwner(hwnd) + `
$d = New-Object System.Windows.Forms.OpenFileDialog
$d.Title = 'Add torrent'
$d.Filter = 'Torrent files (*.torrent)|*.torrent|All files (*.*)|*.*'
$d.Multiselect = $false
try {
  if ($d.ShowDialog($owner) -eq [System.Windows.Forms.DialogResult]::OK) {
    [Console]::OutputEncoding = [System.Text.Encoding]::UTF8
    Write-Output $d.FileName
  }
} finally { $raiseTimer.Stop(); $raiseTimer.Dispose(); Clear-BddmDialogTopMost }
`
	out, err := runPowerShellDialog(script, 10*time.Minute, hwnd)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// openAppWindow opens the UI as an app window: Edge, then Chrome, then the
// default browser.
func openAppWindow(u string) error { return openAppWindowSize(u, mainWindowWidth, mainWindowHeight) }

// openAppWindowSize opens u in an Edge (or Chrome) app window of w x h pixels
// and brings that window to the front. If the main window is already open,
// it is reused and brought to the front instead of starting a second one.
// Main-window launches use a dedicated Chromium profile so a size saved in
// the user's default Edge/Chrome profile cannot restore a maximized window.
// The first time after this default is introduced, the window is forced to
// 900x650 DIP, centred on the work area; later opens remember a size the
// user chose themselves.
func openAppWindowSize(u string, w, h int) error {
	force := isMainAppURL(u) && shouldForceMainWindowDefault()
	if isMainAppURL(u) {
		if hwnd := existingMainWindowHWND(); hwnd != 0 {
			if force {
				applyDefaultMainWindowPlacement(hwnd)
			}
			activateWindow(hwnd)
			return nil
		}
	}
	if isPopupAppURL(u) {
		return openPopupAppWindow(u, w, h)
	}
	dir := appUserDataDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("app window: user-data-dir: %v", err)
	}
	var posX, posY, physW, physH int
	withPos := false
	if force {
		posX, posY, physW, physH, withPos = defaultMainWindowPlacement(w, h)
	}
	dipX, dipY := physicalToDIP(posX, primaryDPI()), physicalToDIP(posY, primaryDPI())
	args := appChromiumArgs(u, dir, w, h, dipX, dipY, withPos)
	candidates, edgeCount := trayBrowserCandidates()
	before := snapshotHWND()
	start := func(cmd *exec.Cmd) error {
		var err error
		withForeground(func() { err = cmd.Start() })
		return err
	}
	for i, p := range candidates {
		if i == edgeCount {
			// Edge not found in the usual folders: let Windows find it.
			cmd := exec.Command("cmd", append([]string{"/c", "start", "", "msedge"}, args...)...)
			hideConsole(cmd)
			if start(cmd) == nil {
				go afterAppWindowLaunch(u, before, 0, force, posX, posY, physW, physH)
				return nil
			}
		}
		if fileExists(p) {
			cmd := exec.Command(p, args...)
			if start(cmd) == nil {
				pid := 0
				if cmd.Process != nil {
					pid = cmd.Process.Pid
				}
				go afterAppWindowLaunch(u, before, pid, force, posX, posY, physW, physH)
				return nil
			}
		}
	}
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	if err := start(cmd); err != nil {
		return err
	}
	go afterAppWindowLaunch(u, before, 0, force, posX, posY, physW, physH)
	return nil
}

func afterAppWindowLaunch(u string, before map[uintptr]bool, startedPID int, force bool, x, y, pw, ph int) {
	if force {
		placeNewMainWindow(before, x, y, pw, ph)
		scheduleTagBDDMAppWindows()
		return
	}
	focusOpenedWindow(u, before, startedPID)
}

func primaryMonitor() uintptr {
	mon, _, _ := procMonitorFromPoint.Call(0, 1) // MONITOR_DEFAULTTOPRIMARY
	return mon
}

func primaryWorkArea() winRect {
	mon := primaryMonitor()
	if mon == 0 {
		return winRect{}
	}
	var info monitorInfo
	info.cbSize = uint32(unsafe.Sizeof(info))
	r, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return winRect{}
	}
	return info.work
}

func primaryDPI() int {
	mon := primaryMonitor()
	if mon != 0 && procGetDpiForMonitor.Find() == nil {
		var dx, dy uint32
		r, _, _ := procGetDpiForMonitor.Call(mon, mdtEffectiveDPI, uintptr(unsafe.Pointer(&dx)), uintptr(unsafe.Pointer(&dy)))
		if r == 0 && dx >= 96 {
			return int(dx)
		}
	}
	if procGetDpiForSystem.Find() == nil {
		r, _, _ := procGetDpiForSystem.Call()
		if r >= 96 {
			return int(r)
		}
	}
	return 96
}

func defaultMainWindowPlacement(dipW, dipH int) (x, y, pw, ph int, ok bool) {
	work := primaryWorkArea()
	dpi := primaryDPI()
	x, y, pw, ph = mainWindowCenteredRect(int(work.left), int(work.top), int(work.right), int(work.bottom), dipW, dipH, dpi)
	return x, y, pw, ph, true
}

func applyDefaultMainWindowPlacement(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	x, y, pw, ph, _ := defaultMainWindowPlacement(mainWindowWidth, mainWindowHeight)
	showWindow(hwnd, swRestore)
	setWindowPosRect(hwnd, x, y, pw, ph)
	markMainWindowDefaultApplied()
}

func placeNewMainWindow(before map[uintptr]bool, x, y, pw, ph int) {
	deadline := time.Now().Add(3 * time.Second)
	var hwnd uintptr
	for hwnd == 0 && time.Now().Before(deadline) {
		for _, w := range newWindowsSince(before) {
			if isMainAppWindowTitle(w.title) {
				hwnd = w.hwnd
				break
			}
		}
		if hwnd == 0 {
			hwnd = existingMainWindowHWND()
		}
		if hwnd == 0 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if hwnd == 0 {
		return
	}
	until := time.Now().Add(2 * time.Second)
	for {
		showWindow(hwnd, swRestore)
		setWindowPosRect(hwnd, x, y, pw, ph)
		activateWindow(hwnd)
		if time.Now().After(until) || !isWindow(hwnd) {
			markMainWindowDefaultApplied()
			return
		}
		time.Sleep(80 * time.Millisecond)
	}
}

func openPopupAppWindow(u string, w, h int) error {
	dir := popupUserDataDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("popup window: user-data-dir: %v", err)
	}
	posX, posY, physW, physH, _ := defaultMainWindowPlacement(w, h)
	dpi := primaryDPI()
	args := appChromiumArgs(u, dir, w, h, physicalToDIP(posX, dpi), physicalToDIP(posY, dpi), true)
	candidates, edgeCount := trayBrowserCandidates()
	before := snapshotHWND()
	start := func(cmd *exec.Cmd) error {
		var err error
		withForeground(func() { err = cmd.Start() })
		return err
	}
	var startedPID int
	launched := false
	for i, p := range candidates {
		if i == edgeCount {
			cmd := exec.Command("cmd", append([]string{"/c", "start", "", "msedge"}, args...)...)
			hideConsole(cmd)
			if start(cmd) == nil {
				launched = true
				break
			}
		}
		if fileExists(p) {
			cmd := exec.Command(p, args...)
			if start(cmd) == nil {
				launched = true
				if cmd.Process != nil {
					startedPID = cmd.Process.Pid
				}
				break
			}
		}
	}
	if !launched {
		cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
		if err := start(cmd); err != nil {
			return err
		}
	}
	go afterPopupWindowLaunch(u, before, startedPID, posX, posY, physW, physH)
	return nil
}

func afterPopupWindowLaunch(u string, before map[uintptr]bool, startedPID int, x, y, pw, ph int) {
	if startedPID > 0 {
		allowSetForegroundWindow(uint32(startedPID))
	}
	placeNewPopupWindow(before, x, y, pw, ph)
	scheduleTagBDDMAppWindows()
}

func placeNewPopupWindow(before map[uintptr]bool, x, y, pw, ph int) {
	deadline := time.Now().Add(2 * time.Second)
	var hwnd uintptr
	for hwnd == 0 && time.Now().Before(deadline) {
		for _, w := range newWindowsSince(before) {
			if isPopupWindowTitle(w.title) || (isChromeWidgetClass(w.class) && isBDDMUITitle(w.title) && !isMainAppWindowTitle(w.title) && !strings.HasPrefix(w.title, "BDDM tray")) {
				hwnd = w.hwnd
				break
			}
		}
		if hwnd == 0 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if hwnd == 0 {
		return
	}
	// Brief initial placement so a restored large size cannot stick.
	// The page then reports its content size to /api/window/fit.
	until := time.Now().Add(800 * time.Millisecond)
	for {
		showWindow(hwnd, swRestore)
		setWindowPosRect(hwnd, x, y, pw, ph)
		activateWindow(hwnd)
		if time.Now().After(until) || !isWindow(hwnd) {
			return
		}
		time.Sleep(80 * time.Millisecond)
	}
}
