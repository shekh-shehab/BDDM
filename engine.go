package main

// engine.go is the download engine.
//
// A Manager holds many Jobs. Each running Job downloads its file in several
// parts at once (each part on its own connection), saves its progress next to
// the file (name.part + name.bddm.json) so it can resume later, retries parts
// that fail or stall, and lowers the number of connections when the server
// answers "429 Too Many Requests".

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultParts     = 8
	defaultMaxActive = 3
	maxTries         = 10
	connectTries     = 5
	smallFile        = 1 << 20 // below this, one connection
	smallDriveFile   = 8 << 20 // Google Drive files below this, one connection
	maxActiveDrive   = 4       // Google Drive files downloading at the same time (at most)
	stallAfter       = 20 * time.Second
	stateInterval    = time.Second
	userAgent        = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"
)

// Job statuses shown in the UI.
const (
	StatusQueued      = "Queued"
	StatusDownloading = "Downloading"
	StatusPaused      = "Paused"
	StatusDone        = "Done"
	StatusError       = "Error"
	StatusPending     = "Pending" // waiting for the user in the "Download File Info" window
)

// httpStatusError is an error answer from the server.
type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string { return fmt.Sprintf("server returned HTTP %d", e.code) }

// stateSyncEvery: how often the progress file (and the data) is flushed to
// the disk while downloading.
var stateSyncEvery = 5 * time.Second

var (
	errFallback = errors.New("server rejected multiple connections")
	errNoRanges = errors.New("server does not support range requests")
	errStalled  = errors.New("download stalled")
)

// rateLimitError means the server answered 429 Too Many Requests.
type rateLimitError struct{ wait time.Duration }

func (e *rateLimitError) Error() string { return "server says too many requests" }

// ---------------------------------------------------------------------------
// Settings and jobs
// ---------------------------------------------------------------------------

type Settings struct {
	DownloadDir   string `json:"downloadDir"`
	Parts         int    `json:"parts"`
	MaxActive     int    `json:"maxActive"`
	Notifications bool   `json:"notifications"`
	// AskCaptured: show the "Download File Info" window before starting a
	// download that came from the browser extension.
	AskCaptured bool `json:"askCaptured"`
	// CookiesBrowser: "" (off), "firefox", "chrome", "edge" or "brave".
	// yt-dlp reads that browser's cookies for sites that want a sign-in.
	CookiesBrowser string `json:"cookiesBrowser"`
	// Categories: a download folder per file type.
	Categories []Category `json:"categories"`
	// StartWithWindows: start BDDM (without its window) when Windows starts.
	StartWithWindows bool `json:"startWithWindows"`
	// ResumeOnStart: downloads that were running when BDDM closed (or the
	// computer crashed) continue when BDDM starts again.
	ResumeOnStart bool `json:"resumeOnStart"`
	// CheckUpdates: look for a new GitHub release when BDDM starts and
	// about every 6 hours. On by default. A notice is shown; nothing is
	// installed automatically.
	CheckUpdates bool `json:"checkUpdates"`
}

func defaultSettings() Settings {
	dir := "Downloads"
	if home, err := os.UserHomeDir(); err == nil {
		dir = filepath.Join(home, "Downloads", "BDDM")
	}
	return Settings{DownloadDir: dir, Parts: defaultParts, MaxActive: defaultMaxActive, Notifications: true, AskCaptured: true,
		Categories: defaultCategories(), ResumeOnStart: true, CheckUpdates: true}
}

func (s *Settings) fix() {
	d := defaultSettings()
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
	switch s.CookiesBrowser {
	case "", "firefox", "chrome", "edge", "brave":
	default:
		s.CookiesBrowser = ""
	}
	cookiesBrowser.Store(s.CookiesBrowser)
	s.Categories = fixCategories(s.Categories)
	for i := range s.Categories {
		if d := s.Categories[i].Dir; d != "" && (!filepath.IsAbs(d) || filepath.Clean(d) == filepath.Clean(s.DownloadDir)) {
			s.Categories[i].Dir = "" // only full paths; the main folder is the default anyway
		}
	}
}

// Job is one download. The exported fields are saved in history.json.
// They are protected by Manager.mu.
type Job struct {
	ID       string    `json:"id"`
	URL      string    `json:"url"`
	Referer  string    `json:"referer,omitempty"`
	Cookie   string    `json:"cookie,omitempty"`
	Want     string    `json:"want,omitempty"` // file name asked for (optional)
	Name     string    `json:"name"`
	Dir      string    `json:"dir"`
	Size     int64     `json:"size"` // 0 = unknown
	Done     int64     `json:"done"`
	Parts    int       `json:"parts"`
	Status   string    `json:"status"`
	Error    string    `json:"error,omitempty"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Removed  bool      `json:"removed,omitempty"` // removed from the list, still in history

	// Videos (downloaded with yt-dlp).
	Kind       string        `json:"kind,omitempty"` // "" = file, "video"
	Video      *VideoOptions `json:"video,omitempty"`
	VideoFiles []string      `json:"videoFiles,omitempty"` // files yt-dlp wrote (for Delete)
	// Stream is "hls" or "dash" when the extension saw a stream whose link
	// does not end in .m3u8/.mpd.
	Stream string `json:"stream,omitempty"`
	// Export: the format of a Google Docs/Sheets/Slides download ("pdf", ...).
	Export string `json:"export,omitempty"`
	// Fast: a YouTube video downloaded with BDDM's own engine (ytfast.go).
	Fast *FastYT `json:"fast,omitempty"`
	// FixPlaylist: the site's playlist had extra bytes before #EXTM3U, so
	// BDDM cleans a fresh copy before every run (see playlist.go).
	FixPlaylist bool `json:"fixPlaylist,omitempty"`
	// Group: the folder download (groups.go) this file belongs to.
	Group string `json:"group,omitempty"`
	// AutoDir: the folder comes from the file type and is chosen again
	// when the real file name is known (before anything is written).
	AutoDir bool `json:"autoDir,omitempty"`
	// Mirrors: more links to the same file (pieces.go).
	Mirrors []string `json:"mirrors,omitempty"`
	// Hash: the checksum the user entered; Checked: the result of the check.
	Hash    *Checksum `json:"hash,omitempty"`
	Checked string    `json:"checked,omitempty"`
	// Interrupted: it was downloading when BDDM closed (or crashed), so it
	// continues when BDDM starts again.
	Interrupted bool `json:"interrupted,omitempty"`
	// Order: place in the queue when it was moved (queue.go).
	Order int64 `json:"order,omitempty"`
	// UA / Lang: the browser's User-Agent and Accept-Language (1.4.1).
	UA   string `json:"ua,omitempty"`
	Lang string `json:"lang,omitempty"`
	// PageTitle: the title of the page the link came from (1.4.2), for a
	// file name when the link's own name says nothing ("video.mp4").
	PageTitle string `json:"pageTitle,omitempty"`
	// TSFix: the stream's pieces are video data hidden behind a fake image
	// header, so ffmpeg is told to read them as MPEG-TS (disguise.go).
	TSFix bool `json:"tsFix,omitempty"`
	// RefAlts: other Referers to try when the site answers 403 (referer.go).
	RefAlts []string `json:"refAlts,omitempty"`
	// Torrent: BitTorrent-specific state (torrent.go). Kind is "torrent".
	Torrent *TorrentInfo `json:"torrent,omitempty"`

	// Runtime only.
	// extra / jar: headers the browser sent for the link and the cookies of
	// every host (headers.go). Never saved, never sent to mirrors.
	extra map[string]string
	jar   cookieSet
	// probed: what the "Download File Info" window found out about the
	// link, so the download does not ask the server again.
	probed     *probeInfo
	probedAt   time.Time
	pend       *pendingInfo  // set while Status is Pending
	vpend      *videoPending // set while a video waits for its options window
	localPL    string        // cleaned local copy of the playlist (FixPlaylist)
	refFrame   string        // stream: the player frame's address (referer.go)
	refPage    string        // stream: the tab's page (referer.go)
	refTried   map[string]bool
	done       atomic.Int64
	cancel     context.CancelFunc
	finished   chan struct{}
	stopReason string

	liveMu  sync.Mutex
	label   string  // name used in log lines
	speed   float64 // bytes per second
	message string
	msgTime time.Time
	// xferStart/xferFrom: when the data transfer began and how much was
	// already there (for a speed before the first one-second sample).
	xferStart time.Time
	xferFrom  int64
	restarts  int  // automatic restarts in this run (file changed, new link)
	netWait   bool // waiting for the internet connection to come back
	skipFrags bool // video: let yt-dlp skip stream pieces that keep failing

	// Torrents (runtime only; see torrent.go).
	th      torrentHandle // the live torrent, if any
	upSpeed float64
	peers   int
	tReady  bool              // add window: metadata is available
	tSource string            // "magnet" or "file"
	tFiles  []TorrentFileView // add window file list
}

func (j *Job) setMessage(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	j.liveMu.Lock()
	j.message = msg
	j.msgTime = time.Now()
	label := j.label
	j.liveMu.Unlock()
	log.Printf("[%s] %s", label, msg)
}

func (j *Job) displayName() string {
	if j.Name != "" {
		return j.Name
	}
	if j.Want != "" {
		return j.Want
	}
	if driveKey(j.URL) != "" {
		return "Google Drive file" // the real name comes with the first answer
	}
	return filenameFromURL(j.URL)
}

// JobView is what the UI receives.
type JobView struct {
	ID       string  `json:"id"`
	URL      string  `json:"url"`
	Name     string  `json:"name"`
	Dir      string  `json:"dir"`
	Size     int64   `json:"size"`
	Done     int64   `json:"done"`
	Speed    float64 `json:"speed"`
	ETA      int64   `json:"eta"` // seconds, -1 = unknown
	Status   string  `json:"status"`
	Error    string  `json:"error,omitempty"`
	Message  string  `json:"message,omitempty"`
	Started  string  `json:"started"`
	Finished string  `json:"finished,omitempty"`
	Removed  bool    `json:"removed,omitempty"`
	Kind     string  `json:"kind,omitempty"`
	Group    string  `json:"group,omitempty"`
	QueuePos int     `json:"queuePos,omitempty"` // Queued: place in the queue (1 = next)
	QueueLen int     `json:"queueLen,omitempty"`
	ViewOnly bool    `json:"viewOnly,omitempty"` // Google Drive: the owner turned off downloading
	Reason   string  `json:"reason,omitempty"`   // Error: what went wrong, in plain words
	Fix      string  `json:"fix,omitempty"`      // Error: what to do about it
	Checked  string  `json:"checked,omitempty"`  // checksum result ("SHA-256 matches ...")
	NetWait  bool    `json:"netWait,omitempty"`  // waiting for the internet connection
	Mirrors  int     `json:"mirrors,omitempty"`  // extra links
	Peers    int     `json:"peers,omitempty"`    // torrent: connected peers
	UpSpeed  float64 `json:"upSpeed,omitempty"`  // torrent: upload bytes/s
	Files    int     `json:"files,omitempty"`    // torrent: number of files
	Uploaded int64   `json:"uploaded,omitempty"` // torrent: bytes uploaded
}

func (j *Job) viewLocked() JobView {
	v := JobView{
		ID: j.ID, URL: j.URL, Name: j.displayName(), Dir: j.Dir, Size: j.Size,
		Done: j.done.Load(), Status: j.Status, Error: j.Error, ETA: -1,
		Started: j.Started.Format(time.RFC3339), Removed: j.Removed, Kind: j.Kind, Group: j.Group,
		ViewOnly: j.Status == StatusError && j.Error == msgDriveViewOnly,
	}
	if !j.Finished.IsZero() {
		v.Finished = j.Finished.Format(time.RFC3339)
	}
	v.Checked, v.Mirrors = j.Checked, len(j.Mirrors)
	if j.Status == StatusError {
		v.Reason, v.Fix = explainError(j.Error)
	}
	if j.Status == StatusDone && j.Size > 0 {
		v.Done = j.Size
	}
	if j.Status == StatusDownloading {
		j.liveMu.Lock()
		v.Speed = j.speed
		if v.Speed <= 0 && !j.xferStart.IsZero() {
			// No one-second sample yet: the average since the transfer began.
			if el := time.Since(j.xferStart).Seconds(); el > 0.2 {
				v.Speed = float64(j.done.Load()-j.xferFrom) / el
			}
		}
		if time.Since(j.msgTime) < 30*time.Second || j.netWait {
			v.Message = j.message
		}
		v.NetWait = j.netWait
		j.liveMu.Unlock()
		if v.Speed > 1 && j.Size > 0 && j.Size > v.Done {
			v.ETA = int64(float64(j.Size-v.Done) / v.Speed)
		}
	}
	if j.Kind == kindTorrent {
		j.liveMu.Lock()
		v.Peers = j.peers
		v.UpSpeed = j.upSpeed
		if j.Status == StatusSeeding {
			v.Speed = 0
			if time.Since(j.msgTime) < 30*time.Second {
				v.Message = j.message
			}
			if v.UpSpeed <= 0 && time.Since(j.msgTime) >= 0 {
				// keep the last measured upload speed
			}
		}
		j.liveMu.Unlock()
		if j.Torrent != nil {
			v.Files = j.Torrent.Files
			v.Uploaded = j.Torrent.Uploaded
		}
		if (j.Status == StatusSeeding || j.Status == StatusDone) && j.Size > 0 {
			v.Done = j.Size
		}
		if j.Status == StatusSeeding && v.UpSpeed > 0 {
			// no ETA while seeding
			v.ETA = -1
		}
	}
	return v
}

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

type AddRequest struct {
	URL      string   `json:"url"`
	Referer  string   `json:"referer"`
	Cookie   string   `json:"cookie"`
	Filename string   `json:"filename"`
	Force    bool     `json:"force"`             // download even if it was downloaded before
	Kind     string   `json:"kind"`              // "video" = open the video options window
	Title    string   `json:"title"`             // page title (videos, optional)
	Stream   string   `json:"stream"`            // "hls"/"dash": a stream seen by the extension
	Mirrors  []string `json:"mirrors,omitempty"` // more links to the same file
	Hash     string   `json:"hash,omitempty"`    // checksum to check the file with
	// From the browser extension (1.4.1): the browser's User-Agent and
	// languages, the headers it sent for the link and the cookies of every
	// host involved (page, link, CDN).
	UserAgent      string            `json:"userAgent,omitempty"`
	AcceptLanguage string            `json:"acceptLanguage,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Cookies        []hostCookie      `json:"cookies,omitempty"`
	// 1.4.2: for a stream, the full address of the player frame that
	// loaded it and the tab's page (other Referers to try after a 403).
	Frame string `json:"frame,omitempty"`
	Page  string `json:"page,omitempty"`
}

