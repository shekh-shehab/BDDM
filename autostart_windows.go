//go:build windows

package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const runKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`

// installedExe returns BDDM.exe's path, or "" when running from "go run"
// (a temporary file that must not start with Windows).
func installedExe() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	low := strings.ToLower(exe)
	if strings.Contains(low, `\go-build`) || strings.Contains(low, `\temp\`) {
		return ""
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	return exe
}

// applyAutostart adds or removes BDDM in "start with Windows" (for this
// user only, no admin rights needed).
func applyAutostart(on bool) {
	if on {
		exe := installedExe()
		if exe == "" {
			log.Print("start with Windows: not set (BDDM runs from go run)")
			return
		}
		cmd := exec.Command("reg", "add", runKey, "/v", "BDDM", "/t", "REG_SZ", "/d", `"`+exe+`" -nowindow`, "/f")
		hideConsole(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("start with Windows: %v %s", err, out)
		}
		return
	}
	if !autostartEnabled() {
		return
	}
	cmd := exec.Command("reg", "delete", runKey, "/v", "BDDM", "/f")
	hideConsole(cmd)
	_ = cmd.Run()
}

// autostartEnabled reports whether BDDM starts with Windows (the installer
// may have set it).
func autostartEnabled() bool {
	cmd := exec.Command("reg", "query", runKey, "/v", "BDDM")
	hideConsole(cmd)
	return cmd.Run() == nil
}
