//go:build windows

package main

import (
	"strings"
	"testing"
	"time"
)

func TestActivateWindowZero(t *testing.T) {
	if activateWindow(0) {
		t.Fatal("a missing window must not be reported as activated")
	}
}

func TestPowershellDialogOwnerUsesHWND(t *testing.T) {
	s := powershellDialogOwner(0x1234)
	if !strings.Contains(s, "4660") || !strings.Contains(s, "HwndOwner") {
		t.Fatalf("owner script should wrap the real HWND:\n%s", s)
	}
	if strings.Contains(s, "New-Object System.Windows.Forms.Form") {
		t.Fatal("a real HWND must not fall back to a dummy form")
	}
	if strings.Contains(s, "ShowWindow($hwnd") || strings.Contains(s, "SetForegroundWindow($hwnd") {
		t.Fatal("must not raise the Edge/Chrome owner window")
	}
	z := powershellDialogOwner(0)
	if !strings.Contains(z, "TopMost") {
		t.Fatal("without an HWND, the fallback still needs a topmost owner form")
	}
}

func TestRaiseNewDialogMissingWindow(t *testing.T) {
	// Must not panic when no picker exists.
	raiseNewDialog(nil, 0, 0, time.Millisecond)
	raiseNewDialog(map[uintptr]bool{}, 1, 1, time.Millisecond)
}

func TestPowershellPreludeRaisesDialogNotOwner(t *testing.T) {
	if !strings.Contains(powershellDialogPrelude, "Raise-BddmDialog") || !strings.Contains(powershellDialogPrelude, "FindWindow") {
		t.Fatal("PowerShell fallback should look for the picker HWND")
	}
	if !strings.Contains(powershellDialogPrelude, "#32770") {
		t.Fatal("expected the standard dialog class")
	}
}
