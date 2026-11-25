//go:build windows

package main

import (
	"log"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/pkg/edge"
	"github.com/jchv/go-webview2/webviewloader"
)

const (
	wsOverlappedWindow = 0x00CF0000
	wsPopup            = 0x80000000
	wsBorder           = 0x00800000
	wsClipChildren     = 0x02000000
	wsExToolWindow     = 0x00000080
	wsExTopmost        = 0x00000008

	wmActivate      = 0x0006
	wmClose         = 0x0010
	wmGetMinMaxInfo = 0x0024
	wmExitSizeMove  = 0x0232
	wmSettingChange = 0x001A
	wmDpiChanged    = 0x02E0
	wmSetIcon       = 0x0080
	waInactive      = 0

	gwlStyle   = ^uintptr(15) // GWL_STYLE (-16)
	gwlExStyle = ^uintptr(19) // GWL_EXSTYLE (-20)
	gwlpProc   = ^uintptr(3)  // GWLP_WNDPROC (-4)

	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
	swpFrameChanged = 0x0020
	swpShowWindow   = 0x0040
	swpHideWindow   = 0x0080

	wmKeyDown         = 0x0100
	wmSize            = 0x0005
	wmEraseBkgnd      = 0x0014
	vkEscape          = 0x1B
	swHide            = 0
	whCBT             = 5
	hcbtCreateWnd     = 3           // HCBT_CREATEWND (0 is HCBT_MOVESIZE; there is no HCBT_SHOWWINDOW)
	gclpHbrBackground = ^uintptr(9) // GCLP_HBRBACKGROUND (-10)
	wsVisible         = 0x10000000
	wsExLayered       = 0x00080000
	lwaAlpha          = 0x00000002
	offscreenPos      = -32000

	mbYesNo         = 0x00000004
	mbIconInfo      = 0x00000040
	mbTopmost       = 0x00040000
	mbSetForeground = 0x00010000
	idYes           = 6
	coinitApartment = 0x2
)

