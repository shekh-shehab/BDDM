//go:build !windows

package main

func setCurrentProcessAppUserModelID() {}

func registerAppIdentity() error { return nil }

func tagBDDMAppWindows() {}

func scheduleTagBDDMAppWindows() {}
