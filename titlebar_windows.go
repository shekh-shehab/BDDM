//go:build windows

package main

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

var (
	dwmapi                    = syscall.NewLazyDLL("dwmapi.dll")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

const (
	dwmwaUseImmersiveDarkMode    = 20
	dwmwaUseImmersiveDarkModeOld = 19 // Windows 10 before 20H1
)

// windowsAppsUseDark reports the "Choose your app mode" setting.
func windowsAppsUseDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return false
	}
	return v == 0
}

// setTitleBarDark switches a window's title bar between dark and light.
func setTitleBarDark(hwnd uintptr, dark bool) {
	if hwnd == 0 || procDwmSetWindowAttribute.Find() != nil {
		return
	}
	var v int32
	if dark {
		v = 1
	}
	r, _, _ := procDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkMode, uintptr(unsafe.Pointer(&v)), 4)
	if r != 0 {
		procDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkModeOld, uintptr(unsafe.Pointer(&v)), 4)
	}
}

// refreshFrame repaints the title bar after its colour changed.
func refreshFrame(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	const (
		swpNoSize       = 0x0001
		swpNoMove       = 0x0002
		swpFrameChanged = 0x0020
	)
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoZOrder|swpNoActivate|swpFrameChanged)
}

// themeBgColorRef returns the Win32 COLORREF (0x00BBGGRR) matching the theme.
func themeBgColorRef(dark bool) uint32 {
	if dark {
		return 0x001A1614 // #14161a
	}
	return 0x00EEE9E6 // #e6e9ee
}

// themeBgRGB returns the sRGB bytes matching the theme.
func themeBgRGB(dark bool) (r, g, b uint8) {
	if dark {
		return 0x14, 0x16, 0x1a
	}
	return 0xe6, 0xe9, 0xee
}