var (
	procExtractIconExW             = shell32.NewProc("ExtractIconExW")
	procSetWindowTextW             = user32.NewProc("SetWindowTextW")
	procIsZoomed                   = user32.NewProc("IsZoomed")
	procSendMessageW               = user32.NewProc("SendMessageW")
	procMessageBoxW                = user32.NewProc("MessageBoxW")
	procGetWindowLongPtr           = user32.NewProc("GetWindowLongPtrW")
	procGetWindowLong              = user32.NewProc("GetWindowLongW")
	procSetWindowLongPtr           = user32.NewProc("SetWindowLongPtrW")
	procSetWindowLong              = user32.NewProc("SetWindowLongW")
	procCallWindowProcW            = user32.NewProc("CallWindowProcW")
	procSetWindowsHookExW          = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx        = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx             = user32.NewProc("CallNextHookEx")
	procSetClassLongPtr            = user32.NewProc("SetClassLongPtrW")
	procSetClassLong               = user32.NewProc("SetClassLongW")
	procCreateSolidBrush           = syscall.NewLazyDLL("gdi32.dll").NewProc("CreateSolidBrush")
	procFillRect                   = user32.NewProc("FillRect")
	procSetLayeredWindowAttributes = user32.NewProc("SetLayeredWindowAttributes")

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

func getWindowLongPtr(hwnd, idx uintptr) uintptr {
	if procGetWindowLongPtr.Find() == nil {
		r, _, _ := procGetWindowLongPtr.Call(hwnd, idx)
		return r
	}
	r, _, _ := procGetWindowLong.Call(hwnd, idx)
	return r
}

func setWindowLongPtr(hwnd, idx, val uintptr) uintptr {
	if procSetWindowLongPtr.Find() == nil {
		r, _, _ := procSetWindowLongPtr.Call(hwnd, idx, val)
		return r
	}
	r, _, _ := procSetWindowLong.Call(hwnd, idx, val)
	return r
}

func setClassLongPtr(hwnd, idx, val uintptr) uintptr {
	if procSetClassLongPtr.Find() == nil {
		r, _, _ := procSetClassLongPtr.Call(hwnd, idx, val)
		return r
	}
	r, _, _ := procSetClassLong.Call(hwnd, idx, val)
	return r
}

// createStructW is CREATESTRUCTW on amd64 (pointer-aligned after style).
type createStructW struct {
	lpCreateParams uintptr
	hInstance      uintptr
	hMenu          uintptr
	hwndParent     uintptr
	cy, cx, y, x   int32
	style          int32
	_pad           int32
	lpszName       uintptr
	lpszClass      uintptr
	dwExStyle      uint32
}

type cbtCreateWnd struct {
	lpcs            uintptr
	hwndInsertAfter uintptr
}

var hideWebViewCreateHookCB = syscall.NewCallback(hideWebViewCreateHook)

// hideWebViewCreateHook is a WH_CBT hook. There is no HCBT_SHOWWINDOW; the
// previous hook used nCode 0 (HCBT_MOVESIZE) and never stopped ShowWindow,
// which is why a white overlapped window still flashed, then appeared to
// "close" when we hid it. On HCBT_CREATEWND we move the host HWND off-screen
// and give it WS_EX_LAYERED so NewWithOptions's ShowWindow is invisible.
func hideWebViewCreateHook(nCode, wParam, lParam uintptr) uintptr {
	if int32(nCode) == hcbtCreateWnd && lParam != 0 {
		cbt := (*cbtCreateWnd)(unsafe.Pointer(lParam))
		if cbt.lpcs != 0 {
			cs := (*createStructW)(unsafe.Pointer(cbt.lpcs))
			class := classNameFromPtr(cs.lpszClass)
			if class == "" {
				class = windowClassName(wParam)
			}
			if class == goWebView2Class || class == bddmWebViewClass {
				cs.x = offscreenPos
				cs.y = offscreenPos
				cs.style &^= int32(wsVisible)
				cs.dwExStyle |= uint32(wsExLayered)
			}
		}
	}
	r, _, _ := procCallNextHookEx.Call(0, nCode, wParam, lParam)
	return r
}

func classNameFromPtr(p uintptr) string {
	if p == 0 || p < 0x10000 { // MAKEINTATOM
		return ""
	}
	return windowsUTF16PtrToString(p)
}

func windowsUTF16PtrToString(p uintptr) string {
	u := (*[256]uint16)(unsafe.Pointer(p))
	n := 0
	for n < len(u) && u[n] != 0 {
		n++
	}
	return syscall.UTF16ToString(u[:n])
}

func windowClassName(hwnd uintptr) string {
	var buf [64]uint16
	procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), 64)
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	return syscall.UTF16ToString(buf[:n])
}

func setWindowAlpha(hwnd uintptr, alpha byte) {
	if hwnd == 0 {
		return
	}
	ex := getWindowLongPtr(hwnd, gwlExStyle)
	if ex&wsExLayered == 0 {
		setWindowLongPtr(hwnd, gwlExStyle, ex|wsExLayered)
	}
	procSetLayeredWindowAttributes.Call(hwnd, 0, uintptr(alpha), lwaAlpha)
}

// newWebViewHidden runs NewWithOptions with a thread-local WH_CBT hook so the
// host HWND is created off-screen and layered (alpha 0). ShowWindow inside
// go-webview2 then cannot flash a white window.
func newWebViewHidden(create func() webview2.WebView) webview2.WebView {
	tid, _, _ := procGetCurrentThreadId.Call()
	hook, _, err := procSetWindowsHookExW.Call(whCBT, hideWebViewCreateHookCB, 0, tid)
	if hook == 0 {
		log.Printf("window host: WH_CBT hook failed: %v", err)
	} else {
		defer procUnhookWindowsHookEx.Call(hook)
	}
	w := create()
	if w != nil {
		if hwnd := uintptr(w.Window()); hwnd != 0 {
			setWindowAlpha(hwnd, 0)
			ox, oy := int32(offscreenPos), int32(offscreenPos)
			procSetWindowPos.Call(hwnd, 0, uintptr(uint32(ox)), uintptr(uint32(oy)), 0, 0, swpNoZOrder|swpNoActivate|swpNosize|swpHideWindow)
			showWindow(hwnd, swHide)
		}
	}
	return w
}

