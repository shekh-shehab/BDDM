//go:build !windows

package main

// notify_other.go: desktop helpers for Linux/macOS (used for testing).

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
)

func hideConsole(cmd *exec.Cmd) {}

// notify shows a desktop notification if notify-send/osascript exists.
func notify(title, body string) {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("osascript", "-e", `display notification "`+escapeAS(body)+`" with title "`+escapeAS(title)+`"`)
	} else if p, err := exec.LookPath("notify-send"); err == nil {
		cmd = exec.Command(p, title, body)
	}
	if cmd == nil {
		log.Printf("notification: %s - %s", title, body)
		return
	}
	if err := cmd.Run(); err != nil {
		log.Printf("notification failed: %v", err)
	}
}

func escapeAS(s string) string {
	out := []rune{}
	for _, r := range s {
		if r == '"' || r == '\\' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return string(out)
}

// openURL opens an http(s) address in the default browser.
func openURL(raw string) error {
	if err := validWebURL(raw); err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		return exec.Command("open", raw).Start()
	}
	return exec.Command("xdg-open", raw).Start()
}

// openFolder opens dir in the file manager (file is selected on Windows only).
func openFolder(dir, file string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
		if file != "" {
			return exec.Command(opener, "-R", file).Start()
		}
	}
	return exec.Command(opener, dir).Start()
}

func pickFolder(start string) (string, error) {
	if p, err := exec.LookPath("zenity"); err == nil && os.Getenv("DISPLAY") != "" {
		out, err := exec.Command(p, "--file-selection", "--directory", "--filename="+start+"/").Output()
		if err != nil {
			return "", nil // cancelled
		}
		return trimNL(string(out)), nil
	}
	return "", errors.New("folder picker is only available on Windows; type the path instead")
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// openAppWindow opens the UI in a Chrome/Chromium app window, or the default browser.
func openAppWindow(u string) error { return openAppWindowSize(u, 1100, 720) }

// openAppWindowSize opens u in an app window of w x h pixels.
func openAppWindowSize(u string, w, h int) error {
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return errors.New("no display; open " + u + " in a browser")
	}
	for _, b := range []string{"microsoft-edge", "google-chrome", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(b); err == nil {
			return exec.Command(p, "--app="+u, fmt.Sprintf("--window-size=%d,%d", w, h)).Start()
		}
	}
	if runtime.GOOS == "darwin" {
		return exec.Command("open", u).Start()
	}
	return exec.Command("xdg-open", u).Start()
}
