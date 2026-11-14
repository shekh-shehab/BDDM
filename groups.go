package main

// groups.go: Google Drive folder downloads shown as one row ("group") in the
// list, and the "download finished" announcements (in-app toast + Windows
// notification), each sent exactly once.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Group is one folder download: its files are jobs with Job.Group = ID.
type Group struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Dir      string    `json:"dir"` // the folder the files go into
	URL      string    `json:"url"`
	Created  time.Time `json:"created"`
	Finished time.Time `json:"finished"`
	Notified bool      `json:"notified,omitempty"` // the "folder done" message was sent
	Removed  bool      `json:"removed,omitempty"`

	// Runtime only: the folder's own download speed (bytes of all its
	// files, also the ones that just finished), sampled about once a second.
	rateAt   time.Time
	rateDone int64
	rate     float64
	// runAt/runFiles: when this run began and how many files were finished
	// then, for an ETA that also counts each file's start-up time.
	runAt    time.Time
	runFiles int
}

// GroupView is what the list shows for a folder download.
type GroupView struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Dir      string  `json:"dir"`
	URL      string  `json:"url"`
	Total    int     `json:"total"`
	Finished int     `json:"finishedFiles"` // files done
	Failed   int     `json:"failed"`        // failed, can be retried
	ViewOnly int     `json:"viewOnly"`      // the owner turned off downloading
	Active   int     `json:"active"`
	Queued   int     `json:"queued"`
	Paused   int     `json:"paused"`
	Size     int64   `json:"size"` // 0 = unknown (some sizes unknown)
	Done     int64   `json:"done"`
	Speed    float64 `json:"speed"`
	ETA      int64   `json:"eta"`
	Status   string  `json:"status"`
	Started  string  `json:"started"`
	Ended    string  `json:"ended,omitempty"`
}

func (m *Manager) groupsPath() string { return filepath.Join(m.configDir, "groups.json") }

func (m *Manager) loadGroups() {
	data, err := os.ReadFile(m.groupsPath())
	if err != nil {
		return
	}
	var gs []*Group
	if err := json.Unmarshal(data, &gs); err != nil {
		log.Printf("could not read groups: %v", err)
		return
	}
	for _, g := range gs {
		if g != nil && g.ID != "" {
			m.groups = append(m.groups, g)
		}
	}
}

func (m *Manager) saveGroupsLocked() {
	data, err := json.MarshalIndent(m.groups, "", "  ")
	if err == nil {
		err = writeFileAtomic(m.groupsPath(), data)
	}
	if err != nil {
		log.Printf("could not save groups: %v", err)
	}
}

func (m *Manager) findGroupLocked(id string) *Group {
	for _, g := range m.groups {
		if g.ID == id {
			return g
		}
	}
	return nil
}

func (m *Manager) groupJobsLocked(id string) []*Job {
	var out []*Job
	for _, j := range m.jobs {
		if j.Group == id && !j.Removed && j.Status != StatusPending {
			out = append(out, j)
		}
	}
	return out
}

