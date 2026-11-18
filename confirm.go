package main

// confirm.go: downloads caught by the browser extension can wait in a
// "Pending" state until the user presses Start in the "Download File Info"
// window (ui/confirm.html). Nothing is downloaded before that.

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const pendingTimeout = 30 * time.Minute // forget unanswered windows after this

type pendingInfo struct {
	dupName  string // set when this link was downloaded before
	probing  bool   // still asking the server for the size and name
	suggest  string // file name to show
	probeErr string
	formats  []string // Google Docs/Sheets/Slides: export formats to offer
	format   string   // the chosen one
	final    bool     // probeErr is final (private, missing, over quota)
}

// PendingView is what the confirm window shows.
type PendingView struct {
	ID        string   `json:"id"`
	URL       string   `json:"url"`
	Name      string   `json:"name"`
	Size      int64    `json:"size"` // 0 = unknown
	Dir       string   `json:"dir"`
	Duplicate bool     `json:"duplicate"`
	DupName   string   `json:"dupName,omitempty"`
	Checking  bool     `json:"checking"`
	Problem   string   `json:"problem,omitempty"`
	Final     bool     `json:"final,omitempty"`   // Problem means it cannot be downloaded
	Formats   []string `json:"formats,omitempty"` // Google Docs export formats
	Format    string   `json:"format,omitempty"`
	Category  string   `json:"category"` // file type (its folder is Dir)
}

// AddPending adds a download that waits for the user to press Start.
func (m *Manager) AddPending(r AddRequest) (JobView, error) {
	r.URL = strings.TrimSpace(r.URL)
	u, err := url.Parse(r.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return JobView{}, errors.New("Please enter a valid http:// or https:// link.")
	}
	if driveNonFilePage(r.URL) {
		return JobView{}, errDrivePage
	}
	p := &pendingInfo{probing: true, suggest: sanitizeName(r.Filename)}
	if !r.Force {
		if d := m.FindDone(r.URL); d != nil {
			p.dupName = d.Name
		}
	}
	dl, isDrive := parseDriveLink(r.URL)
	if isDrive && dl.Kind == "doc" {
		p.formats = docFormats(dl.DocType)
		p.format = p.formats[0]
		p.suggest = ""
	} else if isDrive && p.suggest == "" {
		p.suggest = "Google Drive file"
	}
	if p.suggest == "" && !isDrive {
		p.suggest = filenameFromURL(r.URL)
	}
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return JobView{}, errors.New("BDDM is shutting down")
	}
	j := &Job{
		ID: newID(), URL: r.URL, Referer: strings.TrimSpace(r.Referer), Cookie: strings.TrimSpace(r.Cookie),
		Want: sanitizeName(r.Filename), Dir: m.settings.DownloadDir, Parts: m.settings.Parts,
		Status: StatusPending, Started: time.Now(), pend: p, PageTitle: cleanValue(strings.TrimSpace(r.Title), 1024),
	}
	if !isDrive {
		// "stream.mp4", "download": the page's title says more (names.go).
		p.suggest = nameWithTitle(p.suggest, j.PageTitle)
	}
	j.applyBrowser(r)
	m.jobs = append(m.jobs, j)
	v := j.viewLocked()
	m.mu.Unlock()

	go m.probePending(j)
	go func() {
		time.Sleep(pendingTimeout)
		_ = m.CancelPending(j.ID)
	}()
	return v, nil
}

