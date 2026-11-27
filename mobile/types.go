package mobile

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Settings is persisted as settings.json.
type Settings struct {
	DownloadDir       string     `json:"downloadDir"`
	Parts             int        `json:"parts"`
	MaxActive         int        `json:"maxActive"`
	Notifications     bool       `json:"notifications"`
	ResumeOnStart     bool       `json:"resumeOnStart"`
	CheckUpdates      bool       `json:"checkUpdates"`
	HTTPDownloadLimit int64      `json:"httpDownloadLimit"` // bytes/s, 0 = unlimited
	Theme             string     `json:"theme"`             // system, light, dark
	WifiOnly          bool       `json:"wifiOnly"`
	Categories        []Category `json:"categories"`
	// Browser / ad blocker (item 95).
	AdBlocker         bool `json:"adBlocker"`
	BlockPopups       bool `json:"blockPopups"`
	BlockTrackers     bool `json:"blockTrackers"`
	FilterListAutoUpd bool `json:"filterListAutoUpdate"`
	// Tabs: close unused tabs after this many days (0 = never). Item 96.
	AutoCloseTabsDays int `json:"autoCloseTabsDays"`
}

// Category is a file type and its folder.
type Category struct {
	Name string `json:"name"`
	Exts string `json:"exts"`
	Dir  string `json:"dir"`
}

func defaultSettings(downloadDir string) Settings {
	if downloadDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			downloadDir = filepath.Join(home, "Download", "BDDM")
		} else {
			downloadDir = "Download/BDDM"
		}
	}
	return Settings{
		DownloadDir:       downloadDir,
		Parts:             8,
		MaxActive:         3,
		Notifications:     true,
		ResumeOnStart:     true,
		CheckUpdates:      true,
		Theme:             "dark",
		WifiOnly:          false,
		Categories:        defaultCategories(),
		AdBlocker:         true,
		BlockPopups:       true,
		BlockTrackers:     true,
		FilterListAutoUpd: true,
		AutoCloseTabsDays: 7,
	}
}

func (s *Settings) fix(fallbackDir string) {
	d := defaultSettings(fallbackDir)
	s.DownloadDir = strings.TrimSpace(s.DownloadDir)
	if s.DownloadDir == "" {
		s.DownloadDir = d.DownloadDir
	}
	if s.Parts < 1 {
		s.Parts = d.Parts
	}
	if s.Parts > 32 {
		s.Parts = 32
	}
	if s.MaxActive < 1 {
		s.MaxActive = d.MaxActive
	}
	if s.MaxActive > 10 {
		s.MaxActive = 10
	}
	if s.HTTPDownloadLimit < 0 {
		s.HTTPDownloadLimit = 0
	}
	switch s.Theme {
	case "system", "light", "dark":
	default:
		s.Theme = "dark"
	}
	if s.AutoCloseTabsDays < 0 {
		s.AutoCloseTabsDays = 0
	}
	s.Categories = fixCategories(s.Categories)
}

func defaultCategories() []Category {
	return []Category{
		{Name: "Video", Exts: "mp4 mkv avi mov wmv flv webm m4v mpg mpeg mpe 3gp 3g2 ts m2ts mts ogv vob"},
		{Name: "Audio", Exts: "mp3 m4a aac wav flac ogg oga opus wma aif aiff"},
		{Name: "Documents", Exts: "pdf doc docx xls xlsx ppt pptx odt ods odp rtf txt csv epub"},
		{Name: "Compressed", Exts: "zip rar 7z tar gz tgz bz2 xz zst iso img"},
		{Name: "Programs", Exts: "apk xapk exe msi jar"},
		{Name: "Images", Exts: "jpg jpeg png gif bmp webp svg tif tiff heic ico"},
		{Name: "Other"},
	}
}

