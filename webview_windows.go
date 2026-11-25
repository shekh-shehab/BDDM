//go:build windows

package main

import (
	"log"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"github.com/jchv/go-webview2/webviewloader"
)

const (
	wsOverlappedWindow = 0x00CF0000
	wsPopup            = 0x80000000
	wsBorder           = 0x00800000
	wsExToolWindow     = 0x00000080
	wsExTopmost        = 0x00000008

	wmSize          = 0x0005
	wmMove          = 0x0003
	wmActivate      = 0x0006
	wmSetFocus      = 0x0007
	wmClose         = 0x0010
	wmTimer         = 0x0113
	wmGetMinMaxInfo = 0x0024
	wmExitSizeMove  = 0x0232
	wmSettingChange = 0x001A
	wmDpiChanged    = 0x02E0
	wmSetIcon       = 0x0080
	waInactive      = 0

	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010

	mbYesNo          = 0x00000004
	mbIconInfo       = 0x00000040
	mbTopmost        = 0x00040000
	mbSetForeground  = 0x00010000
	idYes            = 6
	coinitApartment  = 0x2
	hostShowTimerID  = 1
	hostShowFallback = 1500 // ms: show even if the page never finishes loading
)

var (
	procExtractIconExW = shell32.NewProc("ExtractIconExW")
	procSetWindowTextW = user32.NewProc("SetWindowTextW")
	procUpdateWindow   = user32.NewProc("UpdateWindow")
	procSetTimer       = user32.NewProc("SetTimer")
	procKillTimer      = user32.NewProc("KillTimer")
	procIsZoomed       = user32.NewProc("IsZoomed")
	procSendMessageW   = user32.NewProc("SendMessageW")
	procMessageBoxW    = user32.NewProc("MessageBoxW")
	procLoadCursorW    = user32.NewProc("LoadCursorW")

	selfExeOnce sync.Once
	selfExe     string
)

func selfExePath() string {
	selfExeOnce.Do(func() {
		if p, err := os.Executable(); err == nil {
			selfExe = p
		}
	})
	return selfExe
}

// ---------------------------------------------------------------------------
// Helper process: one WebView2 window.
// ---------------------------------------------------------------------------

type webHost struct {
	hwnd     uintptr
	kind     string
	chromium *edge.Chromium
	ready    bool
	shown    bool
	active   bool // has been the active window at least once
	pageDark int  // 0 = page has not said yet, 1 = light, 2 = dark
}

var host *webHost

func runWindowHost(u, kind, rectArg string) int {
	runtime.LockOSThread()
	if !isLocalBDDMURL(u) || !validWindowKind(kind) {
		log.Printf("window host: refusing %q (%s)", u, kind)
		return 2
	}
	r, ok := parseWindowRect(rectArg)
	if !ok {
		work := primaryWorkArea()
		x, y, w, h := mainWindowCenteredRect(int(work.left), int(work.top), int(work.right), int(work.bottom), mainWindowWidth, mainWindowHeight, primaryDPI())
		r = windowRect{x, y, w, h}
	}
	procCoInitializeEx.Call(0, coinitApartment)
	setCurrentProcessAppUserModelID()
	_ = os.MkdirAll(webviewDataDir(), 0755)

	host = &webHost{kind: kind}
	if host.create(windowTitleForURL(u), r) == 0 {
		return 3
	}
	c := edge.NewChromium()
	c.DataPath = webviewDataDir()
	c.MessageCallback = host.onMessage
	c.NavigationCompletedCallback = func(*edge.ICoreWebView2, *edge.ICoreWebView2NavigationCompletedEventArgs) {
		host.show()
	}
	host.chromium = c
	if !c.Embed(host.hwnd) {
		log.Print("window host: WebView2 could not start")
		return 4
	}
	host.ready = true
	if s, err := c.GetSettings(); err == nil {
		_ = s.PutAreDefaultContextMenusEnabled(false)
		_ = s.PutAreDevToolsEnabled(false)
		_ = s.PutIsStatusBarEnabled(false)
		_ = s.PutIsZoomControlEnabled(false)
		_ = s.PutAreBrowserAcceleratorKeysEnabled(false)
	}
	c.Init(webviewInitScript)
	c.Resize()
	c.Navigate(u)
	procSetTimer.Call(host.hwnd, hostShowTimerID, hostShowFallback, 0)

	var msg winMsg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	return 0
}

