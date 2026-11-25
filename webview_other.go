//go:build !windows

package main

func runWindowHost(u, kind, rect string) int { return 2 }

func prepareWindowHosts() { cleanupOldChromiumProfiles() }
