package main

// updater.go: look up the latest GitHub release and show a notice when a
// newer version is available. Nothing is downloaded or installed here.

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	updateAPIURL  = "https://api.github.com/repos/shekh-shehab/BDDM/releases/latest"
	updatePeriod  = 6 * time.Hour
	updateTimeout = 15 * time.Second
	updateMaxBody = 1 << 20
)

// githubRelease is the fields we need from the GitHub releases API.
type githubRelease struct {
	TagName    string `json:"tag_name"`
	Name       string `json:"name"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// ReleaseInfo is a published (non-draft, non-prerelease) GitHub release.
type ReleaseInfo struct {
	Version string // 1.4.3, without a leading "v"
	Tag     string // the tag as GitHub sent it, e.g. v1.4.3
	URL     string // the release page
}

// UpdateStatus is what the window shows.
type UpdateStatus struct {
	Current   string `json:"current"`
	Version   string `json:"version,omitempty"`
	URL       string `json:"url,omitempty"`
	Available bool   `json:"available"`
	Checking  bool   `json:"checking"`
	Message   string `json:"message,omitempty"`
}

// Updater checks GitHub for a newer BDDM and remembers the result.
type Updater struct {
	mu        sync.Mutex
	current   string
	latest    *ReleaseInfo
	dismissed string // hide the notice for this version until the next start
	notified  string // tray notice already shown for this version
	checking  bool
	message   string
	client    *http.Client
	apiURL    string
	period    time.Duration
	notifyFn  func(title, body string)
	enabled   func() bool
	allowNote func() bool
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

// Stop ends the background checks.
func (u *Updater) Stop() {
	select {
	case <-u.stop:
	default:
		close(u.stop)
	}
}

func (u *Updater) Status() UpdateStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.statusLocked()
}

func (u *Updater) statusLocked() UpdateStatus {
	s := UpdateStatus{Current: u.current, Checking: u.checking, Message: u.message}
	if u.latest != nil {
		s.Version = u.latest.Version
		s.URL = u.latest.URL
		if compareVersions(u.current, u.latest.Version) < 0 && u.dismissed != u.latest.Version {
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
	if u.checking {
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
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.latest != nil {
		u.dismissed = u.latest.Version
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
	url := strings.TrimSpace(r.HTMLURL)
	if url == "" {
		return nil, fmt.Errorf("release has no page")
	}
	return &ReleaseInfo{Version: normalizeVersion(tag), Tag: tag, URL: url}, nil
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