// DuplicateError is returned by Add when the URL was already downloaded.
type DuplicateError struct{ Name string }

func (e *DuplicateError) Error() string { return "You already downloaded this file." }

type Manager struct {
	mu        sync.Mutex
	jobs      []*Job // includes removed jobs (history)
	settings  Settings
	configDir string
	client    *http.Client
	closing   bool
	dirty     bool
	notify    func(title, body string)
	tools     *Tools

	driveFolders map[string]*driveFolder // Google Drive folders waiting in their window
	groups       []*Group                // folder downloads (groups.go)
	ann          announcer               // toasts / notifications (groups.go)

	tClient   torrentClient // shared BitTorrent client (torrent.go)
	tLoopback bool          // tests: listen on 127.0.0.1 only, no DHT
}

func NewManager(configDir string, notify func(title, body string)) (*Manager, error) {
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, err
	}
	m := &Manager{configDir: configDir, client: newClient(), settings: defaultSettings(), notify: notify}
	go removeOldCookieFiles()
	refMem.setDir(configDir)
	driveSampleDir = filepath.Join(configDir, "drive-pages")
	m.tools = newTools(configDir, m.client)

	if data, err := os.ReadFile(m.settingsPath()); err == nil {
		s := defaultSettings()
		if json.Unmarshal(data, &s) == nil {
			m.settings = s
		}
	}
	m.settings.fix()
	m.settings.StartWithWindows = autostartEnabled() // the installer may have set it

	if data, err := os.ReadFile(m.historyPath()); err == nil {
		var jobs []*Job
		if err := json.Unmarshal(data, &jobs); err != nil {
			log.Printf("could not read history: %v", err)
		}
		for _, j := range jobs {
			if j == nil || j.ID == "" || (j.URL == "" && j.Kind != kindTorrent) {
				continue
			}
			if j.Kind == kindTorrent {
				// User-paused stays paused. Downloading and seeding torrents
				// are started again after the history is loaded.
				if j.Status == StatusQueued {
					j.Status = StatusDownloading
				}
				j.done.Store(j.Done)
				m.jobs = append(m.jobs, j)
				continue
			}
			// Anything that was running when the app closed (or crashed)
			// comes back paused, or continues (Settings: ResumeOnStart).
			if j.Status == StatusDownloading || j.Status == StatusQueued || j.Status == "" {
				j.Status = StatusPaused
				j.Interrupted = true
			}
			if j.Interrupted && j.Status == StatusPaused && !j.Removed && m.settings.ResumeOnStart {
				j.Status = StatusQueued
			}
			if j.Status != StatusQueued {
				j.Interrupted = false
			}
			j.done.Store(j.Done)
			m.jobs = append(m.jobs, j)
		}
	}

	m.loadGroups()
	m.mu.Lock()
	for _, j := range m.jobs {
		if j.Status == StatusQueued {
			j.Interrupted = false
			log.Printf("[%s] continuing (it was running when BDDM closed)", j.displayName())
		}
	}
	m.scheduleLocked()
	m.restoreTorrentsLocked()
	m.mu.Unlock()
	go m.autosave()
	return m, nil
}

func (m *Manager) settingsPath() string { return filepath.Join(m.configDir, "settings.json") }
func (m *Manager) historyPath() string  { return filepath.Join(m.configDir, "history.json") }

func writeFileAtomic(p string, data []byte) error { return writeFileSync(p, data) }

func (m *Manager) saveHistoryLocked() {
	for _, j := range m.jobs {
		j.Done = j.done.Load()
	}
	saved := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		if j.Status != StatusPending { // not confirmed yet: never saved
			saved = append(saved, j)
		}
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err == nil {
		err = writeFileAtomic(m.historyPath(), data)
	}
	if err != nil {
		log.Printf("could not save history: %v", err)
	}
	m.dirty = false
}

// autosave writes history every few seconds while something is running.
func (m *Manager) autosave() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		m.mu.Lock()
		if m.closing {
			m.mu.Unlock()
			return
		}
		running := false
		for _, j := range m.jobs {
			if j.cancel != nil {
				running = true
				break
			}
		}
		if running || m.dirty {
			m.saveHistoryLocked()
		}
		m.mu.Unlock()
	}
}

func (m *Manager) Settings() Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings
}

