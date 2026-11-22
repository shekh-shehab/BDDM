package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppUserModelID(t *testing.T) {
	if appUserModelID != "BDDM.DownloadManager" {
		t.Fatalf("appUserModelID = %q", appUserModelID)
	}
	if appDisplayName != "BDDM" {
		t.Fatalf("appDisplayName = %q", appDisplayName)
	}
	if !strings.Contains(powershellAppID, "WindowsPowerShell") {
		t.Fatalf("powershell fallback ID looks wrong: %q", powershellAppID)
	}
}

func TestFileURI(t *testing.T) {
	got := fileURI(`C:\Users\Ada\AppData\Local\Programs\BDDM\BDDM.png`)
	want := "file:///C:/Users/Ada/AppData/Local/Programs/BDDM/BDDM.png"
	if got != want {
		t.Fatalf("fileURI: got %q want %q", got, want)
	}
	spaced := fileURI(`C:\Program Files\BDDM\icon.png`)
	if !strings.Contains(spaced, "file:///C:/Program") || !strings.Contains(spaced, "Files") {
		t.Fatalf("fileURI did not keep the path: %q", spaced)
	}
	if strings.Contains(spaced, " ") {
		t.Fatalf("fileURI should escape spaces: %q", spaced)
	}
	if fileURI("") != "" {
		t.Fatal("empty path should be an empty URI")
	}
}

func TestShortcutCandidatePaths(t *testing.T) {
	paths := shortcutCandidatePaths(`C:\Users\Ada`, `C:\Users\Ada\AppData\Roaming`)
	joined := strings.Join(paths, "\n")
	if !strings.Contains(joined, `Start Menu\Programs\BDDM\BDDM.lnk`) &&
		!strings.Contains(joined, `Start Menu/Programs/BDDM/BDDM.lnk`) {
		t.Fatalf("missing Start menu shortcut: %v", paths)
	}
	if !strings.Contains(joined, "Desktop") || !strings.Contains(joined, "BDDM.lnk") {
		t.Fatalf("missing desktop shortcut: %v", paths)
	}
	if len(paths) < 2 {
		t.Fatalf("expected start menu and desktop, got %v", paths)
	}
}

func TestSameExeAndShouldStampShortcut(t *testing.T) {
	exe := `C:\Users\Ada\AppData\Local\Programs\BDDM\BDDM.exe`
	if !sameExePath(exe, `c:\users\ada\appdata\local\programs\bddm\bddm.exe`) {
		t.Fatal("same path with different case should match")
	}
	if shouldStampShortcut("", exe) != true {
		t.Fatal("empty target on a known shortcut path should still be stamped")
	}
	if !shouldStampShortcut(exe, exe) {
		t.Fatal("shortcut to this install should be stamped")
	}
	if shouldStampShortcut(`C:\Other\BDDM.exe`, exe) {
		t.Fatal("a different install must not be stamped")
	}
	if shouldStampShortcut(`C:\Windows\explorer.exe`, exe) {
		t.Fatal("an unrelated target must not be stamped")
	}
}

func TestRelaunchCommandAndIcon(t *testing.T) {
	exe := `C:\Users\Ada\AppData\Local\Programs\BDDM\BDDM.exe`
	if got := relaunchCommand(exe); got != `"`+exe+`"` {
		t.Fatalf("relaunchCommand = %q", got)
	}
	if got := relaunchIconResource(exe, `C:\Users\Ada\AppData\Local\Programs\BDDM\BDDM.ico`); got != `C:\Users\Ada\AppData\Local\Programs\BDDM\BDDM.ico` {
		t.Fatalf("icon with ico = %q", got)
	}
	if got := relaunchIconResource(exe, ""); got != exe+",0" {
		t.Fatalf("icon from exe = %q", got)
	}
}