func setWebViewDefaultBackground(w webview2.WebView, dark bool) {
	c := webviewChromium(w)
	if c == nil {
		return
	}
	ctrl := c.GetController()
	if ctrl == nil {
		return
	}
	c2 := ctrl.GetICoreWebView2Controller2()
	if c2 == nil {
		return
	}
	r, g, b := themeBgRGB(dark)
	if err := c2.PutDefaultBackgroundColor(edge.COREWEBVIEW2_COLOR{A: 255, R: r, G: g, B: b}); err != nil {
		log.Printf("window host: PutDefaultBackgroundColor: %v", err)
	}
}

// webviewChromium returns the go-webview2 Chromium controller, if present,
// so we can call PutIsVisible and Resize after showing a previously hidden HWND.
func webviewChromium(w webview2.WebView) *edge.Chromium {
	if w == nil {
		return nil
	}
	v := reflect.ValueOf(w)
	if v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	f := v.FieldByName("browser")
	if !f.IsValid() {
		return nil
	}
	if !f.CanInterface() {
		if !f.CanAddr() {
			return nil
		}
		f = reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
	}
	c, _ := f.Interface().(*edge.Chromium)
	return c
}

// ---------------------------------------------------------------------------
// Helper process: one WebView2 window via go-webview2 NewWithOptions.
// ---------------------------------------------------------------------------

type webHost struct {
	hwnd        uintptr
	kind        string
	url         string
	wv          webview2.WebView
	origWndProc uintptr
	ready       bool
	active      bool // has been the active window at least once
	pageDark    int  // 0 = page has not said yet, 1 = light, 2 = dark
	domReady    bool
	painted     bool
	shown       bool
	shownAt     time.Time
	bgBrush     uintptr
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

	dataDir := webviewDataDir()
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Printf("window host: mkdir %s: %v", dataDir, err)
	}
	devtools := webviewDevToolsEnabled()
	wv2test := webviewTestModeEnabled()
	dark := themeIsDark(uiTheme(), windowsAppsUseDark())
	// Must be set before the WebView2 environment is created (NewWithOptions).
	_ = os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", webviewBackgroundARGB(dark))
	ver, verErr := webviewloader.GetInstalledVersion()
	log.Printf("window host: stage=1_start version=%s kind=%s url=%s rect=%s data=%s devtools=%v wv2test=%v webview2=%q err=%v bg=%s",
		appVersion, kind, u, r, dataDir, devtools, wv2test, ver, verErr, webviewBackgroundARGB(dark))

	title := windowTitleForURL(u)
	log.Printf("window host: stage=2_new_with_options Debug=%v DataPath=%s hide=%v", devtools, dataDir, kind == windowKindTray)
	create := func() webview2.WebView {
		return webview2.NewWithOptions(webview2.WebViewOptions{
			Debug:     devtools, // false by default in release builds; true only with BDDM_DEVTOOLS=1 / -devtools
			DataPath:  dataDir,
			AutoFocus: true,
			WindowOptions: webview2.WindowOptions{
				Title:  title,
				Width:  uint(r.W),
				Height: uint(r.H),
				IconId: 1,
				Center: false,
			},
		})
	}
	// The tray popup must not flash a blank overlapped window: go-webview2
	// calls ShowWindow inside NewWithOptions, so we swallow that show (and
	// keep the HWND hidden/offscreen) until the first paint / DOM ready.
	var w webview2.WebView
	if kind == windowKindTray {
		w = newWebViewHidden(create)
	} else {
		w = create()
	}
	if w == nil || uintptr(w.Window()) == 0 {
		log.Printf("window host: stage=2_new_with_options failed for %s (%s)", u, kind)
		return 4
	}

	hwnd := uintptr(w.Window())
	host = &webHost{
		hwnd: hwnd,
		kind: kind,
		url:  u,
		wv:   w,
	}
	setWebViewDefaultBackground(w, dark)
	host.configureWindow(r)
	var cr winRect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&cr)))
	log.Printf("window host: stage=3_window_ready hwnd=0x%x client=(%d,%d,%d,%d)", hwnd, cr.left, cr.top, cr.right, cr.bottom)

	host.bindAndInit()
	log.Printf("window host: stage=4_init_bindings")

	if wv2test {
		log.Printf("window host: stage=5_wv2_test calling SetHtml (red test)")
		w.SetHtml(webviewTestHTML)
	} else {
		ready := waitForServerReady(u, 3*time.Second)
		log.Printf("window host: stage=5_navigate serverReady=%v url=%s", ready, u)
		w.Navigate(u)
	}

	if kind != windowKindTray {
		host.ready = true
	} else {
		// Do not reveal a still-white surface. Only a last-resort timeout,
		// after DefaultBackgroundColor is already the theme colour.
		time.AfterFunc(5*time.Second, func() {
			if host != nil && host.wv != nil {
				host.wv.Dispatch(func() {
					log.Printf("window host: stage=7c_reveal_timeout kind=%s painted=%v", host.kind, host.painted)
					host.revealIfNeeded()
				})
			}
		})
	}
	log.Printf("window host: stage=6_run_loop")
	w.Run()
	log.Printf("window host: stage=8_closed domReady=%v", host.domReady)
	return 0
}