func (h *webHost) create(title string, r windowRect) uintptr {
	hinst, _, _ := procGetModuleHandleW.Call(0)
	var big, small uintptr
	if exe := selfExePath(); exe != "" {
		p, _ := syscall.UTF16PtrFromString(exe)
		procExtractIconExW.Call(uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&big)), uintptr(unsafe.Pointer(&small)), 1)
	}
	cls, _ := syscall.UTF16PtrFromString(bddmWebViewClass)
	cursor, _, _ := procLoadCursorW.Call(0, 32512) // IDC_ARROW
	wc := wndClassEx{
		lpfnWndProc:   syscall.NewCallback(hostWndProc),
		hInstance:     hinst,
		hIcon:         big,
		hCursor:       cursor,
		hIconSm:       small,
		lpszClassName: cls,
	}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	style, ex := uintptr(wsOverlappedWindow), uintptr(0)
	if h.kind == windowKindTray {
		style, ex = wsPopup|wsBorder, wsExToolWindow|wsExTopmost
	}
	t, _ := syscall.UTF16PtrFromString(title)
	hwnd, _, _ := procCreateWindowExW.Call(ex, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(t)), style,
		uintptr(int32(r.X)), uintptr(int32(r.Y)), uintptr(int32(r.W)), uintptr(int32(r.H)), 0, 0, hinst, 0)
	h.hwnd = hwnd
	if hwnd != 0 {
		setTitleBarDark(hwnd, themeIsDark(uiTheme(), windowsAppsUseDark()))
		if big != 0 {
			procSendMessageW.Call(hwnd, wmSetIcon, 1, big)
		}
		if small != 0 {
			procSendMessageW.Call(hwnd, wmSetIcon, 0, small)
		}
	}
	return hwnd
}

func (h *webHost) show() {
	if h.shown || h.hwnd == 0 {
		return
	}
	h.shown = true
	procKillTimer.Call(h.hwnd, hostShowTimerID)
	showWindow(h.hwnd, swShow)
	procUpdateWindow.Call(h.hwnd)
	setForegroundWindow(h.hwnd)
	if h.chromium != nil {
		h.chromium.Focus()
	}
}

func (h *webHost) onMessage(m string) {
	switch cmd, arg := parseHostMessage(m); cmd {
	case "close":
		procPostMessageW.Call(h.hwnd, wmClose, 0, 0)
	case "theme":
		// The page knows the chosen theme (System / Light / Dark) and
		// tells us whenever it changes, so the title bar matches.
		dark := arg == themeDark
		if dark {
			h.pageDark = 2
		} else {
			h.pageDark = 1
		}
		setTitleBarDark(h.hwnd, dark)
		refreshFrame(h.hwnd)
	case "title":
		if arg != "" {
			p, _ := syscall.UTF16PtrFromString(arg)
			procSetWindowTextW.Call(h.hwnd, uintptr(unsafe.Pointer(p)))
		}
	}
}

func (h *webHost) saveMainSize() {
	if h.kind != windowKindMain || h.hwnd == 0 || isIconic(h.hwnd) {
		return
	}
	if z, _, _ := procIsZoomed.Call(h.hwnd); z != 0 {
		return
	}
	var wr winRect
	procGetWindowRect.Call(h.hwnd, uintptr(unsafe.Pointer(&wr)))
	_ = saveMainWindowState(savedWindow{X: int(wr.left), Y: int(wr.top), W: int(wr.right - wr.left), H: int(wr.bottom - wr.top), DPI: dpiForWindow(h.hwnd)})
}

type minMaxInfo struct {
	reserved, maxSize, maxPosition, minTrackSize, maxTrackSize winPoint
}

func hostWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	h := host
	if h == nil || hwnd != h.hwnd {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
		return r
	}
	switch msg {
	case wmSize:
		if h.ready {
			h.chromium.Resize()
		}
	case wmMove:
		if h.ready {
			_ = h.chromium.NotifyParentWindowPositionChanged()
		}
	case wmSetFocus:
		if h.ready {
			h.chromium.Focus()
		}
	case wmActivate:
		if wp&0xFFFF == waInactive {
			// Tray popup: close when another window takes focus.
			if h.kind == windowKindTray && h.active {
				procPostMessageW.Call(hwnd, wmClose, 0, 0)
			}
		} else {
			h.active = true
			if h.ready {
				h.chromium.Focus()
			}
		}
	case wmSettingChange:
		if h.pageDark == 0 {
			setTitleBarDark(hwnd, themeIsDark(uiTheme(), windowsAppsUseDark()))
		}
	case wmTimer:
		if wp == hostShowTimerID {
			h.show()
		}
		return 0
	case wmGetMinMaxInfo:
		if h.kind == windowKindMain {
			mmi := (*minMaxInfo)(unsafe.Pointer(lp))
			dpi := dpiForWindow(hwnd)
			mmi.minTrackSize = winPoint{int32(dipToPhysical(480, dpi)), int32(dipToPhysical(360, dpi))}
			return 0
		}
	case wmDpiChanged:
		rc := (*winRect)(unsafe.Pointer(lp))
		procSetWindowPos.Call(hwnd, 0, uintptr(rc.left), uintptr(rc.top), uintptr(rc.right-rc.left), uintptr(rc.bottom-rc.top), swpNoZOrder|swpNoActivate)
		return 0
	case wmExitSizeMove:
		h.saveMainSize()
	case wmClose:
		h.saveMainSize()
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

// ---------------------------------------------------------------------------
// BDDM side: start a helper window, or fall back to Edge.
// ---------------------------------------------------------------------------

var (
	webviewBroken     atomic.Bool
	webviewNoticeOnce sync.Once
	webviewVerMu      sync.Mutex
	webviewVer        string
)

// webview2Ready reports whether the WebView2 runtime is installed and has
// not failed in this run.
func webview2Ready() bool {
	if webviewBroken.Load() {
		return false
	}
	webviewVerMu.Lock()
	defer webviewVerMu.Unlock()
	if webviewVer != "" {
		return true
	}
	v, err := webviewloader.GetInstalledVersion()
	if err != nil {
		log.Printf("WebView2 runtime check: %v", err)
		return false
	}
	webviewVer = v
	if v != "" {
		log.Printf("WebView2 runtime %s", v)
	}
	return v != ""
}

// noteWebView2Missing shows the English runtime notice once per run.
func noteWebView2Missing() {
	webviewNoticeOnce.Do(func() {
		log.Print("WebView2 runtime not found: using an Edge window (sync and sign-in off)")
		go func() {
			text, _ := syscall.UTF16PtrFromString(webview2NoticeText)
			cap, _ := syscall.UTF16PtrFromString("BDDM")
			r, _, _ := procMessageBoxW.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(cap)), mbYesNo|mbIconInfo|mbTopmost|mbSetForeground)
			if r == idYes {
				_ = openURL(webview2RuntimeURL)
			}
		}()
	})
}