func (m *Manager) SetSettings(s Settings) (Settings, error) {
	s.fix()
	if err := os.MkdirAll(s.DownloadDir, 0755); err != nil {
		return s, fmt.Errorf("cannot use this folder: %v", err)
	}
	for _, c := range s.Categories {
		if c.Dir != "" {
			if err := os.MkdirAll(c.Dir, 0755); err != nil {
				return s, fmt.Errorf("cannot use the %s folder: %v", c.Name, err)
			}
		}
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	if err := writeFileAtomic(m.settingsPath(), data); err != nil {
		return s, err
	}
	m.mu.Lock()
	m.settings = s
	m.scheduleLocked()
	m.mu.Unlock()
	applyAutostart(s.StartWithWindows)
	return s, nil
}

// saveSettings writes the current settings (after BDDM itself changed them).
func (m *Manager) saveSettings() {
	m.mu.Lock()
	data, _ := json.MarshalIndent(m.settings, "", "  ")
	m.mu.Unlock()
	if err := writeFileAtomic(m.settingsPath(), data); err != nil {
		log.Printf("could not save settings: %v", err)
	}
}

func newID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func (m *Manager) findLocked(id string) *Job {
	for _, j := range m.jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

// FindDone returns a finished job with the same URL, if any.
// completeOnDisk: the job finished and its file is still on disk, whole.
func (j *Job) completeOnDisk() bool {
	if j.Status != StatusDone || j.Name == "" {
		return false
	}
	if j.Kind == "video" {
		return true // yt-dlp names its own files; trust the status
	}
	st, err := os.Stat(filepath.Join(j.Dir, j.Name))
	if err != nil || st.IsDir() || st.Size() == 0 {
		return false
	}
	return j.Size <= 0 || st.Size() == j.Size
}

// FindDone returns a finished download of the same link whose file is still
// complete on disk (failed, empty or partial files do not count).
func (m *Manager) FindDone(rawURL string) *JobView {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.findDoneLocked(rawURL)
}

func (m *Manager) findDoneLocked(rawURL string) *JobView {
	key := driveKey(rawURL) // the same Drive file under another link form
	for i := len(m.jobs) - 1; i >= 0; i-- {
		j := m.jobs[i]
		if j.Status == StatusDone && j.Kind != kindPDFCapture && (j.URL == rawURL || key != "" && driveKey(j.URL) == key) && j.completeOnDisk() {
			v := j.viewLocked()
			return &v
		}
	}
	return nil
}

func (m *Manager) Add(r AddRequest) (JobView, error) {
	r.URL = strings.TrimSpace(r.URL)
	u, err := url.Parse(r.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return JobView{}, errors.New("Please enter a valid http:// or https:// link.")
	}
	if driveNonFilePage(r.URL) {
		return JobView{}, errDrivePage
	}
	if oneShotPage(r.URL) {
		return JobView{}, errors.New(msgOneShotPage)
	}
	if !r.Force {
		if d := m.FindDone(r.URL); d != nil {
			return JobView{}, &DuplicateError{Name: d.Name}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return JobView{}, errors.New("BDDM is shutting down")
	}
	want := sanitizeName(r.Filename)
	sum, err := parseUserChecksum(r.Hash)
	if err != nil {
		return JobView{}, err
	}
	j := &Job{
		ID: newID(), URL: r.URL, Referer: strings.TrimSpace(r.Referer), Cookie: strings.TrimSpace(r.Cookie),
		Want: want, Dir: m.settings.dirFor(firstNonEmpty(want, filenameFromURL(r.URL))), Parts: m.settings.Parts,
		Status: StatusQueued, Started: time.Now(), AutoDir: true,
		Mirrors: cleanMirrors(r.URL, r.Mirrors), Hash: sum, PageTitle: cleanValue(strings.TrimSpace(r.Title), 1024),
	}
	j.applyBrowser(r)
	m.jobs = append(m.jobs, j)
	m.scheduleLocked()
	m.saveHistoryLocked()
	return j.viewLocked(), nil
}

// List returns the jobs shown on the Downloads tab (newest first).
func (m *Manager) List() []JobView {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Queue order: the order scheduleLocked starts them in.
	pos := map[*Job]int{}
	for _, j := range m.queuedLocked() {
		pos[j] = len(pos) + 1
	}
	out := []JobView{}
	for i := len(m.jobs) - 1; i >= 0; i-- {
		if !m.jobs[i].Removed && m.jobs[i].Status != StatusPending {
			v := m.jobs[i].viewLocked()
			if p := pos[m.jobs[i]]; p > 0 {
				v.QueuePos, v.QueueLen = p, len(pos)
			}
			out = append(out, v)
		}
	}
	return out
}

// History returns every job ever added, newest first, filtered by q.
func (m *Manager) History(q string) []JobView {
	q = strings.ToLower(strings.TrimSpace(q))
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []JobView{}
	for i := len(m.jobs) - 1; i >= 0; i-- {
		j := m.jobs[i]
		if j.Status == StatusPending {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(j.displayName()), q) && !strings.Contains(strings.ToLower(j.URL), q) {
			continue
		}
		out = append(out, j.viewLocked())
	}
	return out
}

// ClearHistory forgets every job that is not waiting to be finished.
func (m *Manager) ClearHistory() {
	m.mu.Lock()
	defer m.mu.Unlock()
	keep := m.jobs[:0]
	for _, j := range m.jobs {
		unfinished := j.Status == StatusQueued || j.Status == StatusDownloading || j.Status == StatusPaused ||
			j.Status == StatusPending || j.Status == StatusSeeding
		if j.cancel != nil || (unfinished && !j.Removed) {
			keep = append(keep, j)
		}
	}
	m.jobs = keep
	m.saveHistoryLocked()
}

func (m *Manager) Pause(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.findLocked(id)
	if j == nil {
		return errors.New("download not found")
	}
	switch j.Status {
	case StatusDownloading, StatusSeeding:
		j.stopReason = "pause"
		if j.cancel != nil {
			j.cancel()
		}
		j.Status = StatusPaused
	case StatusQueued:
		j.Status = StatusPaused
	default:
		return nil
	}
	m.saveHistoryLocked()
	return nil
}

// RunningCount is the number of downloads in progress (not queued or paused).
func (m *Manager) RunningCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, j := range m.jobs {
		if !j.Removed && j.Status == StatusDownloading {
			n++
		}
	}
	return n
}

// PauseAllRunning pauses every download that is in progress and waits until
// their progress has been saved. Queued items are left as they are (Shutdown
// will pause them when BDDM exits).
func (m *Manager) PauseAllRunning() {
	m.mu.Lock()
	var running []*Job
	for _, j := range m.jobs {
		if j.Removed || (j.Status != StatusDownloading && j.Status != StatusSeeding) {
			continue
		}
		j.stopReason = "pause"
		if j.cancel != nil {
			j.cancel()
		}
		j.Status = StatusPaused
		running = append(running, j)
	}
	m.saveHistoryLocked()
	m.mu.Unlock()
	for _, j := range running {
		m.waitStopped(j)
	}
	m.mu.Lock()
	m.saveHistoryLocked()
	m.mu.Unlock()
}

func (m *Manager) waitStopped(j *Job) {
	m.mu.Lock()
	ch := j.finished
	running := j.cancel != nil
	m.mu.Unlock()
	if running && ch != nil {
		select {
		case <-ch:
		case <-time.After(20 * time.Second):
		}
	}
}

func (m *Manager) Resume(id string) error {
	m.mu.Lock()
	j := m.findLocked(id)
	m.mu.Unlock()
	if j == nil {
		return errors.New("download not found")
	}
	m.waitStopped(j) // a pause may still be closing the file
	m.mu.Lock()
	defer m.mu.Unlock()
	if j.Status == StatusPaused || j.Status == StatusError {
		j.Error = ""
		j.Removed = false
		if j.Kind == kindTorrent {
			if err := m.startTorrentLocked(j); err != nil {
				j.Status = StatusError
				j.Error = err.Error()
				m.saveHistoryLocked()
				return err
			}
			m.saveHistoryLocked()
			return nil
		}
		j.Status = StatusQueued
		if g := m.findGroupLocked(j.Group); j.Group != "" && g != nil && g.Notified {
			g.Notified = false // tell about the folder again when it is done
			m.saveGroupsLocked()
		}
		m.scheduleLocked()
		m.saveHistoryLocked()
	}
	return nil
}

func (m *Manager) Delete(id string, deleteFile bool) error {
	m.mu.Lock()
	j := m.findLocked(id)
	if j == nil {
		m.mu.Unlock()
		return errors.New("download not found")
	}
	if j.cancel != nil {
		j.stopReason = "delete"
		j.cancel()
	}
	m.mu.Unlock()
	m.waitStopped(j)

	m.mu.Lock()
	defer m.mu.Unlock()
	j.Removed = true
	if j.Status == StatusDownloading || j.Status == StatusQueued || j.Status == StatusSeeding {
		j.Status = StatusPaused
	}
	if deleteFile && j.Kind == kindTorrent {
		m.removeTorrentFiles(j)
		m.removeTorrentMeta(j)
	} else if deleteFile && j.Kind == "video" {
		removeVideoFiles(j)
	} else if deleteFile && j.Name != "" {
		full := filepath.Join(j.Dir, j.Name)
		if j.Status == StatusDone {
			if fi, err := os.Stat(full); err == nil && fi.IsDir() && j.Kind == kindPDFCapture {
				_ = os.RemoveAll(full) // "Save as PDF" as PNG images: their folder
			} else {
				_ = os.Remove(full)
			}
		}
		_ = os.Remove(full + ".part")
		_ = os.Remove(full + ".bddm.json")
	}
	m.scheduleLocked()
	m.saveHistoryLocked()
	return nil
}

// FilePath returns the folder of a job and, when it is finished, its file.
func (m *Manager) FilePath(id string) (dir, file string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.findLocked(id)
	if j == nil {
		return "", "", errors.New("download not found")
	}
	if j.Kind == kindTorrent {
		dir, file := torrentDataPath(j)
		return dir, file, nil
	}
	if j.Status == StatusDone && j.Name != "" {
		f := filepath.Join(j.Dir, j.Name)
		if fileExists(f) {
			return j.Dir, f, nil
		}
	}
	return j.Dir, "", nil
}

// Shutdown pauses everything and saves state. Safe to call more than once.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.closing = true
	var running []*Job
	for _, j := range m.jobs {
		if j.cancel != nil {
			j.stopReason = "shutdown"
			j.cancel()
			running = append(running, j)
		}
		if j.Kind == kindTorrent {
			if j.Status == StatusDownloading || j.Status == StatusSeeding || j.Status == StatusQueued {
				j.Interrupted = true // resume or keep seeding on the next start
			}
			continue
		}
		if j.cancel != nil || j.Status == StatusQueued {
			j.Interrupted = true // continues when BDDM starts again
		}
	}
	m.mu.Unlock()
	for _, j := range running {
		m.waitStopped(j)
	}
	m.mu.Lock()
	for _, j := range m.jobs {
		if j.Kind == kindTorrent {
			continue
		}
		if j.Status == StatusDownloading || j.Status == StatusQueued {
			j.Status = StatusPaused
		}
	}
	m.closeTorrentClientLocked()
	m.saveHistoryLocked()
	m.mu.Unlock()
}

func (m *Manager) scheduleLocked() {
	if m.closing {
		return
	}
	// Google Drive files have their own limit (driveRate: 4 at a time, fewer
	// while Google asks to slow down); other downloads use the setting.
	active, activeDrive := 0, 0
	for _, j := range m.jobs {
		if j.cancel != nil && j.Kind != kindTorrent {
			if isDriveJob(j) {
				activeDrive++
			} else {
				active++
			}
		}
	}
	driveLimit := driveRate.limit()
	for _, j := range m.queuedLocked() {
		if active >= m.settings.MaxActive && activeDrive >= driveLimit {
			return
		}
		if j.Status != StatusQueued || j.Removed || j.cancel != nil || j.Kind == kindTorrent {
			continue
		}
		if isDriveJob(j) {
			if activeDrive >= driveLimit {
				continue
			}
			activeDrive++
		} else {
			if active >= m.settings.MaxActive {
				continue
			}
			active++
		}
		m.startLocked(j)
	}
}

// driveRate is the adaptive number of Google Drive files downloaded at the
// same time: 4, one less each time Google sends its "too many requests"
// answer (down to 1), one more again after 2 calm minutes.
var driveRate = &adaptiveLimit{max: maxActiveDrive, cur: maxActiveDrive}

type adaptiveLimit struct {
	mu      sync.Mutex
	max     int
	cur     int
	changed time.Time
}

func (a *adaptiveLimit) limit() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cur < a.max && time.Since(a.changed) > 2*time.Minute {
		a.cur++ // recover slowly
		a.changed = time.Now()
		log.Printf("Google Drive: now %d files at a time", a.cur)
	}
	return a.cur
}