func TestToastXMLAndNotifyScriptUseBDDMIdentity(t *testing.T) {
	xml := toastXML("Download finished", `done & "ok"`, "file:///C:/BDDM/BDDM.png")
	if !strings.Contains(xml, "Download finished") || !strings.Contains(xml, "done &amp; &quot;ok&quot;") {
		t.Fatalf("xml missing escaped text: %s", xml)
	}
	if !strings.Contains(xml, `placement="appLogoOverride"`) || !strings.Contains(xml, "file:///C:/BDDM/BDDM.png") {
		t.Fatalf("xml missing logo: %s", xml)
	}
	plain := toastXML("T", "B", "")
	if strings.Contains(plain, "appLogoOverride") {
		t.Fatalf("logo should be omitted when no path: %s", plain)
	}

	script := windowsNotifyScript("Download finished", "movie.mp4 is complete", "file:///C:/BDDM/BDDM.png", `C:\BDDM\BDDM.ico`)
	if !strings.Contains(script, appUserModelID) {
		t.Fatal("toast script must use BDDM's AppUserModelID")
	}
	if !strings.Contains(script, "$appId = '"+appUserModelID+"'") {
		t.Fatalf("BDDM AUMID should be the primary notifier: %s", script)
	}
	// PowerShell is only the fallback, not the identity we try first.
	appIdx := strings.Index(script, "$appId = ")
	fbIdx := strings.Index(script, "$fallbackId = ")
	psIdx := strings.Index(script, powershellAppID)
	if appIdx < 0 || fbIdx < 0 || psIdx < 0 || !(appIdx < fbIdx && fbIdx < psIdx) {
		t.Fatal("PowerShell AUMID must only appear as a fallback")
	}
	if strings.Contains(script, "$appId = '"+powershellAppID+"'") {
		t.Fatal("the primary $appId must not be Windows PowerShell")
	}
	if !strings.Contains(script, `C:\BDDM\BDDM.ico`) {
		t.Fatal("balloon fallback should use the BDDM icon")
	}
	if !strings.Contains(script, "New-Object System.Drawing.Icon") {
		t.Fatal("balloon fallback should load BDDM.ico")
	}
	if !strings.Contains(script, "appLogoOverride") {
		t.Fatal("toast XML in the script should include the logo")
	}
}

func TestInstallIconPaths(t *testing.T) {
	dir := t.TempDir()
	png, ico := installIconPaths(dir)
	if png != "" || ico != "" {
		t.Fatalf("empty dir should have no icons, got %q %q", png, ico)
	}
	icoPath := filepath.Join(dir, "BDDM.ico")
	pngPath := filepath.Join(dir, "BDDM.png")
	if err := os.WriteFile(icoPath, []byte("ico"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pngPath, []byte("png"), 0644); err != nil {
		t.Fatal(err)
	}
	png, ico = installIconPaths(dir)
	if png != pngPath || ico != icoPath {
		t.Fatalf("got png=%q ico=%q", png, ico)
	}
}

func TestInstallerSetsAUMIDAndRegister(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("installer", "BDDM.nsi"))
	if err != nil {
		t.Fatal(err)
	}
	nsi := string(data)
	for _, want := range []string{
		`APP_USER_MODEL_ID "BDDM.DownloadManager"`,
		`BDDM.exe" -register`,
		`File "/oname=BDDM.png"`,
		`Delete "$INSTDIR\BDDM.png"`,
		`Software\Classes\AppUserModelId\${APP_USER_MODEL_ID}`,
		`!define VERSION "1.4.19"`,
	} {
		if !strings.Contains(nsi, want) {
			t.Errorf("BDDM.nsi missing %q", want)
		}
	}
	if !strings.Contains(nsi, `CreateShortcut "$SMPROGRAMS\BDDM\BDDM.lnk"`) {
		t.Error("Start menu shortcut should still be created (and recreated on update)")
	}
	if !strings.Contains(nsi, `CreateShortcut "$DESKTOP\BDDM.lnk"`) {
		t.Error("desktop shortcut should still be created")
	}
}

func TestAppVersionIs1418(t *testing.T) {
	if appVersion != "1.4.19" {
		t.Fatalf("appVersion = %q, want 1.4.19", appVersion)
	}
}

func TestInstallerWaitsForGracefulQuit(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("installer", "BDDM.nsi"))
	if err != nil {
		t.Fatal(err)
	}
	nsi := string(data)
	if !strings.Contains(nsi, `-quit`) {
		t.Fatal("installer should ask a running BDDM to quit")
	}
	if !strings.Contains(nsi, "tasklist") || !strings.Contains(nsi, "loop_wait_") {
		t.Fatal("installer should wait for BDDM.exe to exit before force-killing it")
	}
	quit := strings.Index(nsi, `-quit`)
	wait := strings.Index(nsi, "tasklist")
	kill := strings.LastIndex(nsi, `taskkill /IM BDDM.exe /F`)
	if quit < 0 || wait < 0 || kill < 0 || !(quit < wait && wait < kill) {
		t.Fatal("force-kill must come after asking BDDM to quit and waiting for it")
	}
}
