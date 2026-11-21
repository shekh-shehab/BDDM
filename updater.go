package main

// updater.go: look up the latest GitHub release, show a notice when a
// newer version is available, and install it only when the user presses
// Install.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	updateAPIURL    = "https://api.github.com/repos/shekh-shehab/BDDM/releases/latest"
	updatePeriod    = 6 * time.Hour
	updateTimeout   = 15 * time.Second
	updateMaxBody   = 1 << 20
	updateDlTimeout = 15 * time.Minute
	updateSumMax    = 64 << 10
	setupAssetName  = "bddm-setup.exe"
	sumAssetName    = "bddm-setup.exe.sha256"
)

// githubRelease is the fields we need from the GitHub releases API.
type githubRelease struct {
	TagName    string        `json:"tag_name"`
	Name       string        `json:"name"`
	HTMLURL    string        `json:"html_url"`
	Draft      bool          `json:"draft"`
	Prerelease bool          `json:"prerelease"`
	Assets     []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// ReleaseAsset is one file attached to a GitHub release.
type ReleaseAsset struct {
	Name string
	Size int64
	URL  string
}

// ReleaseInfo is a published (non-draft, non-prerelease) GitHub release.
type ReleaseInfo struct {
	Version string // 1.4.4, without a leading "v"
	Tag     string // the tag as GitHub sent it, e.g. v1.4.4
	URL     string // the release page
	Setup   *ReleaseAsset
	SHA256  *ReleaseAsset
}

// UpdateStatus is what the window shows.
type UpdateStatus struct {
	Current    string `json:"current"`
	Version    string `json:"version,omitempty"`
	URL        string `json:"url,omitempty"`
	Available  bool   `json:"available"`
	CanInstall bool   `json:"canInstall,omitempty"`
	Checking   bool   `json:"checking"`
	Message    string `json:"message,omitempty"`
	State      string `json:"state,omitempty"` // "", "downloading", "installing", "error"
	Done       int64  `json:"done,omitempty"`
	Total      int64  `json:"total,omitempty"`
	Error      string `json:"error,omitempty"`
	Running    int    `json:"running,omitempty"`
}

// Updater checks GitHub for a newer BDDM and, after a click, installs it.
type Updater struct {
	mu        sync.Mutex
	current   string
	latest    *ReleaseInfo
	dismissed string // hide the notice for this version until the next start
	notified  string // tray notice already shown for this version
	checking  bool
	message   string
	state     string
	done      int64
	total     int64
	errMsg    string
	cancelDl  context.CancelFunc
	client    *http.Client
	dlClient  *http.Client
	apiURL    string
	period    time.Duration
	dir       string
	notifyFn  func(title, body string)
	enabled   func() bool
	allowNote func() bool
	running   func() int
	prepare   func()
	runSetup  func(path string) error
	quitFn    func()
	stop      chan struct{}
	stopped   chan struct{}
}

func newUpdater(current string, enabled, allowNote func() bool, notifyFn func(title, body string)) *Updater {
	if enabled == nil {
		enabled = func() bool { return true }
	}
	if allowNote == nil {
		allowNote = func() bool { return true }
	}
	return &Updater{
		current:   current,
		client:    &http.Client{Timeout: updateTimeout},
		apiURL:    updateAPIURL,
		period:    updatePeriod,
		notifyFn:  notifyFn,
		enabled:   enabled,
		allowNote: allowNote,
		stop:      make(chan struct{}),
		stopped:   make(chan struct{}),
	}
}

// Start checks once now (if automatic checking is on), then about every 6 hours.
func (u *Updater) Start() {
	go u.loop()
}

func (u *Updater) loop() {
	defer close(u.stopped)
	if u.enabled() {
		u.Check(false)
	}
	period := u.period
	if period <= 0 {
		period = updatePeriod
	}
	tick := time.NewTicker(period)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if u.enabled() {
				u.Check(false)
			}
		case <-u.stop:
			return
		}
	}
}

// Stop ends the background checks and cancels an in-progress download.
func (u *Updater) Stop() {
	select {
	case <-u.stop:
	default:
		close(u.stop)
	}
	u.Cancel()
}

func (u *Updater) Status() UpdateStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.statusLocked()
}

func (u *Updater) statusLocked() UpdateStatus {
	s := UpdateStatus{
		Current:  u.current,
		Checking: u.checking,
		Message:  u.message,
		State:    u.state,
		Done:     u.done,
		Total:    u.total,
		Error:    u.errMsg,
	}
	if u.running != nil {
		s.Running = u.running()
	}
	if u.latest != nil {
		s.Version = u.latest.Version
		s.URL = u.latest.URL
		newer := compareVersions(u.current, u.latest.Version) < 0
		s.CanInstall = newer && u.latest.Setup != nil
		if newer && (u.dismissed != u.latest.Version || u.state != "") {
			s.Available = true
		}
	}
	return s
}

