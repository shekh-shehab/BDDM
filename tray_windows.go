//go:build windows

package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Persistent notification-area icon for the long-lived BDDM.exe process
// (item 29). Left-click opens the compact tray popup; right-click shows
// the same actions in a native menu. Closing the main window does not
// remove the icon; Quit does.

const (
	nimAdd         = 0
	nimModify      = 1
	nimDelete      = 2
	nimSetVersion  = 4
	nifMessage     = 0x00000001
	nifIcon        = 0x00000002
	nifTip         = 0x00000004
	nifShowTip     = 0x00000080
	notifyIconVer4 = 4

	wmAppTray       = 0x8001 // WM_APP+1
	wmDestroy       = 0x0002
	wmContextMenu   = 0x007B
	ninSelect       = 0x0400 // WM_USER
	wmLButtonUp     = 0x0202
	wmRButtonUp     = 0x0205
	wmLButtonDblClk = 0x0203

	imageIcon      = 1
	lrLoadFromFile = 0x0010
	lrDefaultSize  = 0x0040

	mfString       = 0
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100
	tpmBottomAlign = 0x0020
	tpmRightAlign  = 0x0008

	trayCmdShow     = 1
	trayCmdAdd      = 2
	trayCmdPause    = 3
	trayCmdSettings = 4
	trayCmdQuit     = 5
)

var (
	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procExtractIconW     = shell32.NewProc("ExtractIconW")
	procLoadImageW       = user32.NewProc("LoadImageW")
	procDestroyIcon      = user32.NewProc("DestroyIcon")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procCreatePopupMenu  = user32.NewProc("CreatePopupMenu")
	procAppendMenuW      = user32.NewProc("AppendMenuW")
	procDestroyMenu      = user32.NewProc("DestroyMenu")
	procTrackPopupMenu   = user32.NewProc("TrackPopupMenu")
	procGetCursorPos     = user32.NewProc("GetCursorPos")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")

	trayMu      sync.Mutex
	trayHwnd    uintptr
	trayIcon    uintptr
	trayPort    string
	trayQuit    func()
	trayMgr     *Manager
	trayWndProc = syscall.NewCallback(trayWindowProc)
	trayClass   = mustUTF16("BDDMTrayHidden")
	trayTipStop chan struct{}
)

type notifyIconData struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         [16]byte
	hBalloonIcon     uintptr
}

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type winMsg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
}

type winPoint struct{ x, y int32 }

func mustUTF16(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		panic(err)
	}
	return p
}

func utf16Copy(dst []uint16, s string) {
	u, _ := syscall.UTF16FromString(s)
	n := len(u)
	if n > len(dst) {
		n = len(dst)
		u[n-1] = 0
	}
	copy(dst[:n], u[:n])
}

func loadTrayIcon() uintptr {
	exe, err := os.Executable()
	if err == nil {
		ico := filepath.Join(filepath.Dir(exe), "BDDM.ico")
		if _, st := os.Stat(ico); st == nil {
			p, _ := syscall.UTF16PtrFromString(ico)
			h, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(p)), imageIcon, 16, 16, lrLoadFromFile)
			if h != 0 {
				return h
			}
		}
		p, _ := syscall.UTF16PtrFromString(exe)
		h, _, _ := procExtractIconW.Call(0, uintptr(unsafe.Pointer(p)), 0)
		if h != 0 && h != ^uintptr(0) {
			return h
		}
	}
	return 0
}

func startTray(port string, quit func(), m *Manager) {
	trayMu.Lock()
	trayPort, trayQuit, trayMgr = port, quit, m
	trayMu.Unlock()
	go func() {
		runtime.LockOSThread()
		if err := runTrayLoop(); err != nil {
			log.Printf("tray icon: %v", err)
		}
	}()
	go trayTipLoop(m)
}

func trayTipLoop(m *Manager) {
	if m == nil {
		return
	}
	stop := make(chan struct{})
	trayMu.Lock()
	if trayTipStop != nil {
		close(trayTipStop)
	}
	trayTipStop = stop
	trayMu.Unlock()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	last := ""
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			tip := formatTrayTip(snapshotTray(m.List()))
			if tip != last {
				setTrayTip(tip)
				last = tip
			}
		}
	}
}

func stopTray() {
	trayMu.Lock()
	hwnd := trayHwnd
	stop := trayTipStop
	trayTipStop = nil
	trayMu.Unlock()
	if stop != nil {
		close(stop)
	}
	if hwnd != 0 {
		procPostMessageW.Call(hwnd, wmDestroy, 0, 0)
	}
}

