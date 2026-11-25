//go:build windows

package main

import (
	"strings"
	"unsafe"
)

var (
	procGetClientRect   = user32.NewProc("GetClientRect")
	procGetDpiForWindow = user32.NewProc("GetDpiForWindow")
)

func dpiForWindow(hwnd uintptr) int {
	if hwnd != 0 && procGetDpiForWindow.Find() == nil {
		r, _, _ := procGetDpiForWindow.Call(hwnd)
		if r >= 96 {
			return int(r)
		}
	}
	return primaryDPI()
}

func windowFrameSize(hwnd uintptr) (fw, fh int) {
	dpi := dpiForWindow(hwnd)
	fallbackW, fallbackH := dipToPhysical(16, dpi), dipToPhysical(39, dpi)
	if hwnd == 0 || !isWindow(hwnd) {
		return fallbackW, fallbackH
	}
	var wr, cr winRect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&cr)))
	fw = int(wr.right-wr.left) - int(cr.right-cr.left)
	fh = int(wr.bottom-wr.top) - int(cr.bottom-cr.top)
	if fw < 0 {
		fw = 0
	}
	if fh < 0 {
		fh = 0
	}
	if fw == 0 && fh == 0 {
		return fallbackW, fallbackH
	}
	return fw, fh
}

func findPopupHWND(title string) uintptr {
	title = strings.TrimSpace(title)
	var family uintptr
	for _, w := range enumTopWindows() {
		if !isPopupWindowTitle(w.title) {
			continue
		}
		if title == "" || w.title == title || strings.HasPrefix(w.title, title) || strings.HasPrefix(title, w.title) {
			return w.hwnd
		}
		if family == 0 {
			family = w.hwnd
		}
	}
	return family
}

func applyPopupFit(title string, dipW, dipH int) {
	if dipW < 80 && dipH < 80 {
		return
	}
	if title != "" && !isPopupWindowTitle(title) {
		return
	}
	hwnd := findPopupHWND(title)
	if hwnd == 0 {
		return
	}
	fw, fh := windowFrameSize(hwnd)
	work := primaryWorkArea()
	dpi := dpiForWindow(hwnd)
	x, y, pw, ph := popupFitRect(int(work.left), int(work.top), int(work.right), int(work.bottom), dipW, dipH, fw, fh, dpi)
	showWindow(hwnd, swRestore)
	setWindowPosRect(hwnd, x, y, pw, ph)
	activateWindow(hwnd)
}
