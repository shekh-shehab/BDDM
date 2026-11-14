//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// prepareProc puts a helper tool in its own process group, so killTree can
// stop it together with the ffmpeg it starts.
func prepareProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}

const exeSuffix = ""