func fixCategories(in []Category) []Category {
	out := defaultCategories()
	for i := range out {
		for _, c := range in {
			if strings.EqualFold(c.Name, out[i].Name) {
				if out[i].Name != "Other" && strings.TrimSpace(c.Exts) != "" {
					out[i].Exts = c.Exts
				}
				out[i].Dir = strings.TrimSpace(c.Dir)
			}
		}
	}
	return out
}

func (s Settings) dirFor(name string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(strings.TrimSpace(name))), ".")
	if ext != "" {
		for _, c := range s.Categories {
			if c.Dir == "" {
				continue
			}
			for _, e := range strings.Fields(c.Exts) {
				if e == ext {
					return c.Dir
				}
			}
		}
	}
	return s.DownloadDir
}

// JobView is one row in the list (and the detail screen).
type JobView struct {
	ID       string   `json:"id"`
	URL      string   `json:"url"`
	Name     string   `json:"name"`
	Dir      string   `json:"dir"`
	Size     int64    `json:"size"`
	Done     int64    `json:"done"`
	Speed    float64  `json:"speed"`
	ETA      int64    `json:"eta"` // seconds, -1 unknown
	Status   string   `json:"status"`
	Error    string   `json:"error,omitempty"`
	Message  string   `json:"message,omitempty"`
	Started  string   `json:"started,omitempty"`
	Finished string   `json:"finished,omitempty"`
	Kind     string   `json:"kind,omitempty"`
	Segs     *SegInfo `json:"segs,omitempty"`
}

// SegView is one segment of the per-part bar.
type SegView struct {
	W     int64  `json:"w"`
	Done  int64  `json:"d"`
	State string `json:"s"` // active, done, retry, wait
}

// SegInfo is the per-part progress snapshot (same shape as the PC app).
type SegInfo struct {
	Kind     string    `json:"kind"`
	Segs     []SegView `json:"segs"`
	Total    int       `json:"total"`
	Finished int       `json:"finished"`
	Active   int       `json:"active"`
	Retrying int       `json:"retrying"`
}

func (s *SegInfo) count() {
	s.Total, s.Finished, s.Active, s.Retrying = len(s.Segs), 0, 0, 0
	for _, g := range s.Segs {
		switch g.State {
		case "done":
			s.Finished++
		case "active":
			s.Active++
		case "retry":
			s.Retrying++
		}
	}
}

func (s *SegInfo) idle() *SegInfo {
	if s == nil {
		return nil
	}
	c := *s
	c.Segs = make([]SegView, len(s.Segs))
	for i, g := range s.Segs {
		if g.State == "active" || g.State == "retry" {
			g.State = "wait"
		}
		c.Segs[i] = g
	}
	c.Active, c.Retrying = 0, 0
	return &c
}

// Snapshot is SnapshotJSON.
type Snapshot struct {
	Version string    `json:"version"`
	Jobs    []JobView `json:"jobs"`
	Speed   float64   `json:"speed"`
	ETA     int64     `json:"eta"`
	Upload  float64   `json:"upload"`
	Active  int       `json:"active"`
	Queued  int       `json:"queued"`
	Done    int       `json:"done"`
	Error   int       `json:"error"`
}

