//go:build windows

package main

import (
	"os/exec"
	"strconv"
)

// prepareProc hides the console window of a helper tool (yt-dlp, ffmpeg).
func prepareProc(cmd *exec.Cmd) { hideConsole(cmd) }

// killTree stops a helper tool and everything it started (ffmpeg).
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	k := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	hideConsole(k)
	if k.Run() != nil {
		_ = cmd.Process.Kill()
	}
}

const exeSuffix = ".exe"
