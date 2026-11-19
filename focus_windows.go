//go:build windows

package main

// focus_windows.go: put BDDM's windows and native dialogs in front with
// focus. BDDM itself has no window (it is a background process); the UI is
// an Edge or Chrome app window, and dialogs are opened from a server
// goroutine. Windows then treats the new window as a background window and
// leaves it behind. activateWindow is the one helper used for every window
// or dialog we open.

import (
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	swRestore     = 9
	swShow        = 5
	hwndTopmost   = ^uintptr(0) // HWND_TOPMOST
	hwndNotopmost = ^uintptr(1) // HWND_NOTOPMOST
	swpNosize     = 0x0001
	swpNomove     = 0x0002
	gwOwner       = 4
	gaRoot        = 2
	asfwAny       = ^uint32(0) // ASFW_ANY

	processQueryLimitedInformation = 0x1000
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procEnumWindows                = user32.NewProc("EnumWindows")
	procGetWindowTextW             = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW       = user32.NewProc("GetWindowTextLengthW")
	procGetClassNameW              = user32.NewProc("GetClassNameW")
	procGetWindowThreadProcessId   = user32.NewProc("GetWindowThreadProcessId")
	procIsWindow                   = user32.NewProc("IsWindow")
	procIsWindowVisible            = user32.NewProc("IsWindowVisible")
	procIsIconic                   = user32.NewProc("IsIconic")
	procShowWindow                 = user32.NewProc("ShowWindow")
	procSetForegroundWindow        = user32.NewProc("SetForegroundWindow")
	procGetForegroundWindow        = user32.NewProc("GetForegroundWindow")
	procBringWindowToTop           = user32.NewProc("BringWindowToTop")
	procSetWindowPos               = user32.NewProc("SetWindowPos")
	procAllowSetForegroundWindow   = user32.NewProc("AllowSetForegroundWindow")
	procAttachThreadInput          = user32.NewProc("AttachThreadInput")
	procGetWindow                  = user32.NewProc("GetWindow")
	procGetAncestor                = user32.NewProc("GetAncestor")
	procGetWindowRect              = user32.NewProc("GetWindowRect")
	procGetCurrentThreadId         = kernel32.NewProc("GetCurrentThreadId")
	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procCloseHandle                = kernel32.NewProc("CloseHandle")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
)

type winRect struct{ left, top, right, bottom int32 }

type enumWnd struct {
	hwnd  uintptr
	title string
	class string
	pid   uint32
	tid   uint32
}

var enumMu sync.Mutex
var enumBuf []enumWnd
var enumCallback = syscall.NewCallback(func(hwnd, lparam uintptr) uintptr {
	if !isWindowVisible(hwnd) {
		return 1
	}
	if getWindow(hwnd, gwOwner) != 0 {
		return 1 // owned popups; we want top-level frames
	}
	if w, h := windowSize(hwnd); w < 160 || h < 80 {
		return 1
	}
	tid, pid := windowThreadProcess(hwnd)
	enumBuf = append(enumBuf, enumWnd{
		hwnd:  hwnd,
		title: windowText(hwnd),
		class: windowClass(hwnd),
		pid:   pid,
		tid:   tid,
	})
	return 1
})

func enumTopWindows() []enumWnd {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumBuf = nil
	procEnumWindows.Call(enumCallback, 0)
	out := make([]enumWnd, len(enumBuf))
	copy(out, enumBuf)
	return out
}

func windowText(hwnd uintptr) string {
	n, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+2)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), n+1)
	return syscall.UTF16ToString(buf)
}

func windowClass(hwnd uintptr) string {
	buf := make([]uint16, 256)
	procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), 256)
	return syscall.UTF16ToString(buf)
}

func windowThreadProcess(hwnd uintptr) (tid, pid uint32) {
	var p uint32
	t, _, _ := procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&p)))
	return uint32(t), p
}

func windowSize(hwnd uintptr) (w, h int32) {
	var r winRect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r.right - r.left, r.bottom - r.top
}

