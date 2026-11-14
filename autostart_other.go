//go:build !windows

package main

// Starting with the system is only set up on Windows.
func applyAutostart(on bool) {}

func autostartEnabled() bool { return false }