func (h *webHost) configureWindow(r windowRect) {
	hwnd := h.hwnd
	dark := themeIsDark(uiTheme(), windowsAppsUseDark())
	setTitleBarDark(hwnd, dark)

	if exe := selfExePath(); exe != "" {
		var big, small uintptr
		p, _ := syscall.UTF16PtrFromString(exe)
		procExtractIconExW.Call(uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&big)), uintptr(unsafe.Pointer(&small)), 1)
		if big != 0 {
			procSendMessageW.Call(hwnd, wmSetIcon, 1, big)
		}
		if small != 0 {
			procSendMessageW.Call(hwnd, wmSetIcon, 0, small)
		}
	}

	if h.kind == windowKindTray {
		// Keep hidden until DOM ready (no SWP_SHOWWINDOW). Position at the
		// final tray rectangle so the first show is already the right size.
		showWindow(hwnd, swHide)
		setWindowLongPtr(hwnd, gwlStyle, wsPopup|wsBorder|wsClipChildren)
		setWindowLongPtr(hwnd, gwlExStyle, wsExToolWindow|wsExTopmost|wsExLayered)
		procSetWindowPos.Call(hwnd, hwndTopmost, uintptr(int32(r.X)), uintptr(int32(r.Y)), uintptr(int32(r.W)), uintptr(int32(r.H)), swpFrameChanged|swpHideWindow|swpNoActivate)
		setWindowAlpha(hwnd, 0)
		h.setBackgroundBrush(dark)
	} else {
		st := getWindowLongPtr(hwnd, gwlStyle)
		setWindowLongPtr(hwnd, gwlStyle, st|wsClipChildren)
		procSetWindowPos.Call(hwnd, 0, uintptr(int32(r.X)), uintptr(int32(r.Y)), uintptr(int32(r.W)), uintptr(int32(r.H)), swpNoZOrder|swpFrameChanged|swpShowWindow)
	}

	// Subclass NewWithOptions's wndproc for BDDM sizing, theme, DPI and tray blur,
	// chaining all other messages (including WM_SIZE/WM_DESTROY) to go-webview2.
	h.origWndProc = setWindowLongPtr(hwnd, gwlpProc, syscall.NewCallback(hostSubclassProc))
	if h.kind != windowKindTray {
		setForegroundWindow(hwnd)
	}
}

