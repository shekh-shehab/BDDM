package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// PE optional-header Subsystem values (winnt.h).
const (
	imageSubsystemWindowsGUI = 2
	imageSubsystemWindowsCUI = 3
)

// peSubsystem returns the Subsystem field of a Windows PE executable
// (2 = GUI, 3 = console). Used to make sure the release build is a
// windowed app, so Windows 11's terminal does not open a console tab.
func peSubsystem(path string) (uint16, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var dos [64]byte
	if _, err := io.ReadFull(f, dos[:]); err != nil {
		return 0, fmt.Errorf("too short to be an exe: %w", err)
	}
	if dos[0] != 'M' || dos[1] != 'Z' {
		return 0, fmt.Errorf("not a Windows exe (missing MZ)")
	}
	peOff := binary.LittleEndian.Uint32(dos[0x3C:])
	if _, err := f.Seek(int64(peOff), io.SeekStart); err != nil {
		return 0, err
	}
	var pe [24]byte // "PE\0\0" + 20-byte COFF header
	if _, err := io.ReadFull(f, pe[:]); err != nil {
		return 0, fmt.Errorf("truncated PE header: %w", err)
	}
	if pe[0] != 'P' || pe[1] != 'E' || pe[2] != 0 || pe[3] != 0 {
		return 0, fmt.Errorf("not a Windows exe (missing PE header)")
	}
	// Subsystem is at offset 68 in the optional header for PE32 and PE32+.
	var opt [70]byte
	if _, err := io.ReadFull(f, opt[:]); err != nil {
		return 0, fmt.Errorf("truncated optional header: %w", err)
	}
	magic := binary.LittleEndian.Uint16(opt[0:])
	if magic != 0x10b && magic != 0x20b {
		return 0, fmt.Errorf("unknown PE optional-header magic 0x%X", magic)
	}
	return binary.LittleEndian.Uint16(opt[68:]), nil
}