// Check asks GitHub for the latest release. Automatic checks stay quiet on
// failure (offline, rate-limited, ...). A manual check sets a short message
// for the Settings page. Nothing is downloaded.
func (u *Updater) Check(manual bool) UpdateStatus {
	u.mu.Lock()
	if u.checking || u.state == "downloading" || u.state == "installing" {
		s := u.statusLocked()
		u.mu.Unlock()
		return s
	}
	u.checking = true
	if manual {
		u.message = "Checking..."
	} else {
		u.message = ""
	}
	client, apiURL := u.client, u.apiURL
	current := u.current
	u.mu.Unlock()

	info, err := fetchLatestRelease(client, apiURL, current)

	u.mu.Lock()
	defer u.mu.Unlock()
	u.checking = false
	if err != nil {
		if manual {
			u.message = "Could not check right now."
		}
		// Automatic checks stay silent in the window; a line in the log is enough.
		log.Printf("update check: %v", err)
		return u.statusLocked()
	}
	u.latest = info
	if compareVersions(current, info.Version) < 0 {
		if manual {
			u.message = "BDDM v" + info.Version + " is available."
		} else {
			u.message = ""
		}
		if u.notified != info.Version && u.allowNote() && u.notifyFn != nil {
			u.notified = info.Version
			title, body := "BDDM", "BDDM v"+info.Version+" is available"
			go u.notifyFn(title, body)
		}
	} else if manual {
		u.message = "You have the latest version."
	}
	return u.statusLocked()
}

// Dismiss hides the notice for the current latest version until BDDM is started again.
func (u *Updater) Dismiss() UpdateStatus {
	u.Cancel()
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.latest != nil {
		u.dismissed = u.latest.Version
	}
	if u.state == "error" {
		u.state = ""
		u.errMsg = ""
	}
	return u.statusLocked()
}

// OpenRelease opens the latest release page in the default browser.
func (u *Updater) OpenRelease() error {
	u.mu.Lock()
	var raw string
	if u.latest != nil {
		raw = u.latest.URL
	}
	u.mu.Unlock()
	if raw == "" {
		return fmt.Errorf("no release to open")
	}
	return openURL(raw)
}

// Install starts downloading the setup file and, after it checks out, runs
// the silent upgrade. It does nothing if an install is already in progress.
func (u *Updater) Install() UpdateStatus {
	u.mu.Lock()
	if u.state == "downloading" || u.state == "installing" {
		s := u.statusLocked()
		u.mu.Unlock()
		return s
	}
	if u.latest == nil || u.latest.Setup == nil || compareVersions(u.current, u.latest.Version) >= 0 {
		u.state = "error"
		u.errMsg = "No installer is available for this release."
		s := u.statusLocked()
		u.mu.Unlock()
		return s
	}
	u.state = "downloading"
	u.errMsg = ""
	u.done = 0
	u.total = u.latest.Setup.Size
	u.message = ""
	ctx, cancel := context.WithCancel(context.Background())
	u.cancelDl = cancel
	info := *u.latest
	if u.latest.Setup != nil {
		setup := *u.latest.Setup
		info.Setup = &setup
	}
	if u.latest.SHA256 != nil {
		sum := *u.latest.SHA256
		info.SHA256 = &sum
	}
	client := u.dlClient
	if client == nil {
		client = &http.Client{Timeout: updateDlTimeout}
	}
	dir := u.dir
	current := u.current
	u.mu.Unlock()

	go u.doInstall(ctx, client, dir, current, &info)
	return u.Status()
}

// Cancel stops an in-progress download. It does not stop the installer once
// that has already been started.
func (u *Updater) Cancel() UpdateStatus {
	u.mu.Lock()
	cancel := u.cancelDl
	state := u.state
	u.mu.Unlock()
	if cancel != nil && state == "downloading" {
		cancel()
	}
	return u.Status()
}

