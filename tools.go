package main

// tools.go: the helper programs BDDM uses for videos. yt-dlp finds and
// downloads videos, ffmpeg joins video+audio and makes MP3s, deno lets
// yt-dlp solve YouTube's JavaScript checks. They live in
// %APPDATA%\BDDM\tools and can be downloaded from the app with one click.

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type toolSource struct {
	name  string   // yt-dlp, ffmpeg, deno
	urls  []string // tried in order
	inZip []string // files to take out of the zip ("" = the download is the program itself)
	size  int64    // rough size for the progress bar until the server tells us
}

func toolSources() []toolSource {
	switch runtime.GOOS {
	case "windows":
		return []toolSource{
			{name: "yt-dlp", urls: []string{"https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp.exe"}, size: 24 << 20},
			{name: "ffmpeg", urls: []string{
				"https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip",
				"https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-lgpl.zip",
			}, inZip: []string{"ffmpeg.exe", "ffprobe.exe"}, size: 115 << 20},
			{name: "deno", urls: []string{"https://github.com/denoland/deno/releases/latest/download/deno-x86_64-pc-windows-msvc.zip"},
				inZip: []string{"deno.exe"}, size: 43 << 20},
			// gallery-dl lists the photos, carousels and videos of profiles
			// (profile.go).
			{name: "gallery-dl", urls: []string{"https://github.com/mikf/gallery-dl/releases/latest/download/gallery-dl.exe"}, size: 14 << 20},
		}
	case "linux":
		return []toolSource{
			{name: "yt-dlp", urls: []string{"https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_linux"}, size: 35 << 20},
			{name: "deno", urls: []string{"https://github.com/denoland/deno/releases/latest/download/deno-x86_64-unknown-linux-gnu.zip"},
				inZip: []string{"deno"}, size: 45 << 20},
			{name: "gallery-dl", urls: []string{"https://github.com/mikf/gallery-dl/releases/latest/download/gallery-dl.bin"}, size: 16 << 20},
		}
	}
	return nil
}

