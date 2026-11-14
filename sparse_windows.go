//go:build windows

package main

import (
	"os"
	"syscall"
)

// makeSparse marks the file as sparse. Without this, Windows fills the
// whole gap with zeros before a part far into the file can write anything,
// which makes big downloads look stuck at 0%.
func makeSparse(f *os.File) {
	const fsctlSetSparse = 0x000900c4
	var ret uint32
	_ = syscall.DeviceIoControl(syscall.Handle(f.Fd()), fsctlSetSparse, nil, 0, nil, 0, &ret, nil)
}
