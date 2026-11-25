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

const (
	// Compact tray popup size, in CSS pixels (DIP). Chromium --window-size
	// uses this unit; Windows placement converts it with the monitor DPI.
	trayPopupWidth  = 260
	trayPopupHeight = 248
)

func openTrayPopup(port string) {
	openTrayPopupWindow(trayPopupURL(port), trayPopupWidth, trayPopupHeight)
}

// trayChromiumArgs launches a dedicated Edge/Chrome profile so the popup
// cannot inherit the main BDDM window's saved size.
func trayChromiumArgs(url, userDataDir string, w, h, x, y int) []string {
	args := append([]string{"--user-data-dir=" + userDataDir}, edgeFallbackFlags()...)
	return append(args,
		fmt.Sprintf("--window-size=%d,%d", w, h),
		fmt.Sprintf("--window-position=%d,%d", x, y),
		"--app="+url,
	)
}

func trayUserDataDir() string { return edgeFallbackDir("tray") }

func dipToPhysical(v, dpi int) int {
	if dpi < 96 {
		dpi = 96
	}
	return v * dpi / 96
}

func physicalToDIP(v, dpi int) int {
	if dpi < 96 {
		dpi = 96
	}
	return v * 96 / dpi
}

// trayPopupRect places a compact popup just above the tray cursor and
// clamps it to the work area. dipW/dipH are CSS pixels; dpi is 96 at 100%.
// The result is in physical pixels.
func trayPopupRect(cursorX, cursorY, workL, workT, workR, workB, dipW, dipH, dpi int) (x, y, pw, ph int) {
	if dpi < 96 {
		dpi = 96
	}
	if dipW < 1 {
		dipW = trayPopupWidth
	}
	if dipH < 1 {
		dipH = trayPopupHeight
	}
	pw, ph = dipToPhysical(dipW, dpi), dipToPhysical(dipH, dpi)
	margin := dipToPhysical(8, dpi)
	if margin < 4 {
		margin = 4
	}
	x = cursorX - pw/2
	y = cursorY - ph - margin
	if workR > workL && workB > workT {
		if x+pw > workR-margin {
			x = workR - margin - pw
		}
		if x < workL+margin {
			x = workL + margin
		}
		if y+ph > workB-margin {
			y = workB - margin - ph
		}
		if y < workT+margin {
			y = workT + margin
		}
	}
	return
}