type toolProgress struct {
	Name  string `json:"name"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
	State string `json:"state"` // waiting, downloading, done, error
}

// Tools finds and installs the helper programs.
type Tools struct {
	dir    string
	client *http.Client

	mu         sync.Mutex
	installing bool
	progress   []*toolProgress
	lastErr    string
	waiters    []func() // run when an install finishes
	updateMsg  string
}

func newTools(configDir string, client *http.Client) *Tools {
	t := &Tools{dir: filepath.Join(configDir, "tools"), client: client}
	_ = os.MkdirAll(t.dir, 0755)
	return t
}

// Find returns the path of a tool: our tools folder first, then PATH.
func (t *Tools) Find(name string) string {
	p := filepath.Join(t.dir, name+exeSuffix)
	if fileExists(p) {
		return p
	}
	if name == "yt-dlp" && runtime.GOOS != "windows" {
		if p := filepath.Join(t.dir, "yt-dlp_linux"); fileExists(p) {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// Ready reports whether videos can be downloaded.
func (t *Tools) Ready() bool {
	return t.Find("yt-dlp") != "" && t.Find("ffmpeg") != "" && t.Find("ffprobe") != ""
}

// Missing lists the tools that are not installed (deno is recommended).
func (t *Tools) Missing() []string {
	var out []string
	for _, n := range []string{"yt-dlp", "ffmpeg", "deno", "gallery-dl"} {
		if t.Find(n) == "" {
			out = append(out, n)
		}
	}
	return out
}

type ToolsStatus struct {
	Ready      bool              `json:"ready"`
	Tools      map[string]bool   `json:"tools"`
	Paths      map[string]string `json:"paths"`
	Missing    []string          `json:"missing"`
	Installing bool              `json:"installing"`
	Progress   []toolProgress    `json:"progress"`
	Error      string            `json:"error,omitempty"`
	CanInstall bool              `json:"canInstall"`
	DownloadMB int64             `json:"downloadMB"` // rough size of what is missing
	Update     string            `json:"update,omitempty"`
}

func (t *Tools) Status() ToolsStatus {
	st := ToolsStatus{Ready: t.Ready(), Tools: map[string]bool{}, Paths: map[string]string{}, Missing: t.Missing()}
	for _, n := range []string{"yt-dlp", "ffmpeg", "ffprobe", "deno", "gallery-dl"} {
		p := t.Find(n)
		st.Tools[n] = p != ""
		st.Paths[n] = p
	}
	srcs := toolSources()
	st.CanInstall = len(srcs) > 0
	for _, s := range srcs {
		if t.Find(s.name) == "" {
			st.DownloadMB += s.size >> 20
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st.Installing = t.installing
	st.Error = t.lastErr
	st.Update = t.updateMsg
	for _, p := range t.progress {
		st.Progress = append(st.Progress, *p)
	}
	return st
}

// OnReady runs f once the tools are ready (now, or after an install).
func (t *Tools) OnReady(f func()) {
	if t.Ready() {
		go f()
		return
	}
	t.mu.Lock()
	t.waiters = append(t.waiters, f)
	t.mu.Unlock()
}

// Ensure returns the path of a tool, installing the missing tools first
// when it is not there (waits up to wait). "" = not available.
func (t *Tools) Ensure(ctx context.Context, name string, wait time.Duration) string {
	if p := t.Find(name); p != "" {
		return p
	}
	can := false
	for _, s := range toolSources() {
		if s.name == name {
			can = true
		}
	}
	if !can || t.Install() != nil {
		return ""
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if p := t.Find(name); p != "" {
			return p
		}
		t.mu.Lock()
		busy := t.installing
		t.mu.Unlock()
		if !busy {
			return t.Find(name)
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(500 * time.Millisecond):
		}
	}
	return ""
}

// Install downloads every missing tool (at the same time) in the background.
func (t *Tools) Install() error {
	srcs := toolSources()
	if len(srcs) == 0 {
		return errors.New("automatic install is only available on Windows; install yt-dlp and ffmpeg yourself")
	}
	t.mu.Lock()
	if t.installing {
		t.mu.Unlock()
		return nil
	}
	t.installing = true
	t.lastErr = ""
	t.progress = nil
	var todo []toolSource
	for _, s := range srcs {
		if t.Find(s.name) == "" || (s.name == "ffmpeg" && t.Find("ffprobe") == "") {
			todo = append(todo, s)
			t.progress = append(t.progress, &toolProgress{Name: s.name, Total: s.size, State: "waiting"})
		}
	}
	t.mu.Unlock()

	go func() {
		var wg sync.WaitGroup
		var errMu sync.Mutex
		var errs []string
		for i, s := range todo {
			wg.Add(1)
			go func(i int, s toolSource) {
				defer wg.Done()
				if err := t.installOne(s, t.progress[i]); err != nil {
					log.Printf("tools: %s: %v", s.name, err)
					errMu.Lock()
					errs = append(errs, s.name+": "+err.Error())
					errMu.Unlock()
				}
			}(i, s)
		}
		wg.Wait()
		t.mu.Lock()
		t.installing = false
		if len(errs) > 0 {
			t.lastErr = "Could not download " + strings.Join(errs, "; ")
		}
		var run []func()
		if t.Ready() {
			run, t.waiters = t.waiters, nil
		}
		t.mu.Unlock()
		for _, f := range run {
			go f()
		}
		if t.Ready() {
			_ = os.WriteFile(filepath.Join(t.dir, "last-update.txt"), []byte(time.Now().Format(time.RFC3339)), 0644)
		}
	}()
	return nil
}

func (t *Tools) setProg(p *toolProgress, f func(p *toolProgress)) {
	t.mu.Lock()
	f(p)
	t.mu.Unlock()
}

func (t *Tools) installOne(s toolSource, p *toolProgress) error {
	var lastErr error
	for _, u := range s.urls {
		for try := 1; try <= 3; try++ {
			err := t.fetchTool(s, u, p)
			if err == nil {
				t.setProg(p, func(p *toolProgress) { p.State = "done"; p.Done = p.Total })
				return nil
			}
			lastErr = err
			time.Sleep(time.Duration(try) * 2 * time.Second)
		}
	}
	t.setProg(p, func(p *toolProgress) { p.State = "error" })
	return lastErr
}

func (t *Tools) fetchTool(s toolSource, u string, p *toolProgress) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}
	t.setProg(p, func(p *toolProgress) {
		p.State = "downloading"
		p.Done = 0
		if resp.ContentLength > 0 {
			p.Total = resp.ContentLength
		}
	})
	tmp := filepath.Join(t.dir, s.name+".download")
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	buf := make([]byte, 256*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return werr
			}
			t.setProg(p, func(p *toolProgress) { p.Done += int64(n) })
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if resp.ContentLength > 0 {
		if fi, err := os.Stat(tmp); err != nil || fi.Size() != resp.ContentLength {
			return errors.New("download was cut short")
		}
	}

	if len(s.inZip) == 0 {
		dst := filepath.Join(t.dir, s.name+exeSuffix)
		if err := os.Chmod(tmp, 0755); err != nil {
			return err
		}
		return replaceFile(tmp, dst)
	}
	return extractFromZip(tmp, t.dir, s.inZip)
}

// extractFromZip takes only the named programs out of a zip (from any folder in it).
func extractFromZip(zipPath, dir string, names []string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	found := map[string]bool{}
	for _, zf := range zr.File {
		base := path.Base(strings.ReplaceAll(zf.Name, "\\", "/"))
		want := false
		for _, n := range names {
			if strings.EqualFold(base, n) {
				want = true
			}
		}
		if !want || found[strings.ToLower(base)] || zf.FileInfo().IsDir() {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		tmp := filepath.Join(dir, base+".tmp")
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(tmp)
			return err
		}
		if err := replaceFile(tmp, filepath.Join(dir, base)); err != nil {
			return err
		}
		found[strings.ToLower(base)] = true
	}
	for _, n := range names {
		if !found[strings.ToLower(n)] {
			return fmt.Errorf("%s was not found in the download", n)
		}
	}
	return nil
}

func replaceFile(src, dst string) error {
	_ = os.Remove(dst)
	return os.Rename(src, dst)
}

// AutoUpdate runs "yt-dlp -U" at most once a day for the copy in our tools
// folder (sites change often, old yt-dlp versions stop working).
func (t *Tools) AutoUpdate() {
	exe := filepath.Join(t.dir, "yt-dlp"+exeSuffix)
	if !fileExists(exe) {
		return
	}
	stamp := filepath.Join(t.dir, "last-update.txt")
	if data, err := os.ReadFile(stamp); err == nil {
		if last, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data))); err == nil && time.Since(last) < 24*time.Hour {
			return
		}
	}
	_ = os.WriteFile(stamp, []byte(time.Now().Format(time.RFC3339)), 0644)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-U")
	prepareProc(cmd)
	out, err := cmd.CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if i := strings.LastIndex(msg, "\n"); i >= 0 {
		msg = msg[i+1:]
	}
	if err != nil {
		msg = "yt-dlp update failed: " + err.Error()
	}
	log.Printf("tools: %s", msg)
	t.mu.Lock()
	t.updateMsg = msg
	t.mu.Unlock()
}