// busy: Google asked to slow down.
func (a *adaptiveLimit) busy() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Since(a.changed) < 20*time.Second {
		return // one slow-down per burst
	}
	if a.cur > 1 {
		a.cur--
		log.Printf("Google Drive is limiting requests: now %d files at a time", a.cur)
	}
	a.changed = time.Now()
}

func isGoogleHost(h string) bool {
	h = strings.ToLower(h)
	return h == "google.com" || strings.HasSuffix(h, ".google.com") || strings.HasSuffix(h, ".googleusercontent.com")
}

// isDriveJob: a Google Drive file or document (not a video).
func isDriveJob(j *Job) bool {
	if j.Kind == "video" {
		return false
	}
	l, ok := parseDriveLink(j.URL)
	return ok && l.Kind != "folder"
}

func (m *Manager) startLocked(j *Job) {
	ctx, cancel := context.WithCancel(context.Background())
	j.cancel = cancel
	j.finished = make(chan struct{})
	j.stopReason = ""
	j.Status = StatusDownloading
	j.Error = ""
	j.Checked = ""
	j.liveMu.Lock()
	j.speed = 0
	j.xferStart = time.Time{}
	j.restarts = 0
	j.liveMu.Unlock()
	finished := j.finished
	go func() {
		err := m.runJob(ctx, j)
		cancel()

		m.mu.Lock()
		reason := j.stopReason
		j.cancel = nil
		j.liveMu.Lock()
		j.speed = 0
		j.liveMu.Unlock()
		var title, body string
		switch {
		case err == nil:
			j.Status = StatusDone
			j.Finished = time.Now()
			if j.Size <= 0 {
				j.Size = j.done.Load()
			}
			j.done.Store(j.Size)
			title, body = "Download complete", j.Name
			log.Printf("[%s] done", j.Name)
		case ctx.Err() != nil && reason != "":
			if j.Status == StatusDownloading {
				j.Status = StatusPaused
			}
		default:
			j.Status = StatusError
			j.Error = err.Error()
			reason, fix := explainError(j.Error)
			title, body = "Download failed", j.displayName()+": "+reason
			if fix != "" {
				body += " " + fix
			}
			log.Printf("[%s] error: %v", j.displayName(), err)
		}
		if j.Group != "" {
			// A file of a folder download: one message for the whole folder.
			m.groupFinishedLocked(j.Group)
		} else if title != "" {
			kind := "ok"
			if j.Status == StatusError {
				kind = "bad"
			}
			m.announceLocked(title, body, kind)
		}
		m.scheduleLocked()
		m.saveHistoryLocked()
		m.mu.Unlock()
		close(finished)
	}()
}

// nameInUseLocked reports whether another (not removed, not finished) job
// already owns dir/name, so two jobs never write the same file.
func (m *Manager) nameInUseLocked(dir, name, exceptID string) bool {
	for _, o := range m.jobs {
		if o.ID == exceptID || o.Removed || o.Status == StatusDone || o.Name == "" {
			continue
		}
		if strings.EqualFold(o.Dir, dir) && strings.EqualFold(o.Name, name) {
			return true
		}
	}
	return false
}

// numberedName turns "file.zip" into "file (1).zip".
func numberedName(base string, i int) string {
	if i == 0 {
		return base
	}
	ext := filepath.Ext(base)
	if ext == base || len(ext) > 10 {
		ext = ""
	}
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(base, ext), i, ext)
}

// chooseOutputLocked picks the file name for a new job. If an unfinished
// download of the same URL is found on disk it is resumed instead.
// When an unfinished download of the same URL is found but the file on the
// server changed, it is thrown away and the name used again (changed says why).
func (m *Manager) chooseOutputLocked(dir, base, rawURL string, size int64, etag, lastMod, jobID string) (name string, st *State, resuming bool, changed string) {
	for i := 0; ; i++ {
		name := numberedName(base, i)
		if m.nameInUseLocked(dir, name, jobID) {
			continue
		}
		full := filepath.Join(dir, name)
		partPath := full + ".part"
		statePath := full + ".bddm.json"

		st, err := loadState(statePath)
		if err == nil && st.URL == rawURL && fileExists(partPath) && !fileExists(full) {
			if why := fileChanged(st, size, etag, lastMod); why != "" {
				discardPartial(full)
				return name, nil, false, why
			}
			st.Name = name
			return name, st, true, ""
		}
		if fileExists(full) || fileExists(partPath) || fileExists(statePath) {
			continue
		}
		return name, nil, false, ""
	}
}