func (h *webHost) bindAndInit() {
	_ = h.wv.Bind("bddmHostClose", func() {
		procPostMessageW.Call(h.hwnd, wmClose, 0, 0)
	})
	_ = h.wv.Bind("bddmHostTitle", func(title string) {
		if title == "" {
			return
		}
		if len(title) > 200 {
			title = title[:200]
		}
		p, _ := syscall.UTF16PtrFromString(title)
		procSetWindowTextW.Call(h.hwnd, uintptr(unsafe.Pointer(p)))
	})
	_ = h.wv.Bind("bddmHostTheme", func(eff string) {
		dark := eff == themeDark
		if dark {
			h.pageDark = 2
		} else {
			h.pageDark = 1
		}
		setTitleBarDark(h.hwnd, dark)
		refreshFrame(h.hwnd)
	})
	_ = h.wv.Bind("bddmHostStage", func(stage string) {
		if stage == "dom_ready" && !h.domReady {
			h.domReady = true
			log.Printf("window host: stage=7_dom_ready kind=%s", h.kind)
		}
		if stage == "painted" && !h.painted {
			h.painted = true
			log.Printf("window host: stage=7a_painted kind=%s", h.kind)
			if h.kind == windowKindTray {
				h.wv.Dispatch(func() { h.revealIfNeeded() })
			}
		}
	})
	h.wv.Init(webviewInitScript)
}

func (h *webHost) setBackgroundBrush(dark bool) {
	brush, _, _ := procCreateSolidBrush.Call(uintptr(themeBgColorRef(dark)))
	if brush == 0 {
		return
	}
	setClassLongPtr(h.hwnd, gclpHbrBackground, brush)
	h.bgBrush = brush
}

// revealIfNeeded shows a hidden tray popup after the first paint, then calls
// put_IsVisible(TRUE) and updates controller bounds (the test1 fix).
func (h *webHost) revealIfNeeded() {
	if h == nil || h.shown || h.hwnd == 0 {
		return
	}
	h.shown = true
	h.shownAt = time.Now()
	h.ready = true
	showWindow(h.hwnd, swShow)
	if c := webviewChromium(h.wv); c != nil {
		_ = c.Show()
		c.Resize()
	} else {
		procSendMessageW.Call(h.hwnd, wmSize, 0, 0)
	}
	procSetWindowPos.Call(h.hwnd, hwndTopmost, 0, 0, 0, 0, swpNomove|swpNosize|swpShowWindow)
	setWindowAlpha(h.hwnd, 255)
	setForegroundWindow(h.hwnd)
	h.active = true
	var cr winRect
	procGetClientRect.Call(h.hwnd, uintptr(unsafe.Pointer(&cr)))
	log.Printf("window host: stage=7b_revealed kind=%s painted=%v client=(%d,%d,%d,%d)", h.kind, h.painted, cr.left, cr.top, cr.right, cr.bottom)
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

func hostSubclassProc(hwnd, msg, wp, lp uintptr) uintptr {
	h := host
	if h == nil || hwnd != h.hwnd || h.origWndProc == 0 {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
		return r
	}
	switch msg {
	case wmSettingChange:
		if h.pageDark == 0 {
			setTitleBarDark(hwnd, themeIsDark(uiTheme(), windowsAppsUseDark()))
		}
	case wmEraseBkgnd:
		if h.kind == windowKindTray && h.bgBrush != 0 && wp != 0 {
			var rc winRect
			procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
			procFillRect.Call(wp, uintptr(unsafe.Pointer(&rc)), h.bgBrush)
			return 1
		}
	case wmKeyDown:
		if h.kind == windowKindTray && wp == vkEscape {
			procPostMessageW.Call(hwnd, wmClose, 0, 0)
			return 0
		}
	case wmActivate:
		if h.kind == windowKindTray {
			if wp&0xFFFF == waInactive {
				// Close on blur only after the popup has been revealed, and
				// skip the brief focus dance right after ShowWindow.
				if h.shown && time.Since(h.shownAt) > 150*time.Millisecond {
					procPostMessageW.Call(hwnd, wmClose, 0, 0)
				}
			} else if h.shown {
				h.active = true
			}
			break
		}
		if wp&0xFFFF != waInactive && h.ready {
			h.active = true
		}
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
	}
	r, _, _ := procCallWindowProcW.Call(h.origWndProc, hwnd, msg, wp, lp)
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
		if int(w.pid) == pid && (w.class == goWebView2Class || w.class == bddmWebViewClass) {
			return w.hwnd
		}
	}
	return 0
}

