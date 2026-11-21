package main

import (
	"net/url"
	"path/filepath"
	"strings"
)

// appUserModelID is how Windows groups BDDM's shortcuts, notifications
// and (when we can set it) the Edge/Chrome app window. It must stay
// stable across updates.
const appUserModelID = "BDDM.DownloadManager"

const appDisplayName = "BDDM"

// powershellAppID is the well-known identity of Windows PowerShell. We
// only use it if a toast cannot be shown under BDDM's own identity
// (for example a portable copy with no Start-menu shortcut yet).
const powershellAppID = `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe`

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// fileURI turns a local path into a file: URI that Windows toast XML
// and registry IconUri values accept.
func fileURI(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	// These paths are often Windows-style even when tests run on Linux.
	p := filepath.ToSlash(strings.ReplaceAll(path, "\\", "/"))
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	return u.String()
}

// installIconPaths returns the PNG and ICO next to the executable, if
// they were installed (BDDM.png / BDDM.ico).
func installIconPaths(dir string) (png, ico string) {
	for _, name := range []string{"BDDM.png", "icon.png"} {
		p := filepath.Join(dir, name)
		if fileExists(p) {
			png = p
			break
		}
	}
	p := filepath.Join(dir, "BDDM.ico")
	if fileExists(p) {
		ico = p
	}
	return png, ico
}

// shortcutCandidatePaths is the Start-menu and desktop shortcuts the
// installer creates (and recreates on update).
func shortcutCandidatePaths(home, appData string) []string {
	var out []string
	if appData != "" {
		out = append(out, filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "BDDM", "BDDM.lnk"))
	}
	if home != "" {
		out = append(out, filepath.Join(home, "Desktop", "BDDM.lnk"))
		// OneDrive can redirect the desktop on some accounts.
		out = append(out, filepath.Join(home, "OneDrive", "Desktop", "BDDM.lnk"))
	}
	return out
}

func sameExePath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if a == "" || b == "" || a == "." || b == "." {
		return false
	}
	if strings.EqualFold(a, b) {
		return true
	}
	return strings.EqualFold(filepath.Base(a), filepath.Base(b)) &&
		strings.EqualFold(filepath.Base(a), "BDDM.exe") &&
		strings.EqualFold(filepath.Dir(a), filepath.Dir(b))
}

// shouldStampShortcut is true when the .lnk is one of BDDM's known
// shortcuts, or when its target is this install's BDDM.exe.
func shouldStampShortcut(target, ourExe string) bool {
	if target == "" {
		return true
	}
	return sameExePath(target, ourExe)
}

func relaunchCommand(exe string) string {
	exe = strings.TrimSpace(exe)
	if exe == "" {
		return ""
	}
	if !strings.HasPrefix(exe, `"`) {
		return `"` + exe + `"`
	}
	return exe
}

func relaunchIconResource(exe, ico string) string {
	if ico != "" {
		return ico
	}
	if exe != "" {
		return exe + ",0"
	}
	return ""
}

// toastXML is the Windows toast payload. If logoURI is set, the new
// dark icon is shown on the toast itself (app logo).
func toastXML(title, body, logoURI string) string {
	var b strings.Builder
	b.WriteString(`<toast><visual><binding template="ToastGeneric">`)
	if logoURI != "" {
		b.WriteString(`<image placement="appLogoOverride" hint-crop="none" src="` + xmlEscape(logoURI) + `"/>`)
	}
	b.WriteString(`<text>` + xmlEscape(title) + `</text><text>` + xmlEscape(body) + `</text>`)
	b.WriteString(`</binding></visual></toast>`)
	return b.String()
}

// windowsNotifyScript shows a toast under BDDM's AppUserModelID. If
// that identity is not registered yet, it tries PowerShell's identity,
// then a tray balloon (using the BDDM icon when available).
func windowsNotifyScript(title, body, logoURI, icoPath string) string {
	xml := toastXML(title, body, logoURI)
	script := `
$ErrorActionPreference = 'Stop'
$title = ` + psQuote(title) + `
$body = ` + psQuote(body) + `
$appId = ` + psQuote(appUserModelID) + `
$fallbackId = ` + psQuote(powershellAppID) + `
$ico = ` + psQuote(icoPath) + `
try {
  [Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
  [Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null
  $xml = New-Object Windows.Data.Xml.Dom.XmlDocument
  $xml.LoadXml(` + psQuote(xml) + `)
  $toast = New-Object Windows.UI.Notifications.ToastNotification $xml
  try {
    [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($appId).Show($toast)
  } catch {
    [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($fallbackId).Show($toast)
  }
} catch {
  Add-Type -AssemblyName System.Windows.Forms
  Add-Type -AssemblyName System.Drawing
  $n = New-Object System.Windows.Forms.NotifyIcon
  if ($ico -ne '' -and (Test-Path -LiteralPath $ico)) {
    $n.Icon = New-Object System.Drawing.Icon $ico
  } else {
    $n.Icon = [System.Drawing.SystemIcons]::Information
  }
  $n.BalloonTipTitle = $title
  $n.BalloonTipText = $body
  $n.Visible = $true
  $n.ShowBalloonTip(5000)
  Start-Sleep -Seconds 6
  $n.Dispose()
}
`
	return script
}
