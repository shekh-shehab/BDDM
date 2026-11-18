//go:build windows

package main

// notify_windows.go: Windows helpers — toast notifications, opening Explorer,
// the folder picker and the app window. Everything here runs in the
// background and never blocks downloads if it fails.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
)

const createNoWindow = 0x08000000

// hideConsole stops a black console window from flashing when we start
// PowerShell or cmd from the windowless BDDM.exe.
func hideConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}

// runPowerShell runs a script with Windows PowerShell 5.1 (built into Windows).
func runPowerShell(script string, timeout time.Duration) (string, error) {
	// -EncodedCommand takes UTF-16LE base64, so we never fight with quoting.
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, v := range u {
		b[2*i] = byte(v)
		b[2*i+1] = byte(v >> 8)
	}
	enc := base64.StdEncoding.EncodeToString(b)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-STA", "-ExecutionPolicy", "Bypass", "-EncodedCommand", enc)
	hideConsole(cmd)
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return out.String(), err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return out.String(), errors.New("powershell timed out")
	}
}

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// notify shows a Windows toast. If toasts are not available it falls back to
// a tray balloon. Errors are only logged.
func notify(title, body string) {
	xml := `<toast><visual><binding template="ToastGeneric"><text>` + xmlEscape(title) +
		`</text><text>` + xmlEscape(body) + `</text></binding></visual></toast>`
	script := `
$ErrorActionPreference = 'Stop'
$title = ` + psQuote(title) + `
$body = ` + psQuote(body) + `
try {
  [Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
  [Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null
  $xml = New-Object Windows.Data.Xml.Dom.XmlDocument
  $xml.LoadXml(` + psQuote(xml) + `)
  $toast = New-Object Windows.UI.Notifications.ToastNotification $xml
  $appId = '{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe'
  [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($appId).Show($toast)
} catch {
  Add-Type -AssemblyName System.Windows.Forms
  Add-Type -AssemblyName System.Drawing
  $n = New-Object System.Windows.Forms.NotifyIcon
  $n.Icon = [System.Drawing.SystemIcons]::Information
  $n.BalloonTipTitle = $title
  $n.BalloonTipText = $body
  $n.Visible = $true
  $n.ShowBalloonTip(5000)
  Start-Sleep -Seconds 6
  $n.Dispose()
}
`
	if _, err := runPowerShell(script, 20*time.Second); err != nil {
		log.Printf("notification failed: %v", err)
	}
}

// openURL opens an http(s) address in the default browser.
func openURL(raw string) error {
	if err := validWebURL(raw); err != nil {
		return err
	}
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", raw)
	return cmd.Start()
}

// openFolder opens Explorer at dir, selecting file when given.
func openFolder(dir, file string) error {
	var cmd *exec.Cmd
	if file != "" {
		cmd = exec.Command("explorer.exe")
		// Explorer needs exactly: /select,"C:\path\file" (Go's normal quoting breaks it).
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + file + `"`}
	} else {
		cmd = exec.Command("explorer.exe", dir)
	}
	return cmd.Start() // explorer often returns exit code 1, so don't wait
}

// pickFolder shows the Windows "Browse For Folder" dialog. Returns "" if cancelled.
func pickFolder(start string) (string, error) {
	script := `
Add-Type -AssemblyName System.Windows.Forms
$owner = New-Object System.Windows.Forms.Form -Property @{TopMost = $true; ShowInTaskbar = $false}
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.Description = 'Choose the folder for downloads'
$d.SelectedPath = ` + psQuote(start) + `
$d.ShowNewFolderButton = $true
if ($d.ShowDialog($owner) -eq [System.Windows.Forms.DialogResult]::OK) {
  [Console]::OutputEncoding = [System.Text.Encoding]::UTF8
  Write-Output $d.SelectedPath
}
`
	out, err := runPowerShell(script, 10*time.Minute)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// openAppWindow opens the UI as an app window: Edge, then Chrome, then the
// default browser.
func openAppWindow(u string) error { return openAppWindowSize(u, 1100, 720) }

// openAppWindowSize opens u in an Edge (or Chrome) app window of w x h pixels.
func openAppWindowSize(u string, w, h int) error {
	args := []string{"--app=" + u, fmt.Sprintf("--window-size=%d,%d", w, h)}
	var candidates []string
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
		if base := os.Getenv(env); base != "" {
			candidates = append(candidates, filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"))
		}
	}
	edgeCount := len(candidates)
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
		if base := os.Getenv(env); base != "" {
			candidates = append(candidates, filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"))
		}
	}
	for i, p := range candidates {
		if i == edgeCount {
			// Edge not found in the usual folders: let Windows find it.
			cmd := exec.Command("cmd", append([]string{"/c", "start", "", "msedge"}, args...)...)
			hideConsole(cmd)
			if cmd.Run() == nil {
				return nil
			}
		}
		if fileExists(p) {
			if err := exec.Command(p, args...).Start(); err == nil {
				return nil
			}
		}
	}
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	return cmd.Start()
}