// runJob does the whole download of one job. It returns nil when the file is
// complete, or an error (ctx.Err() when it was paused).
func (m *Manager) runJob(ctx context.Context, j *Job) error {
	if j.Kind == "video" {
		return m.runVideo(ctx, j)
	}
	m.mu.Lock()
	rawURL, dir, nParts := j.URL, j.Dir, j.Parts
	hdr := j.browserHeaders()
	probed := j.probed
	if probed != nil && time.Since(j.probedAt) > probedFresh {
		probed = nil
	}
	j.probed = nil
	m.mu.Unlock()
	if driveNonFilePage(rawURL) {
		return errDrivePage
	}
	j.liveMu.Lock()
	j.label = filenameFromURL(rawURL)
	j.liveMu.Unlock()

	j.setMessage("Connecting...")
	var info probeInfo
	var err error
	// Google Drive / Docs: the link to download from is worked out on every
	// start (Drive links of big files change).
	fetchURL := ""
	m.mu.Lock()
	mirrors := append([]string(nil), j.Mirrors...)
	userSum := j.Hash
	m.mu.Unlock()
	dl, isDrive := parseDriveLink(rawURL)
	if isDrive && dl.Kind == "folder" {
		return errors.New("This is a Google Drive folder. Paste it again to choose the files to download.")
	}
	if isDrive && dl.Kind == "doc" {
		m.mu.Lock()
		fetchURL = docExportURL(dl, j.Export)
		m.mu.Unlock()
	}
	// first: the probe's answer, kept open; its data becomes the start of
	// the file (one request less for links that work only a few times).
	var first *http.Response
	defer func() {
		if first != nil {
			first.Body.Close()
		}
	}()
	useProbed := probed != nil && !isDrive && fetchURL == ""
	if useProbed {
		info = *probed // the "Download File Info" window just asked the server
	}
	for try := 1; try <= connectTries && !useProbed; try++ {
		switch {
		case isDrive && dl.Kind == "file":
			j.setMessage("Getting the Google Drive download link...")
			fetchURL, info, err = resolveDrive(ctx, m.client, dl, hdr)
			if err != nil && isDriveFinal(err) {
				return err // private, missing or over quota: retrying does not help
			}
			if err == nil && info.NoCookie {
				hdr.Cookie = "" // a public file the signed-in account cannot open
			}
		case isDrive && dl.Kind == "doc":
			j.setMessage("Asking Google to export the document...")
			info, err = probeDoc(ctx, m.client, fetchURL, hdr)
			if err != nil && err.Error() == msgDrivePrivate && (hdr.Cookie != "" || dl.AuthUser != "") {
				// Maybe a public document the signed-in account cannot open.
				anon, al := hdr, dl
				anon.Cookie, al.AuthUser = "", ""
				m.mu.Lock()
				anonURL := docExportURL(al, j.Export)
				m.mu.Unlock()
				if info2, err2 := probeDoc(ctx, m.client, anonURL, anon); err2 == nil {
					info, err, hdr, fetchURL = info2, nil, anon, anonURL
				}
			}
			if err != nil && isDriveFinal(err) {
				return err
			}
		case fetchURL != "":
			info, err = probe(ctx, m.client, fetchURL, hdr)
		default:
			if first != nil {
				first.Body.Close()
			}
			n := probeFirst
			if j.Name != "" && fileExists(filepath.Join(dir, j.Name)+".bddm.json") {
				n = 1 // continuing: only the first byte (the data is on disk)
			}
			if isOneShot(rawURL, hdr.Referer) {
				n = 0 // works once: one plain request for the whole file (oneshot.go)
			}
			info, first, err = probeKeep(ctx, m.client, rawURL, hdr, n)
			if n == 0 && err == nil {
				info.CanRange = false // no second connection
			}
		}
		if err == nil || ctx.Err() != nil {
			break
		}
		var hs *httpStatusError
		if !isDrive && errors.As(err, &hs) && isExpiredStatus(hs.code) {
			break // asking again does not help: the link itself is refused
		}
		if isNetErr(err) {
			// No internet: wait for it (this is not a failed try).
			waited, werr := waitOnline(ctx, j, serverAddr(firstNonEmpty(fetchURL, rawURL)))
			if werr != nil {
				return werr
			}
			if waited {
				try--
				continue
			}
		}
		if try < connectTries {
			wait := backoff(try)
			if errors.Is(err, errDriveBusy) {
				driveRate.busy()
				// Google asks to slow down: wait longer (20s, 40s, 60s ...).
				wait = min(time.Duration(try)*20*time.Second, 90*time.Second)
				j.setMessage("Google Drive is limiting downloads; waiting %s before trying again (%d/%d)...", wait, try, connectTries)
				select {
				case <-ctx.Done():
				case <-time.After(wait):
				}
				continue
			}
			j.setMessage("Could not connect (%v); retrying in %s (%d/%d)...", err, wait, try, connectTries)
			select {
			case <-ctx.Done():
			case <-time.After(wait):
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		if isDrive {
			return err // Drive errors explain themselves
		}
		var hs *httpStatusError
		if errors.As(err, &hs) && isExpiredStatus(hs.code) {
			if j.Name != "" && fileExists(filepath.Join(dir, j.Name)+".part") {
				return errors.New(msgLinkExpired)
			}
			return fmt.Errorf("The server refused the download (HTTP %d %s).", hs.code, http.StatusText(hs.code))
		}
		return fmt.Errorf("could not connect: %w", err)
	}
	// What identifies the file's version on the server. Google's links
	// change on every start, so for Drive only the size counts.
	etag, lastMod := info.ETag, info.LastModified
	if isDrive {
		etag, lastMod = "", ""
	}

	if j.Name == "" {
		m.mu.Lock()
		if j.AutoDir {
			// Now the real name is known: the folder of its file type.
			dir = m.settings.dirFor(firstNonEmpty(j.Want, info.Filename))
			j.Dir = dir
			j.AutoDir = false
		}
		m.mu.Unlock()
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("cannot create folder: %w", err)
	}

	// Pick the file name and the plan (new or resumed) while holding the lock,
	// so two jobs never pick the same name.
	m.mu.Lock()
	var (
		name     string
		st       *State
		resuming bool
	)
	changed := ""
	if j.Name != "" && !m.nameInUseLocked(dir, j.Name, j.ID) {
		full := filepath.Join(dir, j.Name)
		if s, err := loadState(full + ".bddm.json"); err == nil && s.URL == rawURL && fileExists(full+".part") {
			if changed = fileChanged(s, info.Size, etag, lastMod); changed != "" {
				// The file on the server is not the one we started: the old
				// parts would make a broken file. Start again.
				discardPartial(full)
				name = j.Name
			} else {
				name, st, resuming = j.Name, s, true
			}
		} else if !fileExists(full) && !fileExists(full+".part") && !fileExists(full+".bddm.json") {
			name = j.Name // nothing on disk yet: start fresh with the same name
		}
	}
	if name == "" {
		base := j.Want
		if base == "" || (uselessName(base) && info.Filename != "" && !uselessName(info.Filename)) {
			// The server's name is better than a "video.mp4" from the browser.
			if base != "" && path.Ext(info.Filename) == "" {
				info.Filename += path.Ext(base)
			}
			base = firstNonEmpty(info.Filename, base)
		}
		// A link with no useful name ("stream.mp4"): the page's title (names.go).
		base = nameWithTitle(base, j.PageTitle)
		if base == "" {
			base = "download.bin"
		}
		name, st, resuming, changed = m.chooseOutputLocked(dir, base, rawURL, info.Size, etag, lastMod, j.ID)
	}
	if !resuming {
		parts := 1
		if info.CanRange && info.Size > 0 {
			parts = nParts
			if info.Size < int64(parts) || info.Size < smallFile || (isDrive && info.Size < smallDriveFile) {
				parts = 1 // small files: one connection is enough (and Drive likes fewer)
			}
		}
		if parts == 1 {
			nParts = 1
		}
		st = &State{URL: rawURL, Size: info.Size, Name: name, Parts: splitParts(info.Size, parts), ETag: etag, LastModified: lastMod}
		j.done.Store(0)
	}
	// Enough room on the disk for the rest of the file?
	need := st.Size
	if resuming {
		for _, p := range st.Parts {
			need -= p.Written
		}
	}
	if err := checkFreeSpace(dir, need); err != nil {
		m.mu.Unlock()
		return err
	}
	j.Name = name
	j.Size = st.Size
	j.liveMu.Lock()
	j.label = name
	j.liveMu.Unlock()
	full := filepath.Join(dir, name)
	if changed != "" {
		j.setMessage("The file on the server changed (%s); downloading it again from the start...", changed)
	}
	f, err := openPartFile(full+".part", st.Size, resuming)
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("could not create file: %w", err)
	}
	host := hostOf(rawURL)
	if fetchURL != "" {
		host = hostOf(fetchURL)
	}
	conns := 1 << 30
	if hl := hostLimit(host, nParts); hl < nParts {
		conns = hl // this server refused more connections a short while ago
		j.setMessage("Using %d connections (this server limits connections).", hl)
	}
	d := &download{
		job: j, State: st, StatePath: full + ".bddm.json", File: f, client: m.client, hdr: hdr,
		CanRange: info.CanRange || (resuming && len(st.Parts) > 1),
		limiter:  connLimiter{limit: conns}, Workers: nParts, fetchURL: fetchURL, host: host,
	}
	if resuming && len(st.Parts) == 1 {
		d.CanRange = info.CanRange
	}
	if st.Sum == nil && info.Sum != nil && !isDrive {
		st.Sum = info.Sum
	}
	err = d.save()
	m.dirty = true
	m.mu.Unlock()
	if err != nil {
		f.Close()
		return fmt.Errorf("could not save state: %w", err)
	}

	if resuming {
		j.setMessage("Resuming...")
	}
	// Mirrors: other links to the same file share the work.
	var useMirrors []string
	if len(mirrors) > 0 && !isDrive && d.CanRange && st.Size > 0 {
		j.setMessage("Checking %d mirror link(s)...", len(mirrors))
		useMirrors = usableMirrors(ctx, j, m.client, hdr, mirrors, st.Size)
		if len(useMirrors) < len(mirrors) {
			log.Printf("[%s] using %d of %d mirrors", name, len(useMirrors), len(mirrors))
		}
	}
	d.setSources(useMirrors)
	if len(useMirrors) > 0 {
		j.setMessage("Downloading from %d links (the main link and %d mirror(s))...", len(useMirrors)+1, len(useMirrors))
	}
	if first != nil && !resuming && fetchURL == "" {
		// The probe's answer starts at byte 0: it becomes the first part.
		a, _, ok := contentRange(first.Header.Get("Content-Range"))
		if (first.StatusCode == http.StatusPartialContent && ok && a == 0) ||
			(first.StatusCode == http.StatusOK && len(st.Parts) == 1) {
			d.mu.Lock()
			d.first, first = first, nil
			d.mu.Unlock()
		}
	}
	j.liveMu.Lock()
	j.xferStart, j.xferFrom = time.Now(), j.done.Load()
	j.liveMu.Unlock()
	err = d.run(ctx)
	d.mu.Lock()
	if d.first != nil {
		d.first.Body.Close() // not used
		d.first = nil
	}
	d.mu.Unlock()
	if err == nil {
		// A checksum (yours or the server's): check the file, and download
		// damaged pieces again.
		want := userSum
		if want == nil {
			d.mu.Lock()
			want = d.State.Sum
			d.mu.Unlock()
		}
		if want != nil {
			var line string
			line, err = d.verify(ctx, full+".part", want)
			m.mu.Lock()
			j.Checked = line
			m.mu.Unlock()
			var hm *hashMismatchError
			if errors.As(err, &hm) && want.From != "user" && isDrive {
				err = nil // never fail a Google Drive file on Google's own headers
			}
		}
	}
	_ = d.save()
	f.Close()
	if err != nil && isDrive && dl.Kind == "file" && ctx.Err() == nil {
		// Ask Drive again: it explains a refusal (download limit, private).
		if _, _, rerr := resolveDrive(ctx, m.client, dl, hdr); rerr != nil && isDriveFinal(rerr) {
			return rerr
		}
	}
	if err != nil && ctx.Err() == nil {
		j.liveMu.Lock()
		j.restarts++
		again := j.restarts <= 2
		j.liveMu.Unlock()
		switch {
		case errors.Is(err, errFileChanged) && again:
			// Another version of the file appeared on the server while
			// downloading: throw the mixed parts away and start again.
			discardPartial(full)
			j.setMessage("The file on the server changed while downloading; starting again from the beginning...")
			return m.runJob(ctx, j)
		case errors.Is(err, errLinkExpired) && isDrive && again:
			// Drive links last a few hours: get a new one and continue.
			j.setMessage("The Google Drive link expired; getting a new one...")
			return m.runJob(ctx, j)
		case errors.Is(err, errLinkExpired):
			return errors.New(msgLinkExpired)
		}
	}
	var mismatch *hashMismatchError
	if err != nil && !errors.As(err, &mismatch) {
		return err
	}
	m.mu.Lock()
	if j.Size <= 0 && d.State.Size > 0 {
		j.Size = d.State.Size // the size was not known before the download
	}
	m.mu.Unlock()
	if mismatch != nil {
		// Keep the file (the user decides), but say clearly it does not match.
		_ = os.Rename(full+".part", full)
		_ = os.Remove(full + ".bddm.json")
		return mismatch
	}
	if err := os.Rename(full+".part", full); err != nil {
		return fmt.Errorf("downloaded, but could not rename file: %w", err)
	}
	_ = os.Remove(full + ".bddm.json")
	return nil
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

type reqHeaders struct {
	Referer string
	Cookie  string
	Extra   map[string]string // more headers (they win over the defaults)
	UA      string            // the browser's User-Agent ("" = BDDM's default)
	Lang    string            // the browser's Accept-Language
	Jar     cookieSet         // cookies of every host (redirects to a CDN)
}

func newClient() *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		// Tries every address of a server name, and public DNS when the
		// computer's DNS fails (netwatch.go).
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Use HTTP/1.1 so every part gets its own TCP connection (like IDM).
		// With HTTP/2 all parts share one connection, so one stall stalls them all.
		ForceAttemptHTTP2:   false,
		TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{},
		MaxConnsPerHost:     0,
		MaxIdleConnsPerHost: 32,
	}
	return &http.Client{Transport: transport, CheckRedirect: checkRedirect}
}

func newRequest(ctx context.Context, method, rawURL string, h reqHeaders) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if len(h.Jar) > 0 {
		req = req.WithContext(withJar(ctx, h.Jar))
	}
	req.Header.Set("User-Agent", firstNonEmpty(h.UA, userAgent))
	req.Header.Set("Accept", defaultAccept)
	req.Header.Set("Accept-Language", firstNonEmpty(h.Lang, defaultLang))
	req.Header.Set("Accept-Encoding", "identity")
	if h.Referer != "" {
		req.Header.Set("Referer", h.Referer)
	}
	if c := mergeCookieHeader(h.Cookie, h.Jar.forURL(req.URL)); c != "" {
		req.Header.Set("Cookie", c)
	}
	for k, v := range h.Extra {
		if v != "" && !strings.EqualFold(k, "Accept-Encoding") && !strings.EqualFold(k, "Range") {
			req.Header.Set(k, v)
		}
	}
	return req, nil
}

type probeInfo struct {
	Size     int64
	CanRange bool
	Filename string
	NoCookie bool // Google Drive: works only without the browser's cookies
	// The server's version of the file (for "did it change?").
	ETag         string
	LastModified string
	Status       int       // HTTP status of the last answer (0 = no answer)
	Sum          *Checksum // the file's checksum from the server's headers
}

func (p *probeInfo) setValidators(resp *http.Response) {
	if v := resp.Header.Get("ETag"); v != "" {
		p.ETag = v
	}
	if v := resp.Header.Get("Last-Modified"); v != "" {
		p.LastModified = v
	}
}