var (
	windowHostMu    sync.Mutex
	windowHostProcs = map[int]*os.Process{}
)

func rememberWindowHost(p *os.Process) {
	if p == nil {
		return
	}
	windowHostMu.Lock()
	windowHostProcs[p.Pid] = p
	windowHostMu.Unlock()
}

func forgetWindowHost(pid int) {
	windowHostMu.Lock()
	delete(windowHostProcs, pid)
	windowHostMu.Unlock()
}

func snapshotWindowHosts() []*os.Process {
	windowHostMu.Lock()
	defer windowHostMu.Unlock()
	out := make([]*os.Process, 0, len(windowHostProcs))
	for _, p := range windowHostProcs {
		out = append(out, p)
	}
	return out
}

const windowHostCloseWait = 3 * time.Second

// closeBDDMWindowHosts closes every BDDM window (main, Settings, tray popup)
// and any leftover Edge --app windows, waits briefly for the -window helper
// processes to exit, then force-kills helpers that are still running.
// The engine process itself is not killed.
func closeBDDMWindowHosts() {
	closeOldEdgeBDDMWindows()
	self := os.Getpid()
	for _, w := range listBDDMAppWindows() {
		procPostMessageW.Call(w.hwnd, wmClose, 0, 0)
	}
	deadline := time.Now().Add(windowHostCloseWait)
	for time.Now().Before(deadline) {
		left := false
		for _, w := range listBDDMAppWindows() {
			if int(w.pid) != self {
				left = true
				break
			}
		}
		if !left {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, p := range snapshotWindowHosts() {
		if p == nil || p.Pid == self {
			continue
		}
		_ = p.Kill()
		forgetWindowHost(p.Pid)
	}
}

// launchWindowHost starts "BDDM.exe -window ...". If the helper exits with
// an error before its window appears, WebView2 is marked broken for this
// run and fallback opens the Edge window instead.
func launchWindowHost(u, kind string, r windowRect, fallback func() error) error {
	exe := selfExePath()
	if exe == "" {
		if kind == windowKindTray {
			noteTrayPopupLaunchFailed()
		}
		return fallback()
	}
	args := append(windowHostArgs(u, kind, r), windowHostThemeArgs(uiTheme())...)
	if webviewDevToolsEnabled() {
		args = append(args, "-devtools")
	}
	if webviewTestModeEnabled() {
		args = append(args, "-wv2test")
	}
	cmd := exec.Command(exe, args...)
	var err error
	withForeground(func() { err = cmd.Start() })
	if err != nil {
		log.Printf("window host: %v", err)
		if kind == windowKindTray {
			noteTrayPopupLaunchFailed()
		}
		webviewBroken.Store(true)
		return fallback()
	}
	pid := cmd.Process.Pid
	rememberWindowHost(cmd.Process)
	allowSetForegroundWindow(uint32(pid))
	done := make(chan error, 1)
	go func() {
		waitErr := cmd.Wait()
		forgetWindowHost(pid)
		if kind == windowKindTray {
			noteTrayPopupExited()
		}
		done <- waitErr
	}()
	go func() {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case err := <-done:
				if err != nil {
					log.Printf("window host for %s failed: %v (see %s); using an Edge window", u, err, webviewLogPath())
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