// probePending asks the server for the size and file name (no download).
func (m *Manager) probePending(j *Job) {
	m.mu.Lock()
	rawURL, h := j.URL, j.browserHeaders()
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var info probeInfo
	var err error
	dl, isDrive := parseDriveLink(rawURL)
	if isOneShot(rawURL, h.Referer) {
		// A link that works once (oneshot.go): asking for the size would
		// use it up. The name comes from the browser, the link or the page.
		m.mu.Lock()
		if j.pend != nil {
			j.pend.probing = false
		}
		m.mu.Unlock()
		return
	}
	switch {
	case isDrive && dl.Kind == "file":
		_, info, err = resolveDrive(ctx, m.client, dl, h)
	case isDrive && dl.Kind == "doc":
		var title string
		title, err = docTitle(ctx, m.client, dl, h)
		if err != nil && (h.Cookie != "" || dl.AuthUser != "") {
			// Maybe a public document the signed-in account cannot open.
			anon, al := h, dl
			anon.Cookie, al.AuthUser = "", ""
			if t2, err2 := docTitle(ctx, m.client, al, anon); err2 == nil {
				title, err = t2, nil
			}
		}
		info.Filename = cleanDriveName(firstNonEmpty(title, "Google "+dl.DocType))
	default:
		info, err = probe(ctx, m.client, rawURL, h)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if j.pend == nil {
		return // already started or cancelled
	}
	j.pend.probing = false
	if err != nil {
		j.pend.probeErr = err.Error()
		j.pend.final = isDriveFinal(err)
		if isDrive && dl.Kind == "doc" {
			j.pend.suggest = withExt(cleanDriveName("Google "+dl.DocType), j.pend.format)
		}
		return
	}
	if isDrive && dl.Kind == "doc" {
		if j.Want == "" {
			j.pend.suggest = withExt(info.Filename, j.pend.format)
		}
		return
	}
	if info.Size > 0 {
		j.Size = info.Size
	}
	if !isDrive {
		j.probed, j.probedAt = &info, time.Now()
	}
	// A name from the server is better than a guess from the URL, unless the
	// browser already told us the name.
	if info.Filename != "" && (j.Want == "" || (uselessName(j.Want) && !uselessName(info.Filename))) {
		j.pend.suggest = nameWithTitle(sanitizeName(info.Filename), j.PageTitle)
	}
}

func (m *Manager) pendingLocked(id string) (*Job, error) {
	j := m.findLocked(id)
	if j == nil || j.Status != StatusPending || j.pend == nil {
		return nil, errors.New("This download is no longer waiting (it was started or cancelled).")
	}
	return j, nil
}

func (m *Manager) Pending(id string) (PendingView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, err := m.pendingLocked(id)
	if err != nil {
		return PendingView{}, err
	}
	name := j.pend.suggest
	if len(j.pend.formats) > 0 {
		name = withExt(name, j.pend.format)
	}
	cat := m.settings.categoryOf(name)
	return PendingView{
		ID: j.ID, URL: j.URL, Name: j.pend.suggest, Size: j.Size, Dir: m.settings.dirForCategory(cat), Category: cat,
		Duplicate: j.pend.dupName != "", DupName: j.pend.dupName,
		Checking: j.pend.probing, Problem: j.pend.probeErr, Final: j.pend.final,
		Formats: j.pend.formats, Format: j.pend.format,
	}, nil
}

// StartPending starts (or, with later=true, adds paused) a pending download
// using the name and folder chosen in the window.
func (m *Manager) StartPending(id, name, dir string, later bool, format string) (JobView, error) {
	name = sanitizeName(strings.TrimSpace(name))
	dir = strings.TrimSpace(dir)
	if dir != "" {
		dir = filepath.Clean(dir)
		if !filepath.IsAbs(dir) {
			return JobView{}, errors.New("Please choose a full folder path, for example D:\\Downloads.")
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return JobView{}, errors.New("Cannot use this folder: " + err.Error())
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j, err := m.pendingLocked(id)
	if err != nil {
		return JobView{}, err
	}
	if len(j.pend.formats) > 0 {
		dl, _ := parseDriveLink(j.URL)
		if !validDocFormat(dl.DocType, format) {
			format = j.pend.format
		}
		j.Export = format
		if name != "" {
			name = withExt(name, format)
		}
	}
	if name != "" {
		j.Want = name
	}
	// The folder of the file type, or the one chosen in the window (which is
	// then remembered for files of this type).
	cat := m.settings.categoryOf(firstNonEmpty(j.Want, j.pend.suggest))
	if dir != "" {
		j.Dir = dir
		m.rememberDirLocked(cat, dir)
	} else {
		j.Dir = m.settings.dirForCategory(cat)
	}
	j.pend = nil
	j.Started = time.Now()
	if later {
		j.Status = StatusPaused
	} else {
		j.Status = StatusQueued
		m.scheduleLocked()
	}
	m.saveHistoryLocked()
	return j.viewLocked(), nil
}

// CancelPending forgets a pending download completely.
func (m *Manager) CancelPending(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, j := range m.jobs {
		if j.ID == id {
			if j.Status != StatusPending {
				return nil
			}
			m.jobs = append(m.jobs[:i], m.jobs[i+1:]...)
			return nil
		}
	}
	return nil
}

// Views returns the jobs with these ids, pending ones included.
func (m *Manager) Views(ids map[string]bool) []JobView {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []JobView{}
	for _, j := range m.jobs {
		if ids[j.ID] {
			out = append(out, j.viewLocked())
		}
	}
	return out
}

// SetPendingExtras sets the checksum and the mirror links typed in the
// "Download File Info" window, before the download starts.
func (m *Manager) SetPendingExtras(id, hash string, mirrors []string) error {
	sum, err := parseUserChecksum(hash)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j, err := m.pendingLocked(id)
	if err != nil {
		return err
	}
	j.Hash = sum
	j.Mirrors = cleanMirrors(j.URL, mirrors)
	return nil
}