func setTrayTip(text string) {
	trayMu.Lock()
	hwnd, icon := trayHwnd, trayIcon
	trayMu.Unlock()
	if hwnd == 0 {
		return
	}
	var nid notifyIconData
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	nid.hWnd = hwnd
	nid.uID = 1
	nid.uFlags = nifTip | nifShowTip
	nid.hIcon = icon
	utf16Copy(nid.szTip[:], text)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

func runTrayLoop() error {
	mod, _, _ := procGetModuleHandleW.Call(0)
	var wc wndClassEx
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	wc.lpfnWndProc = trayWndProc
	wc.hInstance = mod
	wc.lpszClassName = trayClass
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		// Already registered after a previous start in this process is fine.
		if errno, ok := err.(syscall.Errno); !ok || errno != 1410 { // ERROR_CLASS_ALREADY_EXISTS
			if r == 0 && err != syscall.Errno(0) && err.Error() != "The operation completed successfully." {
				// continue; CreateWindow will fail if the class is truly missing
			}
		}
	}
	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(trayClass)),
		uintptr(unsafe.Pointer(trayClass)),
		0, 0, 0, 0, 0,
		^uintptr(2), // HWND_MESSAGE
		0, mod, 0,
	)
	if hwnd == 0 {
		return err
	}
	icon := loadTrayIcon()
	trayMu.Lock()
	trayHwnd, trayIcon = hwnd, icon
	trayMu.Unlock()

	var nid notifyIconData
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	nid.hWnd = hwnd
	nid.uID = 1
	nid.uFlags = nifMessage | nifIcon | nifTip | nifShowTip
	nid.uCallbackMessage = wmAppTray
	nid.hIcon = icon
	utf16Copy(nid.szTip[:], "BDDM")
	if r, _, err := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid))); r == 0 {
		log.Printf("tray icon add: %v", err)
	} else {
		nid.uVersion = notifyIconVer4
		procShellNotifyIconW.Call(nimSetVersion, uintptr(unsafe.Pointer(&nid)))
	}

	var msg winMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}

	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	if icon != 0 {
		procDestroyIcon.Call(icon)
	}
	trayMu.Lock()
	trayHwnd, trayIcon = 0, 0
	trayMu.Unlock()
	return nil
}

func trayWindowProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmAppTray:
		switch uint32(lparam & 0xffff) {
		case wmRButtonUp, wmContextMenu:
			showTrayMenu(hwnd)
		case wmLButtonUp, ninSelect, wmLButtonDblClk:
			trayMu.Lock()
			port := trayPort
			trayMu.Unlock()
			if uint32(lparam&0xffff) == wmLButtonDblClk {
				_ = openAppWindow(trayShowURL(port))
			} else {
				openTrayPopup(port)
			}
		}
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

func showTrayMenu(hwnd uintptr) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	appendTrayItem := func(id uintptr, label string) {
		p, _ := syscall.UTF16PtrFromString(label)
		procAppendMenuW.Call(menu, mfString, id, uintptr(unsafe.Pointer(p)))
	}
	appendTrayItem(trayCmdShow, trayMenuShow)
	appendTrayItem(trayCmdAdd, trayMenuAddURL)
	appendTrayItem(trayCmdPause, trayMenuPauseAll)
	appendTrayItem(trayCmdSettings, trayMenuSettings)
	appendTrayItem(trayCmdQuit, trayMenuQuit)

	var pt winPoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmRightButton|tpmReturnCmd|tpmBottomAlign|tpmRightAlign, uintptr(uint32(pt.x)), uintptr(uint32(pt.y)), 0, hwnd, 0)
	procPostMessageW.Call(hwnd, 0, 0, 0) // WM_NULL: keeps the menu dismissable
	runTrayCommand(cmd)
}

func runTrayCommand(cmd uintptr) {
	trayMu.Lock()
	port, quit, mgr := trayPort, trayQuit, trayMgr
	trayMu.Unlock()
	switch cmd {
	case trayCmdShow:
		_ = openAppWindow(trayShowURL(port))
	case trayCmdAdd:
		_ = openAppWindow(trayAddURL(port))
	case trayCmdPause:
		if mgr != nil {
			mgr.PauseAll()
		}
	case trayCmdSettings:
		_ = openAppWindowSize(traySettingsURL(port), 900, 800)
	case trayCmdQuit:
		if quit != nil {
			go quit()
		}
	}
}