func (m *Manager) groupViewLocked(g *Group) GroupView {
	v := GroupView{ID: g.ID, Name: g.Name, Dir: g.Dir, URL: g.URL, ETA: -1, Started: g.Created.Format(time.RFC3339)}
	if !g.Finished.IsZero() {
		v.Ended = g.Finished.Format(time.RFC3339)
	}
	// Size, progress and ETA count the files that can still be downloaded
	// (failed and view-only files are counted separately).
	sizeKnown := true
	var remaining, doneBytes int64
	var unknown, doneN int64
	for _, j := range m.groupJobsLocked(g.ID) {
		jv := j.viewLocked()
		if j.Status == StatusDone && jv.Done > 0 {
			doneBytes += jv.Done
			doneN++
		}
		v.Total++
		switch {
		case j.Status == StatusDone:
			v.Finished++
		case jv.ViewOnly:
			v.ViewOnly++
			continue
		case j.Status == StatusError:
			v.Failed++
			continue
		case j.Status == StatusDownloading:
			v.Active++
			v.Speed += jv.Speed
		case j.Status == StatusQueued:
			v.Queued++
		case j.Status == StatusPaused:
			v.Paused++
		}
		v.Done += jv.Done
		if jv.Size > 0 {
			v.Size += jv.Size
			if j.Status != StatusDone && jv.Size > jv.Done {
				remaining += jv.Size - jv.Done
			}
		} else if j.Status != StatusDone {
			sizeKnown = false
			unknown++
			remaining -= jv.Done
		}
	}
	// Files whose size is not known yet (the folder listing did not say):
	// estimate them with the average size of the files already finished.
	if unknown > 0 {
		if doneN == 0 {
			remaining = -1 << 62 // no basis for an estimate: no ETA
		} else {
			remaining += unknown * (doneBytes / doneN)
		}
	}
	if !sizeKnown {
		v.Size = 0
	}
	switch {
	case v.Total == 0:
		v.Status = "Done"
	case v.Active > 0:
		v.Status = "Downloading"
	case v.Queued > 0:
		v.Status = "Queued"
	case v.Paused > 0:
		v.Status = "Paused"
	case v.Failed > 0:
		v.Status = "Error"
	default:
		v.Status = "Done"
	}
	// Small files often finish between two speed samples, so also measure the
	// folder's bytes over time and show the higher of the two.
	if v.Status == "Downloading" {
		now := time.Now()
		if g.rateAt.IsZero() || v.Done < g.rateDone {
			g.rateAt, g.rateDone, g.rate = now, v.Done, 0
		} else if el := now.Sub(g.rateAt).Seconds(); el >= 1 {
			inst := float64(v.Done-g.rateDone) / el
			if g.rate == 0 {
				g.rate = inst
			} else {
				g.rate = 0.4*inst + 0.6*g.rate
			}
			g.rateAt, g.rateDone = now, v.Done
		}
		v.Speed = max(v.Speed, g.rate)
		if g.runAt.IsZero() || v.Finished < g.runFiles {
			g.runAt, g.runFiles = now, v.Finished
		}
	} else {
		g.rateAt, g.rate = time.Time{}, 0
		g.runAt = time.Time{}
	}
	if v.Status == "Downloading" && v.Speed > 1 && remaining > 0 {
		v.ETA = int64(float64(remaining) / v.Speed)
	}
	// Many small files: the time per file (finding the link, starting the
	// transfer) matters more than the bytes, so use the file rate when slower.
	if v.Status == "Downloading" && !g.runAt.IsZero() {
		left := v.Total - v.Finished - v.Failed - v.ViewOnly
		if got := v.Finished - g.runFiles; got > 0 && left > 0 {
			per := time.Since(g.runAt).Seconds() / float64(got)
			if e := int64(per * float64(left)); e > v.ETA {
				v.ETA = e
			}
		}
	}
	return v
}

// Groups lists the folder downloads shown in the list, newest first.
func (m *Manager) Groups() []GroupView {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []GroupView{}
	for i := len(m.groups) - 1; i >= 0; i-- {
		g := m.groups[i]
		if g.Removed {
			continue
		}
		v := m.groupViewLocked(g)
		if v.Total == 0 {
			continue
		}
		out = append(out, v)
	}
	return out
}

