// Package mobile is the BDDM download engine for Android (gomobile bind).
// It reuses the same HTTP multi-part, pause/resume and per-part progress
// model as the PC app, exposed through a small, bindable API.
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const AppVersion = "1.6.3"

const (
	StatusQueued      = "Queued"
	StatusDownloading = "Downloading"
	StatusPaused      = "Paused"
	StatusDone        = "Done"
	StatusError       = "Error"
)

// Listener is notified when jobs or totals change. Implemented on the Kotlin side.
type Listener interface {
	OnChange()
}

// Engine is the bindable download manager.
type Engine struct {
	mu       sync.Mutex
	cfgDir   string
	settings Settings
	jobs     []*job
	client   *httpClient
	limit    *limiter
	closing  bool
	listener Listener
}

// New starts the engine. configDir holds settings.json and history.json.
// downloadDir is the default folder for new files.
func New(configDir, downloadDir string) (*Engine, error) {
	if configDir == "" {
		return nil, errors.New("config directory is required")
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, fmt.Errorf("cannot create config folder: %w", err)
	}
	e := &Engine{
		cfgDir: configDir,
		client: newHTTPClient(),
		limit:  newLimiter(0),
	}
	e.settings = defaultSettings(downloadDir)
	e.loadSettings()
	e.settings.fix(downloadDir)
	if e.settings.DownloadDir != "" {
		_ = os.MkdirAll(e.settings.DownloadDir, 0755)
	}
	e.limit.set(e.settings.HTTPDownloadLimit)
	e.loadHistory()
	if e.settings.ResumeOnStart {
		e.resumeInterruptedLocked()
	}
	return e, nil
}

// Close stops every download and writes state to disk.
func (e *Engine) Close() {
	e.mu.Lock()
	if e.closing {
		e.mu.Unlock()
		return
	}
	e.closing = true
	var running []*job
	for _, j := range e.jobs {
		if j.Status == StatusDownloading {
			j.Interrupted = true
			running = append(running, j)
			if j.cancel != nil {
				j.cancel()
			}
		}
	}
	e.saveHistoryLocked()
	e.mu.Unlock()
	for _, j := range running {
		j.waitStopped()
	}
}

// SetListener registers a UI callback (may be nil).
func (e *Engine) SetListener(l Listener) {
	e.mu.Lock()
	e.listener = l
	e.mu.Unlock()
}

func (e *Engine) notify() {
	e.mu.Lock()
	l := e.listener
	e.mu.Unlock()
	if l != nil {
		l.OnChange()
	}
}

// Add starts (or queues) an HTTP or HTTPS file download. dir and filename may be empty.
// Returns the job id.
func (e *Engine) Add(rawURL, dir, filename string) (string, error) {
	return e.AddWithHeaders(rawURL, dir, filename, "")
}

// AddWithHeaders is Add plus extra request headers as a JSON object, for example
// {"Cookie":"a=b","Referer":"https://site/","User-Agent":"..."} taken from the browser.
func (e *Engine) AddWithHeaders(rawURL, dir, filename, headersJSON string) (string, error) {
	headers := parseHeaders(headersJSON)
	rawURL = strings.TrimSpace(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("Please enter a valid http:// or https:// link.")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing {
		return "", errors.New("BDDM is shutting down")
	}
	want := sanitizeName(filename)
	if dir == "" {
		dir = e.settings.dirFor(firstNonEmpty(want, filenameFromURL(rawURL)))
	}
	j := &job{
		ID:      newID(),
		URL:     rawURL,
		Want:    want,
		Dir:     dir,
		Parts:   e.settings.Parts,
		Status:  StatusQueued,
		Started: time.Now(),
		AutoDir: dir == "" || dir == e.settings.DownloadDir,
		Headers: headers,
	}
	e.jobs = append(e.jobs, j)
	e.scheduleLocked()
	e.saveHistoryLocked()
	go e.notify()
	return j.ID, nil
}

// Pause stops a running or queued download. It can be resumed later.
func (e *Engine) Pause(id string) error {
	e.mu.Lock()
	j := e.findLocked(id)
	if j == nil {
		e.mu.Unlock()
		return errors.New("download not found")
	}
	if j.Status == StatusDone {
		e.mu.Unlock()
		return errors.New("this download is already finished")
	}
	j.Status = StatusPaused
	j.Interrupted = false
	cancel := j.cancel
	e.saveHistoryLocked()
	e.scheduleLocked()
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	go e.notify()
	return nil
}

// Resume continues a paused or failed download.
func (e *Engine) Resume(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	j := e.findLocked(id)
	if j == nil {
		return errors.New("download not found")
	}
	if j.Status == StatusDownloading || j.Status == StatusQueued {
		return nil
	}
	if j.Status == StatusDone {
		return errors.New("this download is already finished")
	}
	j.Status = StatusQueued
	j.Error = ""
	j.Interrupted = false
	e.scheduleLocked()
	e.saveHistoryLocked()
	go e.notify()
	return nil
}

// UpdateURL replaces an expired link of a paused or failed download (link refresh).
// The bytes already downloaded are kept when the new link serves the same file size.
func (e *Engine) UpdateURL(id, rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("Please enter a valid http:// or https:// link.")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	j := e.findLocked(id)
	if j == nil {
		return errors.New("download not found")
	}
	if j.Status == StatusDownloading || j.Status == StatusQueued {
		return errors.New("pause the download before changing its link")
	}
	if j.Status == StatusDone {
		return errors.New("this download is already finished")
	}
	j.URL = rawURL
	j.Error = ""
	e.saveHistoryLocked()
	go e.notify()
	return nil
}

// Delete removes a download from the list. If deleteFile is true, the file is removed too.
func (e *Engine) Delete(id string, deleteFile bool) error {
	e.mu.Lock()
	j := e.findLocked(id)
	if j == nil {
		e.mu.Unlock()
		return errors.New("download not found")
	}
	cancel := j.cancel
	j.Status = StatusPaused
	dir, name := j.Dir, j.Name
	// drop from the slice
	out := e.jobs[:0]
	for _, x := range e.jobs {
		if x != j {
			out = append(out, x)
		}
	}
	e.jobs = out
	e.scheduleLocked()
	e.saveHistoryLocked()
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	j.waitStopped()
	if deleteFile && name != "" && dir != "" {
		base := filepath.Join(dir, name)
		_ = os.Remove(base)
		_ = os.Remove(base + ".part")
		_ = os.Remove(base + ".bddm.json")
	}
	go e.notify()
	return nil
}

// FilePath returns the finished file path, or the in-progress .part path.
func (e *Engine) FilePath(id string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	j := e.findLocked(id)
	if j == nil {
		return "", errors.New("download not found")
	}
	if j.Dir == "" || j.Name == "" {
		return "", errors.New("the file path is not known yet")
	}
	full := filepath.Join(j.Dir, j.Name)
	if fileExists(full) {
		return full, nil
	}
	if fileExists(full + ".part") {
		return full + ".part", nil
	}
	return full, nil
}

// SnapshotJSON is the current list, totals and per-part progress, as JSON.
func (e *Engine) SnapshotJSON() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Snapshot{Jobs: make([]JobView, 0, len(e.jobs)), Version: AppVersion}
	var down float64
	var etaSet bool
	var eta int64 = -1
	for i := len(e.jobs) - 1; i >= 0; i-- {
		v := e.jobs[i].view()
		s.Jobs = append(s.Jobs, v)
		if v.Status == StatusDownloading {
			down += v.Speed
			if v.ETA > 0 {
				if !etaSet || v.ETA > eta {
					eta = v.ETA
					etaSet = true
				}
			}
		}
	}
	s.Speed = down
	s.ETA = eta
	s.Upload = 0
	s.Active = countStatus(e.jobs, StatusDownloading)
	s.Queued = countStatus(e.jobs, StatusQueued)
	s.Done = countStatus(e.jobs, StatusDone)
	s.Error = countStatus(e.jobs, StatusError)
	b, _ := json.Marshal(s)
	return string(b)
}

