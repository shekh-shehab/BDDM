package main

import (
	"os"
	"sync"
	"testing"
	"time"
)

// Compact progress and complete windows (item 26). The download stays in
// the main list; these are extra windows. Folder and burst-batch starts
// do not get a window for every file.

const (
	popupBurstWindow = 2 * time.Second
	popupBurstMax    = 3
)

var (
	popupMu   sync.Mutex
	popupLog  []string // "kind:id" — recorded even under go test
	popupHook func(kind, id, url string)
)

func popupURL(port, page, id string) string {
	return "http://127.0.0.1:" + port + "/" + page + ".html?id=" + id
}

func recordPopup(kind, id, url string) {
	popupMu.Lock()
	popupLog = append(popupLog, kind+":"+id)
	hook := popupHook
	popupMu.Unlock()
	if hook != nil {
		hook(kind, id, url)
	}
}

func takePopupLog() []string {
	popupMu.Lock()
	defer popupMu.Unlock()
	out := append([]string(nil), popupLog...)
	popupLog = nil
	return out
}

func openJobPopup(port, kind, id string) {
	if port == "" || id == "" {
		return
	}
	page, w, h := "progress", 380, 400
	switch kind {
	case "complete":
		page, w, h = "complete", 380, 240
	case "error":
		page, w, h = "error", 420, 360
	}
	u := popupURL(port, page, id)
	recordPopup(kind, id, u)
	if testing.Testing() || os.Getenv("BDDM_TEST") == "1" {
		return
	}
	_ = openAppWindowSize(u, w, h)
}

func (s *server) offerProgress(job JobView, later bool) {
	if later || !popupEligible(job) {
		return
	}
	if !s.m.markPopup(job.ID) {
		return
	}
	openJobPopup(s.port, "progress", job.ID)
}

func popupEligible(job JobView) bool {
	if job.ID == "" || job.Group != "" {
		return false
	}
	if job.Kind == kindTorrent {
		return false
	}
	return true
}

func (m *Manager) markPopup(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.markPopupLocked(id)
}

func (m *Manager) markPopupLocked(id string) bool {
	now := time.Now()
	recent := m.popupBurst[:0]
	for _, t := range m.popupBurst {
		if now.Sub(t) < popupBurstWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= popupBurstMax {
		m.popupBurst = recent
		return false
	}
	m.popupBurst = append(recent, now)
	if m.popupIDs == nil {
		m.popupIDs = map[string]bool{}
	}
	m.popupIDs[id] = true
	return true
}

func (m *Manager) takePopupLocked(id string) bool {
	if m.popupIDs[id] {
		delete(m.popupIDs, id)
		return true
	}
	return false
}

func (m *Manager) offerCompleteLocked(j *Job) {
	if j == nil || j.Group != "" || j.Kind == kindTorrent || m.uiPort == "" {
		return
	}
	if !m.takePopupLocked(j.ID) {
		return
	}
	kind := "complete"
	if j.Status != StatusDone {
		kind = "error"
	}
	port := m.uiPort
	id := j.ID
	go openJobPopup(port, kind, id)
}
