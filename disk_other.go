//go:build !windows

package main

import "syscall"

// diskFree returns the bytes this user may still write on the disk of dir.
func diskFree(dir string) (int64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil {
		return -1, err
	}
	return int64(s.Bavail) * int64(s.Bsize), nil
}
