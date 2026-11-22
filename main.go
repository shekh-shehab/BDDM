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
	"sync"
	"time"
)

const port = "9614"

// appVersion is the BDDM version shown in Settings and on the update notice.
const appVersion = "1.4.18"

func main() {
	noWindow := flag.Bool("nowindow", false, "do not open the window (run in the background)")
	quitRunning := flag.Bool("quit", false, "stop a running BDDM (downloads are paused and saved) and exit")
	registerOnly := flag.Bool("register", false, "update this install's Windows shortcuts and notifications, then exit")
	flag.Parse()
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
	// Stop transfers and save before the installer starts, so torrents
	// that were running can resume and Windows does not have to force-kill BDDM.
	upd.prepare = m.Shutdown
	upd.runSetup = runSilentSetup
	upd.Start()

	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	stopped := make(chan struct{})
	var once sync.Once
	quit := func() {
		once.Do(func() {
			log.Print("Stopping: pausing downloads and saving...")
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

// stopRunning asks a running BDDM to quit and waits until it has saved its
// downloads and closed. If BDDM is already shutting down (the HTTP API may
// not answer), it still waits for the port to close so the installer does
// not force-kill the process mid-save.
func stopRunning(appURL string) {
	addr := "127.0.0.1:" + port
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
	_ = waitForClosedPort(addr, 20*time.Second, 500*time.Millisecond)
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