// hostRectFor is where a new window opens (physical pixels).
func hostRectFor(kind string, w, h int) windowRect {
	work := primaryWorkArea()
	dpi := primaryDPI()
	if kind == windowKindMain {
		if s, ok := loadMainWindowState(); ok {
			if r, ok := restoreMainRect(s, int(work.left), int(work.top), int(work.right), int(work.bottom), dpi); ok {
				return r
			}
		}
		w, h = mainWindowWidth, mainWindowHeight
	}
	x, y, pw, ph := mainWindowCenteredRect(int(work.left), int(work.top), int(work.right), int(work.bottom), w, h, dpi)
	return windowRect{x, y, pw, ph}
}

func findHostWindow(pid int) uintptr {
	for _, w := range enumTopWindows() {
		if int(w.pid) == pid && w.class == bddmWebViewClass {
			return w.hwnd
		}
	}
	return 0
}

// launchWindowHost starts "BDDM.exe -window ...". If the helper exits with
// an error before its window appears, WebView2 is marked broken for this
// run and fallback opens the Edge window instead.
func launchWindowHost(u, kind string, r windowRect, fallback func() error) error {
	exe := selfExePath()
	if exe == "" {
		return fallback()
	}
	cmd := exec.Command(exe, append(windowHostArgs(u, kind, r), windowHostThemeArgs(uiTheme())...)...)
	var err error
	withForeground(func() { err = cmd.Start() })
	if err != nil {
		log.Printf("window host: %v", err)
		webviewBroken.Store(true)
		return fallback()
	}
	pid := cmd.Process.Pid
	allowSetForegroundWindow(uint32(pid))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	go func() {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case err := <-done:
				if err != nil {
					log.Printf("window host for %s failed: %v; using an Edge window", u, err)
					webviewBroken.Store(true)
					_ = fallback()
				}
				return
			default:
			}
			if hwnd := findHostWindow(pid); hwnd != 0 && isWindowVisible(hwnd) {
				activateWindow(hwnd)
				scheduleTagBDDMAppWindows()
				return
			}
			time.Sleep(80 * time.Millisecond)
		}
	}()
	return nil
}

// closeOldEdgeBDDMWindows closes Edge --app windows left by 1.5.3 (the main
// window and the tray popup), so their profile folders can be removed.
// Only bare app-window titles match; normal browser windows end with the
// browser name and are never touched.
func closeOldEdgeBDDMWindows() int {
	n := 0
	for _, w := range enumTopWindows() {
		if !isChromeWidgetClass(w.class) || !isBrowserAppExe(processExe(w.pid)) {
			continue
		}
		if w.title == "BDDM - Download Manager" || w.title == "BDDM tray" {
			procPostMessageW.Call(w.hwnd, wmClose, 0, 0)
			n++
		}
	}
	return n
}

// prepareWindowHosts runs once at startup (bug 76).
func prepareWindowHosts() {
	closed := closeOldEdgeBDDMWindows()
	go func() {
		if closed > 0 {
			time.Sleep(3 * time.Second)
		}
		if n := cleanupOldChromiumProfiles(); n > 0 {
			log.Printf("Removed %d old Edge profile folder(s)", n)
		}
	}()
	if !webview2Ready() {
		noteWebView2Missing()
	}
}
