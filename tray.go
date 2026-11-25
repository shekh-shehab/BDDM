package main

import "fmt"

// Tray menu labels (item 29). Kept here so tests and the Windows menu
// stay in English and in sync.
const (
	trayMenuShow     = "Show BDDM"
	trayMenuAddURL   = "Add URL"
	trayMenuPauseAll = "Pause all"
	trayMenuSettings = "Settings"
	trayMenuQuit     = "Quit"
)

type traySnapshot struct {
	Active int
	Speed  float64 // bytes/s, all downloading jobs
	Done   int64
	Size   int64
	Pct    int // 0-100, or -1 if unknown
}

func snapshotTray(jobs []JobView) traySnapshot {
	var s traySnapshot
	s.Pct = -1
	for _, j := range jobs {
		if j.Status == StatusDownloading {
			s.Active++
			s.Speed += j.Speed
			if j.Size > 0 {
				s.Size += j.Size
				if j.Done > 0 {
					s.Done += j.Done
				}
			}
		}
	}
	if s.Size > 0 {
		p := int((s.Done * 100) / s.Size)
		if p < 0 {
			p = 0
		}
		if p > 100 {
			p = 100
		}
		s.Pct = p
	}
	return s
}

func formatSpeedShort(b float64) string {
	if b <= 0 {
		return "0 B/s"
	}
	n, u := b, "B/s"
	switch {
	case b >= 1024*1024*1024:
		n, u = b/(1024*1024*1024), "GB/s"
	case b >= 1024*1024:
		n, u = b/(1024*1024), "MB/s"
	case b >= 1024:
		n, u = b/1024, "KB/s"
	}
	if n >= 10 || u == "B/s" {
		return fmt.Sprintf("%.0f %s", n, u)
	}
	return fmt.Sprintf("%.1f %s", n, u)
}

// formatTrayHeadline is the line in the compact tray popup.
func formatTrayHeadline(s traySnapshot) string {
	if s.Active == 0 {
		return "No active downloads"
	}
	noun := "active"
	if s.Active == 1 {
		noun = "active"
	}
	return fmt.Sprintf("%d %s, %s", s.Active, noun, formatSpeedShort(s.Speed))
}

// formatTrayTip is the tray icon tooltip (item 31): "BDDM — 2.4 MB/s, 68%".
func formatTrayTip(s traySnapshot) string {
	if s.Active == 0 {
		return "BDDM"
	}
	if s.Pct >= 0 {
		return fmt.Sprintf("BDDM — %s, %d%%", formatSpeedShort(s.Speed), s.Pct)
	}
	return fmt.Sprintf("BDDM — %s", formatSpeedShort(s.Speed))
}

func trayShowURL(port string) string {
	return "http://127.0.0.1:" + port + "/"
}

func trayAddURL(port string) string {
	return "http://127.0.0.1:" + port + "/?add=1"
}

func traySettingsURL(port string) string {
	return "http://127.0.0.1:" + port + "/?tab=settings"
}

func trayPopupURL(port string) string {
	return "http://127.0.0.1:" + port + "/tray.html"
}

func openTrayPopup(port string) {
	_ = openAppWindowSize(trayPopupURL(port), 240, 280)
}
