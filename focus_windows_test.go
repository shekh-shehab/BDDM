//go:build windows

package main

import (
	"strings"
	"testing"
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
	z := powershellDialogOwner(0)
	if !strings.Contains(z, "TopMost") {
		t.Fatal("without an HWND, the fallback still needs a topmost owner form")
	}
}