func isWindow(hwnd uintptr) bool {
	r, _, _ := procIsWindow.Call(hwnd)
	return r != 0
}
func isWindowVisible(hwnd uintptr) bool {
	r, _, _ := procIsWindowVisible.Call(hwnd)
	return r != 0
}
func isIconic(hwnd uintptr) bool {
	r, _, _ := procIsIconic.Call(hwnd)
	return r != 0
}
func getWindow(hwnd uintptr, cmd int) uintptr {
	r, _, _ := procGetWindow.Call(hwnd, uintptr(cmd))
	return r
}
func getAncestor(hwnd uintptr, ga uint32) uintptr {
	r, _, _ := procGetAncestor.Call(hwnd, uintptr(ga))
	return r
}
func getForegroundWindow() uintptr {
	r, _, _ := procGetForegroundWindow.Call()
	return r
}
func getCurrentThreadId() uint32 {
	r, _, _ := procGetCurrentThreadId.Call()
	return uint32(r)
}
func showWindow(hwnd uintptr, cmd int) {
	procShowWindow.Call(hwnd, uintptr(cmd))
}
func bringWindowToTop(hwnd uintptr) {
	procBringWindowToTop.Call(hwnd)
}
func setForegroundWindow(hwnd uintptr) bool {
	r, _, _ := procSetForegroundWindow.Call(hwnd)
	return r != 0
}
func setWindowPos(hwnd, insertAfter uintptr, flags uint32) {
	procSetWindowPos.Call(hwnd, insertAfter, 0, 0, 0, 0, uintptr(flags))
}
func allowSetForegroundWindow(pid uint32) {
	procAllowSetForegroundWindow.Call(uintptr(pid))
}
func attachThreadInput(a, b uint32, attach bool) bool {
	var v uintptr
	if attach {
		v = 1
	}
	r, _, _ := procAttachThreadInput.Call(uintptr(a), uintptr(b), v)
	return r != 0
}