func (u *Updater) doInstall(ctx context.Context, client *http.Client, dir, current string, info *ReleaseInfo) {
	fail := func(msg string) {
		log.Printf("update install: %s", msg)
		u.mu.Lock()
		if u.state == "downloading" || u.state == "installing" {
			u.state = "error"
			u.errMsg = msg
			u.cancelDl = nil
		}
		u.mu.Unlock()
	}

	if dir == "" {
		dir = os.TempDir()
	}
	dest := filepath.Join(dir, "update", "BDDM-Setup.exe")
	ua := "BDDM/" + current
	dlClient := client
	if dlClient == nil {
		dlClient = &http.Client{Timeout: updateDlTimeout}
	}

	progress := func(done int64) {
		u.mu.Lock()
		u.done = done
		u.mu.Unlock()
	}
	err := downloadHTTP(ctx, dlClient, info.Setup.URL, dest, ua, info.Setup.Size, progress)
	if err != nil {
		if ctx.Err() != nil {
			u.mu.Lock()
			if u.state == "downloading" {
				u.state = ""
				u.errMsg = ""
				u.done = 0
				u.cancelDl = nil
			}
			u.mu.Unlock()
			return
		}
		if errors.Is(err, errSetupSize) {
			fail("The update file is not the size it should be. It was not installed.")
			return
		}
		fail("The update file could not be downloaded. Check your internet connection and try again.")
		return
	}

	var wantSHA string
	if info.SHA256 != nil && info.SHA256.URL != "" {
		data, err := downloadSmall(ctx, dlClient, info.SHA256.URL, ua, updateSumMax)
		if err != nil {
			if ctx.Err() != nil {
				u.mu.Lock()
				if u.state == "downloading" {
					u.state = ""
					u.errMsg = ""
					u.cancelDl = nil
				}
				u.mu.Unlock()
				return
			}
			fail("The checksum file could not be downloaded. Check your internet connection and try again.")
			return
		}
		wantSHA, err = parseSHA256File(data)
		if err != nil {
			fail("The checksum file could not be read. The update was not installed.")
			return
		}
	}
	if err := verifySetupFile(dest, info.Setup.Size, wantSHA); err != nil {
		fail(err.Error())
		return
	}

	u.mu.Lock()
	u.state = "installing"
	u.done = u.total
	u.cancelDl = nil
	prepare, runSetup, quitFn := u.prepare, u.runSetup, u.quitFn
	u.mu.Unlock()

	// prepare (Manager.Shutdown) stops the server-side work and saves
	// state, including which torrents should start again. The installer
	// is started only after that, then this process exits. The installer
	// waits for this process to leave on its own and only force-kills it
	// as a last resort.
	if prepare != nil {
		prepare()
	}
	if runSetup != nil {
		if err := runSetup(dest); err != nil {
			fail("The update could not be started. " + err.Error())
			return
		}
	} else {
		fail("The update could not be started.")
		return
	}
	if quitFn != nil {
		quitFn()
	}
}

func fetchLatestRelease(client *http.Client, apiURL, current string) (*ReleaseInfo, error) {
	if client == nil {
		client = &http.Client{Timeout: updateTimeout}
	}
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "BDDM/"+current)
	// No Authorization header: this is a public request, no token.

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, updateMaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > updateMaxBody {
		return nil, fmt.Errorf("response too large")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub returned HTTP %d", resp.StatusCode)
	}
	return parseLatestRelease(body)
}

// parseLatestRelease reads a GitHub /releases/latest JSON body.
func parseLatestRelease(data []byte) (*ReleaseInfo, error) {
	var r githubRelease
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.Draft || r.Prerelease {
		return nil, fmt.Errorf("latest release is a draft or pre-release")
	}
	tag := strings.TrimSpace(r.TagName)
	if tag == "" {
		return nil, fmt.Errorf("release has no tag")
	}
	page := strings.TrimSpace(r.HTMLURL)
	if page == "" {
		return nil, fmt.Errorf("release has no page")
	}
	setup, sum := selectSetupAssets(r.Assets)
	return &ReleaseInfo{Version: normalizeVersion(tag), Tag: tag, URL: page, Setup: setup, SHA256: sum}, nil
}

// selectSetupAssets finds BDDM-Setup.exe and, if present, BDDM-Setup.exe.sha256.
func selectSetupAssets(assets []githubAsset) (setup, sum *ReleaseAsset) {
	for _, a := range assets {
		name := strings.ToLower(strings.TrimSpace(a.Name))
		url := strings.TrimSpace(a.BrowserDownloadURL)
		if url == "" || a.Size < 0 {
			continue
		}
		item := ReleaseAsset{Name: strings.TrimSpace(a.Name), Size: a.Size, URL: url}
		switch name {
		case setupAssetName:
			cp := item
			setup = &cp
		case sumAssetName:
			cp := item
			sum = &cp
		}
	}
	return setup, sum
}

