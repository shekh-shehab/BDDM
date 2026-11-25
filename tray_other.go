//go:build !windows

package main

// startTray is a no-op off Windows; the icon lives in the Windows
// notification area of the long-lived BDDM.exe process.
func startTray(port string, quit func(), m *Manager) {}

func stopTray() {}

func setTrayTip(text string) {}