// probe asks the server for the size, name and range support of a link.
func probe(ctx context.Context, client *http.Client, rawURL string, h reqHeaders) (probeInfo, error) {
	info, resp, err := probeKeep(ctx, client, rawURL, h, 1)
	if resp != nil {
		resp.Body.Close()
	}
	return info, err
}

// probeFirst: how much of the file the first request of a new download
// asks for (its data is kept: see probeKeep).
var probeFirst int64 = 2 << 20

// probeKeep asks with one GET for the first n bytes (no separate HEAD
// request: some links work only a few times). When it works, the answer
// is returned still open, so its data can become the start of the file;
// the caller closes it.
func probeKeep(ctx context.Context, client *http.Client, rawURL string, h reqHeaders, n int64) (probeInfo, *http.Response, error) {
	var info probeInfo
	get, err := newRequest(ctx, http.MethodGet, rawURL, h)
	if err != nil {
		return info, nil, err
	}
	if n > 0 {
		get.Header.Set("Range", fmt.Sprintf("bytes=0-%d", n-1))
	}
	resp, err := client.Do(get)
	if err != nil {
		return info, nil, err
	}
	noRanges := false
	switch code := resp.StatusCode; {
	case n <= 0:
		// No Range was asked for: nothing to ask again without it.
	case code == http.StatusRequestedRangeNotSatisfiable || code == http.StatusBadRequest || code == http.StatusForbidden || code == http.StatusNotImplemented:
		// Some servers refuse any Range header: ask once more without it.
		drain(resp)
		if get, err = newRequest(ctx, http.MethodGet, rawURL, h); err != nil {
			return info, nil, err
		}
		if resp, err = client.Do(get); err != nil {
			return info, nil, err
		}
		noRanges = resp.StatusCode < 400 // it works only without a range
	}
	info.Status = resp.StatusCode
	if resp.StatusCode >= 400 {
		drain(resp)
		return info, nil, &httpStatusError{code: resp.StatusCode}
	}
	info.setValidators(resp)
	info.Filename = filenameFromHeaders(resp, resp.Request.URL.String())
	if resp.StatusCode == http.StatusPartialContent {
		a, b, okRange := contentRange(resp.Header.Get("Content-Range"))
		if size, ok := parseContentRange(resp.Header.Get("Content-Range")); ok {
			info.Size = size
		}
		info.CanRange = info.Size > 0
		info.Sum = fileChecksum(resp.Header, okRange && a == 0 && b == info.Size-1)
		return info, resp, nil
	}
	// 200: the whole file (the server ignored the Range header, or sent
	// everything because the range asked for was the whole file).
	if resp.ContentLength > 0 {
		info.Size = resp.ContentLength
	}
	info.CanRange = supportsBytes(resp) && info.Size > 0 && !noRanges
	info.Sum = fileChecksum(resp.Header, resp.StatusCode == http.StatusOK)
	return info, resp, nil
}

// drain reads a little of an unwanted answer (so the connection can be
// used again) and closes it.
func drain(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	resp.Body.Close()
}

func supportsBytes(resp *http.Response) bool {
	ar := strings.ToLower(strings.TrimSpace(resp.Header.Get("Accept-Ranges")))
	return ar == "bytes"
}

func parseContentRange(s string) (int64, bool) {
	// Example: "bytes 0-0/10485760"
	slash := strings.LastIndex(s, "/")
	if slash < 0 || slash+1 >= len(s) {
		return 0, false
	}
	total := s[slash+1:]
	if total == "*" {
		return 0, false
	}
	n, err := strconv.ParseInt(total, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func filenameFromHeaders(resp *http.Response, rawURL string) string {
	if name := filenameFromDisposition(resp.Header.Get("Content-Disposition")); name != "" {
		return name
	}
	return filenameFromURL(rawURL)
}

func filenameFromDisposition(h string) string {
	if h == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(h)
	if err != nil {
		return ""
	}
	if v, ok := params["filename*"]; ok {
		if name := sanitizeName(decodeRFC5987(v)); name != "" {
			return name
		}
	}
	if v, ok := params["filename"]; ok {
		return sanitizeName(v)
	}
	return ""
}

func decodeRFC5987(s string) string {
	// charset''percent-encoded-name
	parts := strings.SplitN(s, "''", 2)
	if len(parts) != 2 {
		return s
	}
	dec, err := url.QueryUnescape(parts[1])
	if err != nil {
		return s
	}
	return dec
}

func filenameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "download.bin"
	}
	name := path.Base(u.Path)
	if dec, err := url.PathUnescape(name); err == nil {
		name = dec
	}
	name = sanitizeName(name)
	if name == "" {
		return "download.bin"
	}
	return name
}

