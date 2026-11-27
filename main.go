// BDDM — a download manager with a window.
//
// Run it with:  go run .
// Or build it:  go build -ldflags="-H=windowsgui" -o BDDM.exe
//
// It starts a small local web server on 127.0.0.1:9614 and opens the window
// (an Edge or Chrome "app" window). Closing the window does not stop the
// downloads; use the Quit button in the window to stop BDDM.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

const port = "9614"

// appVersion is the BDDM version shown in Settings and on the update notice.
const appVersion = "1.5.6"

func main() {
	runtime.LockOSThread()
	noWindow := flag.Bool("nowindow", false, "do not open the window (run in the background)")
	quitRunning := flag.Bool("quit", false, "stop a running BDDM (downloads are paused and saved) and exit")
	registerOnly := flag.Bool("register", false, "update this install's Windows shortcuts and notifications, then exit")
	windowURL := flag.String("window", "", "internal: show one BDDM page in a WebView2 window")
	windowKind := flag.String("window-kind", windowKindPage, "internal: main, page, popup or tray")
	windowRectArg := flag.String("window-rect", "", "internal: x,y,w,h in physical pixels")
	windowTheme := flag.String("window-theme", themeSystem, "internal: system, light or dark")
	devtools := flag.Bool("devtools", false, "debug: enable WebView2 DevTools (or set BDDM_DEVTOOLS=1)")
	wv2test := flag.Bool("wv2test", false, "debug: show red WebView2 test page (or set BDDM_WV2_TEST=1)")
	updateTest := flag.String("updatetest", "", "debug: test the updater with this release tag (or set BDDM_UPDATE_TEST_TAG)")
	flag.Parse()
	if *devtools {
		setWebViewDevTools(true)
	}
	if *wv2test {
		setWebViewTestMode(true)
	}
	if *updateTest != "" {
		setUpdateTestTag(*updateTest)
	}
	if *windowURL != "" { // helper process for one BDDM window (bug 76)
		setupWindowHostLog()
		currentTheme.Store(normalizeTheme(*windowTheme))
		os.Exit(runWindowHost(*windowURL, *windowKind, *windowRectArg))
	}
	appURL := "http://127.0.0.1:" + port
	if *registerOnly { // used by the installer after it creates shortcuts
		if err := registerAppIdentity(); err != nil {
			os.Exit(1)
		}
		return
	}
	if *quitRunning { // used by the installer before it replaces BDDM.exe
		stopRunning(appURL)
		return
	}

	configDir := filepath.Join(".", "BDDM-data")
	if d, err := os.UserConfigDir(); err == nil { // %APPDATA% on Windows
		configDir = filepath.Join(d, "BDDM")
	}
	_ = os.MkdirAll(configDir, 0755)
	setupLog(configDir)
	if tag := updateTestTag(); tag != "" {
		log.Printf("update test mode: using GitHub tag %s instead of /releases/latest", tag)
	}
	setCurrentProcessAppUserModelID()
	if err := registerAppIdentity(); err != nil {
		log.Printf("Windows app identity: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		// Probably BDDM is already running: send it the link (if any) and show its window.
		if isBDDMRunning(appURL) {
			for _, u := range flag.Args() {
				sendToRunning(appURL, u)
			}
			log.Print("BDDM is already running.")
			if !*noWindow {
				_ = openAppWindow(appURL)
			}
			return
		}
		msg := fmt.Sprintf("BDDM cannot start: port %s is used by another program (%v)", port, err)
		log.Print(msg)
		notify("BDDM", msg)
		os.Exit(1)
	}

	m, err := NewManager(configDir, notify)
	if err != nil {
		log.Fatalf("cannot start: %v", err)
	}

	go m.tools.AutoUpdate() // keep yt-dlp fresh (at most once a day)

	upd := newUpdater(appVersion,
		func() bool { return m.Settings().CheckUpdates },
		func() bool { return m.Settings().Notifications },
		notify)
	upd.dir = configDir
	upd.running = m.RunningCount
	// Stop transfers, close every BDDM window and -window helper, then
	// start the silent setup. The process exits after setup is launched
	// so the installer can copy BDDM.exe. /run reopens the new version.
	upd.prepare = m.Shutdown
	upd.closeWindows = closeBDDMWindowHosts
	upd.runSetup = runSilentSetup
	upd.Start()

	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	stopped := make(chan struct{})
	var once sync.Once
	quit := func() {
		once.Do(func() {
			log.Print("Stopping: pausing downloads and saving...")
			closeBDDMWindowHosts()
			stopTaskbar()
			stopTray()
			upd.Stop()
			m.Shutdown()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(ctx)
			close(stopped)
		})
	}
	upd.quitFn = quit
	srv.Handler = newServer(m, port, quit, upd)

	// Interrupt also stops cleanly when BDDM is run from a terminal (go run).
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		quit()
	}()

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("server error: %v", err)
			quit()
		}
	}()

	for _, u := range flag.Args() {
		if isMagnet(u) || looksLikeTorrentPath(u) {
			var job JobView
			var err error
			if isMagnet(u) {
				job, err = m.AddTorrentMagnet(u)
			} else {
				job, err = m.AddTorrentFile(u, nil)
			}
			if err != nil {
				log.Printf("could not add %s: %v", u, err)
				continue
			}
			_ = openAppWindowSize("http://127.0.0.1:"+port+"/torrent.html?id="+job.ID, 560, 620)
			continue
		}
		if _, err := m.Add(AddRequest{URL: u, Force: true}); err != nil {
			log.Printf("could not add %s: %v", u, err)
		}
	}

	prepareWindowHosts()
	startTray(port, quit, m)
	startTaskbar(m)

	log.Print("BDDM is running at " + appURL)
	log.Print("Data folder: " + configDir)
	log.Print("Close the window any time; use the Quit button in the window to stop BDDM.")
	if !*noWindow {
		if err := openAppWindow(appURL); err != nil {
			log.Printf("Could not open the window: %v", err)
			log.Print("Open " + appURL + " in your browser.")
		}
	}

	<-stopped
	log.Print("BDDM stopped.")
}