// SettingsJSON returns the current settings.
func (e *Engine) SettingsJSON() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, _ := json.Marshal(e.settings)
	return string(b)
}

// SetSettingsJSON applies settings from JSON. Unknown fields are ignored.
func (e *Engine) SetSettingsJSON(raw string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.settings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return errors.New("could not read settings")
	}
	s.fix(e.settings.DownloadDir)
	if err := os.MkdirAll(s.DownloadDir, 0755); err != nil {
		return fmt.Errorf("cannot use that folder: %w", err)
	}
	e.settings = s
	e.limit.set(s.HTTPDownloadLimit)
	e.saveSettingsLocked()
	e.scheduleLocked()
	go e.notify()
	return nil
}

func (e *Engine) findLocked(id string) *job {
	for _, j := range e.jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

func (e *Engine) resumeInterruptedLocked() {
	for _, j := range e.jobs {
		if j.Interrupted && (j.Status == StatusDownloading || j.Status == StatusQueued) {
			j.Status = StatusQueued
			j.Interrupted = false
		} else if j.Status == StatusDownloading {
			// Was mid-run in a previous process that did not mark Interrupted.
			j.Status = StatusQueued
		}
	}
	e.scheduleLocked()
}

func (e *Engine) scheduleLocked() {
	if e.closing {
		return
	}
	active := 0
	for _, j := range e.jobs {
		if j.Status == StatusDownloading {
			active++
		}
	}
	max := e.settings.MaxActive
	if max < 1 {
		max = 1
	}
	for _, j := range e.jobs {
		if active >= max {
			break
		}
		if j.Status == StatusQueued {
			e.startLocked(j)
			active++
		}
	}
}

func (e *Engine) startLocked(j *job) {
	j.Status = StatusDownloading
	j.Error = ""
	ctx, cancel := context.WithCancel(context.Background())
	j.cancel = cancel
	j.finished = make(chan struct{})
	go e.run(ctx, j)
}

func (e *Engine) run(ctx context.Context, j *job) {
	defer func() {
		e.mu.Lock()
		j.cancel = nil
		if j.finished != nil {
			close(j.finished)
			j.finished = nil
		}
		if !e.closing && j.Status == StatusDownloading {
			// ended without status change: treat as error
			j.Status = StatusError
			if j.Error == "" {
				j.Error = "the download stopped unexpectedly"
			}
		}
		e.scheduleLocked()
		e.saveHistoryLocked()
		e.mu.Unlock()
		e.notify()
	}()
	err := e.download(ctx, j)
	e.mu.Lock()
	defer e.mu.Unlock()
	if ctx.Err() != nil {
		if j.Status == StatusDownloading {
			if e.closing {
				j.Interrupted = true
			} else {
				j.Status = StatusPaused
			}
		}
		return
	}
	if err != nil {
		j.Status = StatusError
		j.Error = err.Error()
		return
	}
	j.Status = StatusDone
	j.Error = ""
	j.Finished = time.Now()
	j.speed = 0
}

func countStatus(jobs []*job, st string) int {
	n := 0
	for _, j := range jobs {
		if j.Status == st {
			n++
		}
	}
	return n
}