func sanitizeName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || name == "/" {
		return ""
	}
	invalid := `<>:"/\|?*`
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(invalid, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	return name
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func backoff(try int) time.Duration {
	// try=1 → 2s, then 4s, 8s, 16s, capped at 30s.
	d := time.Duration(2<<uint(try-1)) * time.Second
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

func retryAfter(resp *http.Response) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if sec, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && sec > 0 && sec < 600 {
			return time.Duration(sec) * time.Second
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// Saved progress (name.bddm.json)
// ---------------------------------------------------------------------------

type Part struct {
	From    int64 `json:"from"`
	To      int64 `json:"to"` // inclusive; -1 means unknown (until the end)
	Written int64 `json:"written"`
}

type State struct {
	URL   string `json:"url"`
	Size  int64  `json:"size"`
	Name  string `json:"name"`
	Parts []Part `json:"parts"`
	// The server's version of the file when the download started: if it
	// changes, the saved parts belong to another file.
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
	// Sum: the file's checksum as the server sent it (verify.go).
	Sum *Checksum `json:"sum,omitempty"`
}

// saveStateFile writes the progress file so that a power cut never leaves
// it half written.
func saveStateFile(p string, st *State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFileSync(p, data)
}

func loadState(p string) (*State, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	if st.URL == "" || len(st.Parts) == 0 {
		return nil, errors.New("invalid state file")
	}
	return &st, nil
}

func splitParts(size int64, n int) []Part {
	if n < 1 {
		n = 1
	}
	if size <= 0 {
		return []Part{{From: 0, To: -1}}
	}
	if size < int64(n) {
		n = 1
	}
	chunk := size / int64(n)
	parts := make([]Part, n)
	for i := 0; i < n; i++ {
		from := int64(i) * chunk
		to := from + chunk - 1
		if i == n-1 {
			to = size - 1
		}
		parts[i] = Part{From: from, To: to}
	}
	return parts
}

func openPartFile(p string, size int64, resume bool) (*os.File, error) {
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, err
	}
	if size > 0 {
		makeSparse(f)
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, err
		}
		if !resume || info.Size() != size {
			if err := f.Truncate(size); err != nil {
				f.Close()
				return nil, err
			}
		}
	} else if !resume {
		if err := f.Truncate(0); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

// ---------------------------------------------------------------------------
// One running download
// ---------------------------------------------------------------------------

// connLimiter lowers how many parts run at once when the server complains.
type connLimiter struct {
	mu    sync.Mutex
	limit int
	busy  int
}

func (l *connLimiter) acquire(ctx context.Context) error {
	for {
		l.mu.Lock()
		if l.busy < l.limit {
			l.busy++
			l.mu.Unlock()
			return nil
		}
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (l *connLimiter) release() {
	l.mu.Lock()
	l.busy--
	l.mu.Unlock()
}

// lower returns the new limit, or 0 if it did not change.
func (l *connLimiter) lower() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	cur := l.busy
	if l.limit < cur {
		cur = l.limit
	}
	if cur > 1 {
		l.limit = cur - 1
		return l.limit
	}
	l.limit = 1
	return 0
}

// Work stealing: when a connection finishes its part early, it takes over
// the second half of the part that will take longest to finish (IDM style),
// so all connections keep working until the end.
var (
	stealEnabled = true
	minSteal     = int64(1 << 20) // each half must be at least this big
	// Only split a part that still needs this long: a new connection costs
	// time to open, so splitting a part that is almost done only slows it.
	minStealTime = 2 * time.Second
)

// errPartDone stops reading a part that reached its (possibly new) end.
var errPartDone = errors.New("part reached its end")

// partLive is runtime-only information about one part (not saved).
type partLive struct {
	assigned bool      // a connection is working on it
	reserved int64     // end (exclusive) of a write in progress, 0 = none
	since    time.Time // when the current connection took it
	got      int64     // bytes since then (for its speed)
	src      int       // the link it downloads from (0 = main, then mirrors)

	// Slow-connection check (pieces.go).
	cancel    context.CancelFunc // closes the current request
	replacing bool               // closed because it was slow
	replaced  int                // how often
	lastGot   int64
	rate      float64 // bytes/s, smoothed
	slowSince time.Time
}

type download struct {
	job       *Job
	State     *State
	StatePath string
	File      *os.File
	CanRange  bool
	client    *http.Client
	hdr       reqHeaders
	first     *http.Response // the probe's open answer from byte 0 (runJob)
	limiter   connLimiter
	Workers   int    // connections to use (the "parts" setting)
	fellBack  bool   // switched to a single connection: never split again
	fetchURL  string // link to download from, when it differs from State.URL (Google Drive)
	host      string // server (for remembering how many connections it accepts)
	resets    int    // dropped connections in this run
	lastLower time.Time
	strict    bool   // never fall back to one connection: return the error (the caller handles it)
	shared    bool   // the job has several files: job.done is the sum (only add to it)
	what      string // "video", "audio": shown in the status message
	live      []partLive
	steals    int
	lastSave  time.Time
	saveMu    sync.Mutex

	// Links (pieces.go): sources[0] is the main link, then the mirrors.
	sources      []string
	srcDead      []bool
	srcFails     []int
	srcActive    []int
	typicalRate  float64 // median speed of the connections (checkSlow)
	replacedSlow int     // slow connections replaced

	mu sync.Mutex
}

// saveSoon saves the progress, at most every few seconds (flushing the file
// to the disk costs time).
func (d *download) saveSoon() {
	d.mu.Lock()
	due := time.Since(d.lastSave) >= stateSyncEvery
	d.mu.Unlock()
	if due {
		_ = d.save()
	}
}

// save flushes the downloaded data to the disk, then writes the progress
// file. After a crash or power cut the progress file never claims more than
// what is really on the disk.
func (d *download) save() error {
	d.saveMu.Lock() // one save at a time
	defer d.saveMu.Unlock()
	d.mu.Lock()
	d.lastSave = time.Now()
	snap := *d.State // what is written so far (copied before the flush)
	snap.Parts = append([]Part(nil), d.State.Parts...)
	d.mu.Unlock()
	if err := d.File.Sync(); err != nil {
		return err // the data the progress file points to must be on disk first
	}
	return saveStateFile(d.StatePath, &snap)
}

func (d *download) writtenLocked() int64 {
	var n int64
	for i := range d.State.Parts {
		n += d.State.Parts[i].Written
	}
	return n
}

// liveLocked returns the runtime info of part idx (growing the list as needed).
func (d *download) liveLocked(idx int) *partLive {
	for len(d.live) < len(d.State.Parts) {
		d.live = append(d.live, partLive{})
	}
	return &d.live[idx]
}

func partFinished(p Part) bool { return p.To >= 0 && p.From+p.Written > p.To }

// canStealLocked reports whether parts may be split right now.
func (d *download) canStealLocked() bool {
	if !stealEnabled || d.fellBack || !d.CanRange || d.State.Size <= 0 || d.Workers < 2 {
		return false
	}
	d.limiter.mu.Lock()
	limit := d.limiter.limit
	d.limiter.mu.Unlock()
	return limit >= d.Workers // the server is not limiting connections
}

// steal splits the active part that needs the most time to finish and gives
// its second half to the caller as a new part. Returns -1 if nothing is
// worth splitting.
func (d *download) steal() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.canStealLocked() {
		return -1
	}
	now := time.Now()
	// Average speed of the parts we can measure, for parts just started.
	var sum float64
	var cnt int
	for i := range d.State.Parts {
		lv := d.liveLocked(i)
		if el := now.Sub(lv.since).Seconds(); lv.assigned && el >= 1 {
			sum += float64(lv.got) / el
			cnt++
		}
	}
	avg := 1.0
	if cnt > 0 && sum > 0 {
		avg = sum / float64(cnt)
	}
	best, bestCur := -1, int64(0)
	bestTime := -1.0
	for i := range d.State.Parts {
		pt := d.State.Parts[i]
		lv := d.liveLocked(i)
		if !lv.assigned || pt.To < 0 {
			continue
		}
		cur := pt.From + pt.Written
		if lv.reserved > cur {
			cur = lv.reserved // never split inside a write in progress
		}
		rem := pt.To - cur + 1
		if rem < 2*minSteal {
			continue
		}
		speed := avg
		if el := now.Sub(lv.since).Seconds(); el >= 1 {
			speed = float64(lv.got) / el
		}
		if speed < 1 {
			speed = 1
		}
		t := float64(rem) / speed
		if t < minStealTime.Seconds() {
			continue
		}
		if t > bestTime {
			best, bestCur, bestTime = i, cur, t
		}
	}
	if best < 0 {
		return -1
	}
	victim := &d.State.Parts[best]
	rem := victim.To - bestCur + 1
	mid := bestCur + rem/2
	np := Part{From: mid, To: victim.To}
	victim.To = mid - 1 // the victim stops here (checked on every write)
	d.State.Parts = append(d.State.Parts, np)
	idx := len(d.State.Parts) - 1
	d.takeLocked(idx, now)
	d.steals++
	return idx
}

// connectionDropped: a server that keeps dropping connections while several
// are open often allows fewer; use one less (at most every 10 seconds).
func (d *download) connectionDropped(err error) {
	msg := strings.ToLower(err.Error())
	if !errors.Is(err, errStalled) && !strings.Contains(msg, "reset") && !strings.Contains(msg, "eof") &&
		!strings.Contains(msg, "closed") && !strings.Contains(msg, "refused") {
		return
	}
	d.mu.Lock()
	d.resets++
	lower := d.resets >= 3 && time.Since(d.lastLower) > 10*time.Second
	if lower {
		d.resets = 0
		d.lastLower = time.Now()
	}
	d.mu.Unlock()
	if lower {
		if n := d.limiter.lower(); n > 0 {
			hostLowered(d.host, n)
			d.job.setMessage("The server keeps dropping connections; now using %d at a time.", n)
		}
	}
}

// nextPart gives a free connection its next job: an unfinished part nobody
// works on, or else half of a slow part. Returns -1 when there is nothing.
func (d *download) nextPart() int {
	d.mu.Lock()
	for i := range d.State.Parts {
		lv := d.liveLocked(i)
		if !lv.assigned && !partFinished(d.State.Parts[i]) {
			d.takeLocked(i, time.Now())
			d.mu.Unlock()
			return i
		}
	}
	d.mu.Unlock()
	return d.steal()
}

func (d *download) release(idx int) {
	d.mu.Lock()
	lv := d.liveLocked(idx)
	d.dropSourceLocked(lv)
	lv.assigned = false
	d.mu.Unlock()
}

// partWriter writes one part's bytes. It never writes past the part's end,
// even when another connection moved that end while we were reading.
type partWriter struct {
	d   *download
	idx int
}

func (w *partWriter) Write(p []byte) (int, error) {
	d := w.d
	d.mu.Lock()
	pt := d.State.Parts[w.idx]
	off := pt.From + pt.Written
	short := false
	if pt.To >= 0 {
		allowed := pt.To - off + 1
		if allowed <= 0 {
			d.mu.Unlock()
			return 0, errPartDone
		}
		if int64(len(p)) > allowed {
			p = p[:allowed]
			short = true
		}
	}
	d.liveLocked(w.idx).reserved = off + int64(len(p))
	d.mu.Unlock()

	n, err := d.File.WriteAt(p, off)

	d.mu.Lock()
	d.State.Parts[w.idx].Written += int64(n)
	lv := d.liveLocked(w.idx)
	lv.reserved = 0
	lv.got += int64(n)
	finished := partFinished(d.State.Parts[w.idx])
	d.mu.Unlock()
	d.job.done.Add(int64(n))
	if err != nil {
		return n, err
	}
	if short || finished {
		return n, errPartDone
	}
	return n, nil
}

func (d *download) partSnapshot(idx int) Part {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.State.Parts[idx]
}

func (d *download) switchToSingle(keepPrefix bool, canRange bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var written int64
	if keepPrefix && len(d.State.Parts) > 0 && d.State.Parts[0].From == 0 {
		written = d.State.Parts[0].Written
	}
	to := int64(-1)
	if d.State.Size > 0 {
		to = d.State.Size - 1
	}
	d.State.Parts = []Part{{From: 0, To: to, Written: written}}
	d.live = nil
	clear(d.srcActive)
	d.CanRange = canRange
	d.fellBack = true
}

func (d *download) run(ctx context.Context) error {
	d.mu.Lock()
	if !d.shared {
		d.job.done.Store(d.writtenLocked())
	}
	size := d.State.Size
	d.mu.Unlock()

	stopProg := make(chan struct{})
	var progWG sync.WaitGroup
	progWG.Add(1)
	go func() {
		defer progWG.Done()
		d.progressLoop(stopProg, size)
	}()
	defer func() {
		close(stopProg)
		progWG.Wait()
	}()

	for attempt := 0; attempt < 3; attempt++ {
		d.mu.Lock()
		nParts := len(d.State.Parts)
		d.mu.Unlock()

		if d.shared {
			// the caller shows the message
		} else if d.what != "" {
			conns := min(nParts, max(d.Workers, 1))
			if conns > 1 {
				d.job.setMessage("Downloading %s with %d connections...", d.what, conns)
			} else {
				d.job.setMessage("Downloading %s...", d.what)
			}
		} else if links := d.mirrorsInUse(); links > 1 {
			d.job.setMessage("Downloading in %d parts from %d links...", nParts, links)
		} else if nParts > 1 {
			d.job.setMessage("Downloading in %d parts...", nParts)
		} else {
			d.job.setMessage("Downloading with a single connection...")
		}

		err := d.downloadParts(ctx)
		if err == nil {
			d.mu.Lock()
			written := d.writtenLocked()
			total := d.State.Size
			d.mu.Unlock()
			if total > 0 && written != total {
				return fmt.Errorf("incomplete download: got %d of %d bytes", written, total)
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		noRanges := errors.Is(err, errNoRanges)
		rejected := errors.Is(err, errFallback)
		if rejected {
			// 403: the server may only refuse many connections, or the link
			// expired. Ask once for the first byte to tell the two apart.
			from := d.State.URL
			if d.fetchURL != "" {
				from = d.fetchURL
			}
			if code := linkWorks(ctx, d.client, from, d.hdr); isExpiredStatus(code) {
				return &linkExpiredError{code: code}
			}
		}
		if !noRanges && !rejected || d.strict {
			return err
		}
		if nParts == 1 && rejected {
			// 403/429 on a single connection is a real failure.
			return err
		}

		canRange := rejected && !noRanges
		keepPrefix := canRange
		if noRanges {
			d.job.setMessage("Server does not support range requests; falling back to a single connection...")
		} else {
			d.job.setMessage("Server rejected multiple connections; falling back to a single connection...")
		}
		d.switchToSingle(keepPrefix, canRange)
		d.mu.Lock()
		if !d.shared {
			d.job.done.Store(d.writtenLocked())
		}
		d.mu.Unlock()
		_ = d.save()
	}
	return errors.New("could not finish download after falling back to a single connection")
}

func (d *download) downloadParts(ctx context.Context) error {
	d.mu.Lock()
	d.live = nil
	clear(d.srcActive)
	unfinished := 0
	for _, p := range d.State.Parts {
		if !partFinished(p) {
			unfinished++
		}
	}
	workers := unfinished
	if d.Workers > 0 && workers > d.Workers {
		workers = d.Workers // never more connections than the setting
	}
	if d.canStealLocked() && d.Workers > workers {
		workers = d.Workers // free connections will split slow parts
	}
	if workers < 1 {
		workers = 1
	}
	d.mu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	setErr := func(err error) {
		if err == nil {
			return
		}
		once.Do(func() {
			firstErr = err
			cancel()
		})
	}

	// Each worker is one connection: it downloads a part, then takes the
	// next free part or splits a slow one, until nothing is left.
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				idx := d.nextPart()
				if idx < 0 {
					return
				}
				err := d.downloadPart(ctx, idx)
				d.release(idx)
				if err != nil {
					if !errors.Is(err, context.Canceled) {
						setErr(err)
					}
					return
				}
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, p := range d.State.Parts {
		if p.To >= 0 && !partFinished(p) {
			return fmt.Errorf("part %d did not finish", i+1)
		}
	}
	return nil
}

func (d *download) downloadPart(ctx context.Context, idx int) error {
	var lastErr error
	rlCount := 0
	for try := 1; try <= maxTries; try++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := d.limiter.acquire(ctx); err != nil {
			return err
		}
		err := d.fetchPart(ctx, idx)
		d.limiter.release()
		if err == nil {
			p := d.partSnapshot(idx)
			if p.To < 0 {
				// Size was unknown and the server ended the answer cleanly:
				// the part is complete now (without this, the part never
				// counts as finished and is fetched again and again).
				d.mu.Lock()
				pt := &d.State.Parts[idx]
				pt.To = pt.From + pt.Written - 1
				if len(d.State.Parts) == 1 && d.State.Size <= 0 {
					d.State.Size = pt.Written
				}
				d.mu.Unlock()
				return nil
			}
			if partFinished(p) {
				return nil
			}
			err = errors.New("connection closed early")
		}
		var rl *rateLimitError
		if errors.As(err, &rl) {
			if n := d.limiter.lower(); n > 0 {
				hostLowered(d.host, n)
				d.job.setMessage("Server is limiting connections; now using %d at a time.", n)
			}
			wait := rl.wait
			if wait == 0 {
				wait = backoff(try)
			}
			try-- // a "too many requests" answer is not counted as a failed try
			rlCount++
			if rlCount > 30 {
				return fmt.Errorf("part %d: server kept refusing (too many requests)", idx+1)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errReplaced) {
			try-- // a slow connection was closed on purpose: just reconnect
			continue
		}
		// A mirror that fails is dropped; the part goes to another link.
		if d.mirrorFailed(idx, err) {
			try--
			continue
		}
		if errors.Is(err, errFallback) || errors.Is(err, errNoRanges) || errors.Is(err, context.Canceled) ||
			errors.Is(err, errFileChanged) || errors.Is(err, errLinkExpired) {
			return err
		}
		if isNetErr(err) {
			// No internet: wait until it is back (not a failed try).
			_, from := d.partSource(idx)
			waited, werr := waitOnline(ctx, d.job, serverAddr(from))
			if werr != nil {
				return werr
			}
			if waited {
				try--
				continue
			}
		}
		if errors.Is(err, errPieceDamaged) {
			lastErr = err
			d.job.setMessage("Part %d arrived damaged; downloading it again (%d/%d)...", idx+1, try, maxTries)
			continue
		}
		d.connectionDropped(err)
		lastErr = err
		wait := backoff(try)
		d.job.setMessage("Part %d: %v; retrying in %s (%d/%d)...", idx+1, err, wait, try, maxTries)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return fmt.Errorf("part %d failed after %d tries: %w", idx+1, maxTries, lastErr)
}

// checkSameFile compares an answer with the file this download started
// with: another size, ETag or date means the server has a new version.
func (d *download) checkSameFile(resp *http.Response, src int) error {
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil
	}
	d.mu.Lock()
	st := State{Size: d.State.Size, ETag: d.State.ETag, LastModified: d.State.LastModified}
	d.mu.Unlock()
	var size int64
	if resp.StatusCode == http.StatusPartialContent {
		size, _ = parseContentRange(resp.Header.Get("Content-Range"))
	} else if resp.ContentLength > 0 {
		size = resp.ContentLength
	}
	etag, lastMod := resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
	if d.fetchURL != "" || src > 0 {
		etag, lastMod = "", "" // Google Drive and mirrors: only the size counts
	}
	if why := fileChanged(&st, size, etag, lastMod); why != "" {
		log.Printf("[%s] the file on the server changed: %s", d.job.Name, why)
		return errFileChanged
	}
	return nil
}

func (d *download) fetchPart(ctx context.Context, idx int) error {
	p := d.partSnapshot(idx)
	start := p.From + p.Written
	end := p.To
	if end >= 0 && start > end {
		return nil
	}

	reqCtx, reqCancel := context.WithCancel(ctx)
	defer reqCancel()
	// A connection that is far slower than the others is closed by
	// checkSlow (pieces.go); then this part starts again on a new one.
	d.setRequest(idx, reqCancel)
	err := d.fetchFrom(reqCtx, reqCancel, idx, start, end)
	if d.endRequest(idx) && ctx.Err() == nil {
		return errReplaced
	}
	return err
}

func (d *download) fetchFrom(reqCtx context.Context, reqCancel context.CancelFunc, idx int, start, end int64) error {
	ctx := reqCtx
	src, from := d.partSource(idx)
	hdr := d.hdr
	if src > 0 {
		// The browser's cookies and headers belong to the main site.
		hdr = hdr.forMirror()
	}
	d.mu.Lock()
	canRange := d.CanRange
	multi := len(d.State.Parts) > 1
	var resp *http.Response
	if start == 0 && src == 0 && d.first != nil {
		resp, d.first = d.first, nil // the probe's answer: no new request
	}
	reused := resp != nil
	d.mu.Unlock()
	rangeRequested := false
	if resp != nil {
		// Closing the request (pause, slow or stalled connection) closes it.
		stop := context.AfterFunc(reqCtx, func() { resp.Body.Close() })
		defer stop()
		rangeRequested = resp.StatusCode == http.StatusPartialContent
	} else {
		req, err := newRequest(reqCtx, http.MethodGet, from, hdr)
		if err != nil {
			return err
		}
		if canRange && (start > 0 || end >= 0) {
			rangeRequested = true
			if end >= 0 {
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
			} else {
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-", start))
			}
		}
		if resp, err = d.client.Do(req); err != nil {
			return err
		}
	}
	defer resp.Body.Close()

	// Google Drive answers some requests with a web page (rate limit, sign-in)
	// instead of the file: never write that page into the file.
	if d.fetchURL != "" && strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") &&
		isGoogleHost(resp.Request.URL.Hostname()) {
		if resp.StatusCode == http.StatusTooManyRequests || strings.HasPrefix(resp.Request.URL.Path, "/sorry") {
			driveRate.busy()
			return &rateLimitError{wait: max(retryAfter(resp), 20*time.Second)}
		}
		return errors.New("Google Drive sent a web page instead of the file data")
	}

	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		// Too many requests / server busy: fewer connections, then again.
		if isGoogleHost(resp.Request.URL.Hostname()) {
			driveRate.busy()
		}
		return &rateLimitError{wait: retryAfter(resp)}
	case http.StatusUnauthorized, http.StatusNotFound, http.StatusGone:
		return &linkExpiredError{code: resp.StatusCode}
	}
	if err := d.checkSameFile(resp, src); err != nil {
		return err
	}
	d.noteChecksum(resp, src, rangeRequested)
	if rangeRequested {
		switch resp.StatusCode {
		case http.StatusPartialContent:
			// Good.
		case http.StatusOK:
			return errNoRanges
		case http.StatusForbidden:
			return errFallback
		default:
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
	} else if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden {
			return errFallback
		}
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	} else if multi {
		// Multi-part job received a full 200 response. Fall back.
		return errNoRanges
	} else if start > 0 {
		// Single connection without ranges always starts from the beginning.
		d.mu.Lock()
		d.State.Parts[idx].Written = 0
		if !d.shared {
			d.job.done.Store(0)
		}
		d.mu.Unlock()
		start = 0
	}

	var body io.Reader = resp.Body
	if end >= 0 {
		body = io.LimitReader(body, end-start+1)
	}

	// partWriter counts the bytes and stops at the part's current end, which
	// can move closer when another connection takes over half of this part.
	var w io.Writer = &partWriter{d: d, idx: idx}
	// A checksum for just this piece: check it as it arrives.
	var hw *hashWriter
	var pc *Checksum
	var pieceLen int64
	if resp.StatusCode == http.StatusPartialContent {
		if a, b, ok := contentRange(resp.Header.Get("Content-Range")); ok && a == start {
			if pc = pieceChecksum(resp.Header); pc != nil {
				pieceLen = b - a + 1
				hw = &hashWriter{w: w, h: newHash(pc.Algo)}
				w = hw
			}
		}
	}
	err := copyWithStall(ctx, reqCancel, w, body, func(int) {}, stallAfter)
	if errors.Is(err, errPartDone) {
		err = nil
	}
	if err == nil && reused {
		if p := d.partSnapshot(idx); p.To >= 0 && !partFinished(p) {
			return errReplaced // the probe's answer ended: go on with a new request
		}
	}
	if err == nil && hw != nil && hw.n == pieceLen && sumHex(pc.Algo, hw.h) != pc.Hex {
		// Damaged on the way: forget this piece and download it again.
		d.mu.Lock()
		d.State.Parts[idx].Written = start - d.State.Parts[idx].From
		d.mu.Unlock()
		d.job.done.Add(-hw.n)
		log.Printf("[%s] part %d: piece %d-%d did not match its %s (%s); downloading it again",
			d.job.Name, idx+1, start, start+pieceLen-1, pc.From, algoName(pc.Algo))
		return errPieceDamaged
	}
	return err
}

func copyWithStall(parent context.Context, cancelReq context.CancelFunc, dst io.Writer, src io.Reader, onBytes func(int), stallFor time.Duration) error {
	var lastRead atomic.Int64
	lastRead.Store(time.Now().UnixNano())

	readDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				lastRead.Store(time.Now().UnixNano())
				if _, werr := dst.Write(buf[:n]); werr != nil {
					readDone <- werr
					return
				}
				onBytes(n)
			}
			if err != nil {
				if err == io.EOF {
					readDone <- nil
					return
				}
				readDone <- err
				return
			}
		}
	}()

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case err := <-readDone:
			return err
		case <-tick.C:
			last := time.Unix(0, lastRead.Load())
			if time.Since(last) > stallFor {
				cancelReq()
				err := <-readDone
				if err == nil {
					return nil
				}
				return errStalled
			}
		case <-parent.Done():
			cancelReq()
			<-readDone
			return parent.Err()
		}
	}
}

func (d *download) progressLoop(stop <-chan struct{}, size int64) {
	var (
		lastDone = d.job.done.Load()
		smoothed float64
	)
	ticker := time.NewTicker(stateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			cur := d.job.done.Load()
			instant := float64(cur - lastDone) // bytes in the last second
			if instant < 0 {
				instant = 0
			}
			lastDone = cur
			if smoothed <= 0 {
				smoothed = instant
			} else {
				smoothed = 0.3*instant + 0.7*smoothed
			}
			d.job.liveMu.Lock()
			d.job.speed = smoothed
			d.job.liveMu.Unlock()
			d.checkSlow(time.Now())
			d.saveSoon()
		}
	}
}