// groupFinishedLocked is called when a file of a group stops. When no file of
// the group is waiting or running any more, it announces the folder once.
func (m *Manager) groupFinishedLocked(id string) {
	g := m.findGroupLocked(id)
	if g == nil || g.Notified {
		return
	}
	v := m.groupViewLocked(g)
	if v.Active > 0 || v.Queued > 0 || v.Paused > 0 || v.Total == 0 {
		return
	}
	g.Notified = true
	g.Finished = time.Now()
	viewOnly := ""
	if v.ViewOnly > 0 {
		viewOnly = fmt.Sprintf(", %d view-only %s skipped", v.ViewOnly, plural(v.ViewOnly, "file", "files"))
	}
	if v.Failed == 0 {
		m.announceLocked("Folder download complete", fmt.Sprintf("%s (%d of %d %s downloaded%s)", g.Name, v.Finished, v.Total, plural(v.Total, "file", "files"), viewOnly), "ok")
	} else {
		m.announceLocked("Folder download finished with errors",
			fmt.Sprintf("%s: %d of %d %s failed%s. Open BDDM and press Retry failed.", g.Name, v.Failed, v.Total, plural(v.Total, "file", "files"), viewOnly), "bad")
	}
	m.saveGroupsLocked()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// groupAction runs a job action on every file of a group.
func (m *Manager) groupIDs(id string, statuses ...string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.findGroupLocked(id)
	if g == nil || g.Removed {
		return nil, errors.New("folder download not found")
	}
	var ids []string
	for _, j := range m.groupJobsLocked(id) {
		if j.Status == StatusError && j.Error == msgDriveViewOnly && len(statuses) > 0 {
			continue // view only: retrying cannot help
		}
		if len(statuses) == 0 {
			ids = append(ids, j.ID)
			continue
		}
		for _, s := range statuses {
			if j.Status == s {
				ids = append(ids, j.ID)
				break
			}
		}
	}
	return ids, nil
}

func (m *Manager) rearmGroup(id string) {
	m.mu.Lock()
	if g := m.findGroupLocked(id); g != nil {
		g.Notified = false
		g.Finished = time.Time{}
		m.saveGroupsLocked()
	}
	m.mu.Unlock()
}

// PauseGroup pauses every running or waiting file of a folder download.
func (m *Manager) PauseGroup(id string) error {
	ids, err := m.groupIDs(id, StatusDownloading, StatusQueued)
	if err != nil {
		return err
	}
	for _, x := range ids {
		_ = m.Pause(x)
	}
	return nil
}

// ResumeGroup resumes the paused files (retryFailed: also the failed ones).
func (m *Manager) ResumeGroup(id string, retryFailed bool) error {
	st := []string{StatusPaused}
	if retryFailed {
		st = []string{StatusError}
	}
	ids, err := m.groupIDs(id, st...)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	m.rearmGroup(id)
	for _, x := range ids {
		_ = m.Resume(x)
	}
	return nil
}

// DeleteGroup removes a folder download from the list (and its files when
// deleteFiles is set).
func (m *Manager) DeleteGroup(id string, deleteFiles bool) error {
	ids, err := m.groupIDs(id)
	if err != nil {
		return err
	}
	for _, x := range ids {
		_ = m.Delete(x, deleteFiles)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if g := m.findGroupLocked(id); g != nil {
		g.Removed = true
		if deleteFiles {
			removeEmptyDirs(g.Dir)
		}
	}
	m.saveGroupsLocked()
	return nil
}

// removeEmptyDirs removes dir and its subfolders if they are empty.
func removeEmptyDirs(dir string) {
	if dir == "" {
		return
	}
	es, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range es {
		if e.IsDir() {
			removeEmptyDirs(filepath.Join(dir, e.Name()))
		}
	}
	_ = os.Remove(dir) // fails (keeps it) when something is still inside
}

// GroupDir is the folder of a folder download (for "Open folder").
func (m *Manager) GroupDir(id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.findGroupLocked(id)
	if g == nil {
		return "", errors.New("folder download not found")
	}
	if _, err := os.Stat(g.Dir); err != nil {
		return filepath.Dir(g.Dir), nil
	}
	return g.Dir, nil
}

// ---------------------------------------------------------------------------
// Announcements: one toast in the app and one Windows notification each.
// ---------------------------------------------------------------------------

// Event is one announcement; the app window shows each Seq once.
type Event struct {
	Seq   int64  `json:"seq"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Kind  string `json:"kind,omitempty"` // "ok", "bad"
	Time  string `json:"time"`
}

// bootID changes on every start, so windows know the event numbers restarted.
var bootID = strconv.FormatInt(time.Now().UnixNano(), 36)

type announcer struct {
	mu     sync.Mutex
	seq    int64
	events []Event
}

// announceLocked records an event for the app window and shows a Windows
// notification (when they are on). m.mu must be held.
func (m *Manager) announceLocked(title, body, kind string) {
	m.ann.mu.Lock()
	m.ann.seq++
	m.ann.events = append(m.ann.events, Event{Seq: m.ann.seq, Title: title, Body: body, Kind: kind, Time: time.Now().Format(time.RFC3339)})
	if len(m.ann.events) > 100 {
		m.ann.events = m.ann.events[len(m.ann.events)-100:]
	}
	m.ann.mu.Unlock()
	if m.settings.Notifications && m.notify != nil {
		go m.notify(title, body)
	}
}

// Events returns the announcements after seq, and the newest seq. The first
// call from a window (seq < 0) gets no old events, only the cursor.
func (m *Manager) Events(after int64) ([]Event, int64) {
	m.ann.mu.Lock()
	defer m.ann.mu.Unlock()
	out := []Event{}
	if after >= 0 {
		for _, e := range m.ann.events {
			if e.Seq > after {
				out = append(out, e)
			}
		}
	}
	return out, m.ann.seq
}