// parseSHA256File reads a SHA-256 file (sha256sum format, a bare hash, or
// "SHA256 (file) = hash").
func parseSHA256File(data []byte) (string, error) {
	s := strings.TrimSpace(string(data))
	if s == "" {
		return "", fmt.Errorf("checksum file is empty")
	}
	if i := strings.Index(strings.ToLower(s), ") = "); i >= 0 {
		s = strings.TrimSpace(s[i+4:])
	}
	if cs, err := parseUserChecksum(s); err == nil && cs != nil && cs.Algo == "sha256" {
		return cs.Hex, nil
	}
	for _, f := range strings.Fields(s) {
		f = strings.TrimPrefix(f, "*")
		if hexRe.MatchString(f) && len(f) == 64 {
			return strings.ToLower(f), nil
		}
	}
	return "", fmt.Errorf("checksum file is not a SHA-256 hash")
}

// verifySetupFile checks the setup file's size and, when wantSHA is not
// empty, its SHA-256 hash. The messages are safe to show in the window.
func verifySetupFile(path string, size int64, wantSHA string) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("The update file could not be read. It was not installed.")
	}
	if st.Size() != size {
		return fmt.Errorf("The update file is not the size it should be. It was not installed.")
	}
	if wantSHA == "" {
		return nil
	}
	got, err := hashFile(context.Background(), path, "sha256", nil)
	if err != nil {
		return fmt.Errorf("The update file could not be checked. It was not installed.")
	}
	if !strings.EqualFold(got, wantSHA) {
		return fmt.Errorf("The update file did not match its checksum. It was not installed.")
	}
	return nil
}

var errSetupSize = errors.New("size mismatch")

func downloadHTTP(ctx context.Context, client *http.Client, rawURL, dest, ua string, size int64, progress func(int64)) error {
	if err := validWebURL(rawURL); err != nil {
		return err
	}
	if client == nil {
		client = &http.Client{Timeout: updateDlTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	part := dest + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	var got int64
	buf := make([]byte, 32*1024)
	var copyErr error
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if size > 0 && got+int64(n) > size {
				copyErr = errSetupSize
				break
			}
			if _, werr := f.Write(buf[:n]); werr != nil {
				copyErr = werr
				break
			}
			got += int64(n)
			if progress != nil {
				progress(got)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			copyErr = rerr
			break
		}
	}
	if copyErr == nil {
		copyErr = f.Sync()
	}
	_ = f.Close()
	if copyErr != nil {
		_ = os.Remove(part)
		return copyErr
	}
	if size > 0 && got != size {
		_ = os.Remove(part)
		return errSetupSize
	}
	_ = os.Remove(dest)
	return os.Rename(part, dest)
}

func downloadSmall(ctx context.Context, client *http.Client, rawURL, ua string, max int64) ([]byte, error) {
	if err := validWebURL(rawURL); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: updateTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/plain, application/octet-stream")
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("checksum file too large")
	}
	return data, nil
}

// normalizeVersion strips a leading "v" or "V" and surrounding spaces.
func normalizeVersion(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 0 && (s[0] == 'v' || s[0] == 'V') {
		s = s[1:]
	}
	return strings.TrimSpace(s)
}

// compareVersions compares two dotted version numbers (a leading "v" is
// ignored). It returns -1 if a < b, 0 if they are equal, and 1 if a > b.
func compareVersions(a, b string) int {
	pa := versionParts(normalizeVersion(a))
	pb := versionParts(normalizeVersion(b))
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		na, sa := versionPartAt(pa, i)
		nb, sb := versionPartAt(pb, i)
		if na != nb {
			if na < nb {
				return -1
			}
			return 1
		}
		// Same number: a suffix such as "beta" is older than a plain number.
		if sa != sb {
			if sa == "" {
				return 1
			}
			if sb == "" {
				return -1
			}
			if sa < sb {
				return -1
			}
			return 1
		}
	}
	return 0
}

type versionPart struct {
	num    int
	suffix string
}

func versionParts(s string) []versionPart {
	if s == "" {
		return nil
	}
	bits := strings.Split(s, ".")
	out := make([]versionPart, 0, len(bits))
	for _, bit := range bits {
		out = append(out, parseVersionPart(bit))
	}
	return out
}

func parseVersionPart(s string) versionPart {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n := 0
	if i > 0 {
		n, _ = strconv.Atoi(s[:i])
	}
	suf := s[i:]
	if len(suf) > 0 && (suf[0] == '-' || suf[0] == '+' || suf[0] == '_') {
		suf = suf[1:]
	}
	return versionPart{num: n, suffix: suf}
}

func versionPartAt(parts []versionPart, i int) (int, string) {
	if i >= len(parts) {
		return 0, ""
	}
	return parts[i].num, parts[i].suffix
}

// validWebURL accepts only http and https addresses.
func validWebURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("cannot open this address")
	}
	return nil
}