func processExe(pid uint32) string {
	h, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer procCloseHandle.Call(h)
	var n uint32 = 260
	buf := make([]uint16, n)
	// QueryFullProcessImageNameW(handle, 0, buf, &n)
	if err := procQueryFullProcessImageNameW.Find(); err != nil {
		return ""
	}
	r, _, _ := procQueryFullProcessImageNameW.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

func (w enumWnd) isBDDMApp() bool {
	if !isChromeWidgetClass(w.class) {
		return false
	}
	if !isBrowserAppExe(processExe(w.pid)) {
		return false
	}
	return isBDDMUITitle(w.title)
}

func listBDDMAppWindows() []enumWnd {
	var out []enumWnd
	for _, w := range enumTopWindows() {
		if w.isBDDMApp() {
			out = append(out, w)
		}
	}
	return out
}

// dialogOwnerHWND is the BDDM app window that should own a native dialog
// (the one the user just clicked in, if we can tell).
func dialogOwnerHWND() uintptr {
	fg := getForegroundWindow()
	if fg != 0 {
		root := getAncestor(fg, gaRoot)
		if root == 0 {
			root = fg
		}
		tid, pid := windowThreadProcess(root)
		w := enumWnd{hwnd: root, title: windowText(root), class: windowClass(root), pid: pid, tid: tid}
		if w.isBDDMApp() {
			return root
		}
	}
	if wins := listBDDMAppWindows(); len(wins) > 0 {
		return wins[0].hwnd // EnumWindows is in z-order, top first
	}
	return 0
}

// withForeground briefly attaches to the current foreground thread so
// Windows allows this (windowless) process to put a window in front. The
// user has usually just clicked in the Edge/Chrome app window.
func withForeground(fn func()) {
	allowSetForegroundWindow(asfwAny)
	fg := getForegroundWindow()
	fgTid, _ := windowThreadProcess(fg)
	cur := getCurrentThreadId()
	attached := false
	if fg != 0 && fgTid != 0 && fgTid != cur {
		attached = attachThreadInput(cur, fgTid, true)
	}
	defer func() {
		if attached {
			attachThreadInput(cur, fgTid, false)
		}
	}()
	fn()
}

// activateWindow brings hwnd to the front and gives it focus. This is the
// helper used for every window or dialog BDDM opens on Windows.
func activateWindow(hwnd uintptr) bool {
	if hwnd == 0 || !isWindow(hwnd) {
		return false
	}
	allowSetForegroundWindow(asfwAny)
	fg := getForegroundWindow()
	fgTid, _ := windowThreadProcess(fg)
	appTid, appPid := windowThreadProcess(hwnd)
	cur := getCurrentThreadId()
	if appPid != 0 {
		allowSetForegroundWindow(appPid)
	}
	if isIconic(hwnd) {
		showWindow(hwnd, swRestore)
	} else {
		showWindow(hwnd, swShow)
	}
	bringWindowToTop(hwnd)
	// The topmost on/off trick works even when SetForegroundWindow is blocked.
	setWindowPos(hwnd, hwndTopmost, swpNomove|swpNosize)
	setWindowPos(hwnd, hwndNotopmost, swpNomove|swpNosize)
	if fgTid != 0 && fgTid != cur {
		if attachThreadInput(cur, fgTid, true) {
			defer attachThreadInput(cur, fgTid, false)
		}
	}
	if appTid != 0 && appTid != cur && appTid != fgTid {
		if attachThreadInput(cur, appTid, true) {
			defer attachThreadInput(cur, appTid, false)
		}
	}
	setForegroundWindow(hwnd)
	bringWindowToTop(hwnd)
	return true
}

func snapshotHWND() map[uintptr]bool {
	m := map[uintptr]bool{}
	for _, w := range enumTopWindows() {
		m[w.hwnd] = true
	}
	return m
}

func newWindowsSince(before map[uintptr]bool) []enumWnd {
	var out []enumWnd
	for _, w := range enumTopWindows() {
		if !before[w.hwnd] {
			out = append(out, w)
		}
	}
	return out
}

func activateNewWindows(before map[uintptr]bool, match func(enumWnd) bool, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		for _, w := range newWindowsSince(before) {
			if match == nil || match(w) {
				if activateWindow(w.hwnd) {
					return true
				}
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(80 * time.Millisecond)
	}
}

func isNewBrowserWindow(w enumWnd) bool {
	if !isChromeWidgetClass(w.class) && !strings.EqualFold(w.class, "MozillaWindowClass") {
		return false
	}
	return isBrowserAppExe(processExe(w.pid)) || w.class == "MozillaWindowClass"
}

func focusExistingMainWindow() bool {
	for _, w := range listBDDMAppWindows() {
		if w.title == "BDDM - Download Manager" || w.title == "BDDM" || strings.HasPrefix(w.title, "BDDM - ") {
			return activateWindow(w.hwnd)
		}
	}
	return false
}

// focusOpenedWindow is used after we launch an Edge/Chrome app window: if
// the main window is already open we bring that to the front; otherwise we
// wait for the new window and activate it.
func focusOpenedWindow(u string, before map[uintptr]bool, startedPID int) {
	if startedPID > 0 {
		allowSetForegroundWindow(uint32(startedPID))
	}
	if isMainAppURL(u) && focusExistingMainWindow() {
		return
	}
	match := func(w enumWnd) bool {
		if startedPID > 0 && int(w.pid) == startedPID {
			return isChromeWidgetClass(w.class) || isBDDMUITitle(w.title)
		}
		return w.isBDDMApp() || (isChromeWidgetClass(w.class) && isBrowserAppExe(processExe(w.pid)))
	}
	if activateNewWindows(before, match, 3*time.Second) {
		return
	}
	// The launched process often hands off to a child; activate any new
	// BDDM app window, or an existing main window as a last resort.
	if isMainAppURL(u) && focusExistingMainWindow() {
		return
	}
	if wins := listBDDMAppWindows(); len(wins) > 0 {
		activateWindow(wins[0].hwnd)
	}
}
