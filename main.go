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
const appVersion = "1.4.10"

func main() {
	noWindow := flag.Bool("nowindow", false, "do not open the window (run in the background)")
	quitRunning := flag.Bool("quit", false, "stop a running BDDM (downloads are paused and saved) and exit")
	flag.Parse()
	appURL := "http://127.0.0.1:" + port
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

	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		// Probably BDDM is already running: send it the link (if any) and show its window.
		if isBDDMRunning(appURL) {
			for _, u := range flag.Args() {
				sendToRunning(appURL, u)
			}
			fmt.Println("BDDM is already running.")
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
	upd.prepare = m.PauseAllRunning
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

	// Ctrl+C in the terminal also stops cleanly.
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

	fmt.Println("BDDM is running at " + appURL)
	fmt.Println("Data folder: " + configDir)
	fmt.Println("Close the window any time; use the Quit button (or Ctrl+C here) to stop BDDM.")
	if !*noWindow {
		if err := openAppWindow(appURL); err != nil {
			fmt.Println("Could not open the window:", err)
			fmt.Println("Open " + appURL + " in your browser.")
		}
	}

	<-stopped
	fmt.Println("BDDM stopped.")
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

// stopRunning asks a running BDDM to quit and waits (up to 15 s) until it has
// saved its downloads and closed.
func stopRunning(appURL string) {
	if !isBDDMRunning(appURL) {
		return
	}
	c := http.Client{Timeout: 3 * time.Second}
	if resp, err := c.Post(appURL+"/api/quit", "application/json", nil); err == nil {
		resp.Body.Close()
	}
	for i := 0; i < 60; i++ {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 300*time.Millisecond)
		if err != nil {
			time.Sleep(500 * time.Millisecond) // the process exits right after closing the port
			return
		}
		conn.Close()
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