type job struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Want        string    `json:"want,omitempty"`
	Name        string    `json:"name"`
	Dir         string    `json:"dir"`
	Size        int64     `json:"size"`
	Done        int64     `json:"done"`
	Parts       int       `json:"parts"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
	Started     time.Time `json:"started"`
	Finished    time.Time `json:"finished,omitempty"`
	Interrupted bool      `json:"interrupted,omitempty"`
	AutoDir     bool      `json:"autoDir,omitempty"`
	Kind        string    `json:"kind,omitempty"`

	mu        sync.Mutex
	speed     float64
	message   string
	segs      *SegInfo
	cancel    context.CancelFunc
	finished  chan struct{}
	xferStart time.Time
	xferFrom  int64
}

func (j *job) view() JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	v := JobView{
		ID: j.ID, URL: j.URL, Name: j.displayName(), Dir: j.Dir,
		Size: j.Size, Done: j.Done, Speed: j.speed, Status: j.Status,
		Error: j.Error, Message: j.message, Kind: j.Kind,
	}
	if !j.Started.IsZero() {
		v.Started = j.Started.Format(time.RFC3339)
	}
	if !j.Finished.IsZero() {
		v.Finished = j.Finished.Format(time.RFC3339)
	}
	v.ETA = -1
	if j.Status == StatusDownloading && j.speed > 0 && j.Size > j.Done {
		v.ETA = int64(float64(j.Size-j.Done) / j.speed)
	}
	if j.segs != nil && len(j.segs.Segs) >= 2 && j.Status != StatusDone {
		if j.Status == StatusDownloading {
			v.Segs = j.segs
		} else {
			v.Segs = j.segs.idle()
		}
	}
	return v
}

func (j *job) displayName() string {
	if j.Name != "" {
		return j.Name
	}
	if j.Want != "" {
		return j.Want
	}
	return filenameFromURL(j.URL)
}

func (j *job) setMessage(s string) {
	j.mu.Lock()
	j.message = s
	j.mu.Unlock()
}

func (j *job) setSegs(s *SegInfo) {
	j.mu.Lock()
	j.segs = s
	j.mu.Unlock()
}

func (j *job) waitStopped() {
	j.mu.Lock()
	ch := j.finished
	j.mu.Unlock()
	if ch != nil {
		<-ch
	}
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format("150405.00000000")))
	}
	return hex.EncodeToString(b[:])
}

type persistedJob struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Want        string    `json:"want,omitempty"`
	Name        string    `json:"name"`
	Dir         string    `json:"dir"`
	Size        int64     `json:"size"`
	Done        int64     `json:"done"`
	Parts       int       `json:"parts"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
	Started     time.Time `json:"started"`
	Finished    time.Time `json:"finished,omitempty"`
	Interrupted bool      `json:"interrupted,omitempty"`
	Kind        string    `json:"kind,omitempty"`
}

func (e *Engine) settingsPath() string { return filepath.Join(e.cfgDir, "settings.json") }
func (e *Engine) historyPath() string  { return filepath.Join(e.cfgDir, "history.json") }

func (e *Engine) loadSettings() {
	b, err := os.ReadFile(e.settingsPath())
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &e.settings)
}

func (e *Engine) saveSettingsLocked() {
	b, err := json.MarshalIndent(e.settings, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(e.settingsPath(), b, 0644)
}

func (e *Engine) loadHistory() {
	b, err := os.ReadFile(e.historyPath())
	if err != nil {
		return
	}
	var list []persistedJob
	if json.Unmarshal(b, &list) != nil {
		return
	}
	for _, p := range list {
		st := p.Status
		if st == StatusDownloading {
			st = StatusQueued
			p.Interrupted = true
		}
		e.jobs = append(e.jobs, &job{
			ID: p.ID, URL: p.URL, Want: p.Want, Name: p.Name, Dir: p.Dir,
			Size: p.Size, Done: p.Done, Parts: p.Parts, Status: st, Error: p.Error,
			Started: p.Started, Finished: p.Finished, Interrupted: p.Interrupted, Kind: p.Kind,
		})
	}
}

func (e *Engine) saveHistoryLocked() {
	list := make([]persistedJob, 0, len(e.jobs))
	for _, j := range e.jobs {
		list = append(list, persistedJob{
			ID: j.ID, URL: j.URL, Want: j.Want, Name: j.Name, Dir: j.Dir,
			Size: j.Size, Done: j.Done, Parts: j.Parts, Status: j.Status, Error: j.Error,
			Started: j.Started, Finished: j.Finished, Interrupted: j.Interrupted, Kind: j.Kind,
		})
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(e.historyPath(), b, 0644)
}