// setupLog writes the log to bddm.log (and to the console when there is one).
func setupLog(configDir string) {
	f, err := os.OpenFile(filepath.Join(configDir, "bddm.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return
	}
	// The file comes first: with -H=windowsgui there is no console and
	// writing to it fails, which would stop MultiWriter.
	log.SetOutput(io.MultiWriter(f, consoleWriter{}))
}

type consoleWriter struct{}

func (consoleWriter) Write(p []byte) (int, error) {
	_, _ = os.Stderr.Write(p)
	return len(p), nil
}

// installerHelperTimeout is how long BDDM.exe -quit may block. The NSIS
// installer/uninstaller also wraps the process in a wait-with-kill so a
// hung helper cannot freeze Setup.
var installerHelperTimeout = 8 * time.Second

// stopRunning asks a running BDDM to quit and waits until it has saved its
// downloads and closed. It never opens a window or starts WebView2, and it
// always returns within installerHelperTimeout so the installer cannot hang.
func stopRunning(appURL string) {
	stopRunningAt(appURL, "127.0.0.1:"+port, installerHelperTimeout)
}

func stopRunningAt(appURL, addr string, timeout time.Duration) {
	if timeout <= 0 {
		timeout = installerHelperTimeout
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		running := isBDDMRunning(appURL)
		if !running && !tcpPortOpen(addr, 300*time.Millisecond) {
			return
		}
		if running {
			c := http.Client{Timeout: 3 * time.Second}
			if resp, err := c.Post(appURL+"/api/quit", "application/json", nil); err == nil {
				resp.Body.Close()
			}
		}
		_ = waitForClosedPort(addr, timeout, 0)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// tcpPortOpen is true when something is accepting connections on addr.
func tcpPortOpen(addr string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// waitForClosedPort waits until addr is no longer accepting connections.
// linger is a short extra wait after it first looks closed, so a process
// that has just shut its listener can finish exiting.
func waitForClosedPort(addr string, timeout, linger time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !tcpPortOpen(addr, 300*time.Millisecond) {
			if linger > 0 {
				time.Sleep(linger)
			}
			return true
		}
		if time.Now().After(deadline) {
			return !tcpPortOpen(addr, 300*time.Millisecond)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func isBDDMRunning(appURL string) bool {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(appURL + "/api/ping")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var v map[string]string
	return json.NewDecoder(resp.Body).Decode(&v) == nil && v["app"] == "BDDM"
}

func sendToRunning(appURL, link string) {
	body, _ := json.Marshal(AddRequest{URL: link, Force: true})
	resp, err := http.Post(appURL+"/api/add", "application/json", bytes.NewReader(body))
	if err == nil {
		resp.Body.Close()
	}
}

// setupWindowHostLog sends a window helper's log to window-host.log, so it
// never truncates bddm.log of the running BDDM.
func setupWindowHostLog() {
	dir := webviewLogDir()
	_ = os.MkdirAll(dir, 0755)
	f, err := os.OpenFile(webviewLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.SetOutput(io.Discard)
		return
	}
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(f)
}
