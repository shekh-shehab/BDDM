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
