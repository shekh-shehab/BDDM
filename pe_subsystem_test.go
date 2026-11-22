package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPESubsystemDetectsGUIAndConsole(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	gui := filepath.Join(dir, "gui.exe")
	cui := filepath.Join(dir, "cui.exe")
	build := func(out string, extraLdflags string) {
		t.Helper()
		args := []string{"build", "-o", out}
		if extraLdflags != "" {
			args = append(args, "-ldflags", extraLdflags)
		}
		args = append(args, src)
		cmd := exec.Command("go", args...)
		cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
		if outb, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build %s: %v\n%s", out, err, outb)
		}
	}
	build(gui, "-H=windowsgui")
	build(cui, "")

	got, err := peSubsystem(gui)
	if err != nil {
		t.Fatalf("gui: %v", err)
	}
	if got != imageSubsystemWindowsGUI {
		t.Fatalf("gui subsystem = %d, want %d (WINDOWS_GUI)", got, imageSubsystemWindowsGUI)
	}
	got, err = peSubsystem(cui)
	if err != nil {
		t.Fatalf("cui: %v", err)
	}
	if got != imageSubsystemWindowsCUI {
		t.Fatalf("console subsystem = %d, want %d (WINDOWS_CUI)", got, imageSubsystemWindowsCUI)
	}
}

func TestPESubsystemRejectsNonExe(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "not.exe")
	if err := os.WriteFile(p, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := peSubsystem(p); err == nil {
		t.Fatal("expected an error for a non-exe file")
	}
	if _, err := peSubsystem(filepath.Join(dir, "missing.exe")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestWindowsBuildUsesGUISubsystem(t *testing.T) {
	for _, path := range []string{"INSTALL.txt", "build-windows.sh", "main.go"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		s := string(data)
		if !strings.Contains(s, "-H=windowsgui") && !strings.Contains(s, "-H windowsgui") {
			t.Errorf("%s should tell the release/developer build to use -H=windowsgui", path)
		}
	}
	data, err := os.ReadFile("build-windows.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "GOOS=windows") {
		t.Error("build-windows.sh should set GOOS=windows")
	}
	if strings.Contains(s, "-ldflags=-s") && !strings.Contains(s, "windowsgui") {
		t.Error("build-windows.sh must not strip windowsgui from ldflags")
	}
}

func TestInstallerRestartsBDDMAfterUpdate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("installer", "BDDM.nsi"))
	if err != nil {
		t.Fatal(err)
	}
	nsi := string(data)
	if !strings.Contains(nsi, `Exec '"$INSTDIR\BDDM.exe"'`) {
		t.Error("installer should start BDDM after a /run update")
	}
	if !strings.Contains(nsi, `/run`) {
		t.Error("installer should honor /run from the updater")
	}
	// nsExec would wait until BDDM exits, so the post-update start must be Exec.
	if strings.Contains(nsi, `nsExec::Exec '"$INSTDIR\BDDM.exe"'`) {
		t.Error("post-update start must not use nsExec (it would wait for BDDM to quit)")
	}
}
