package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConfirmVariants(t *testing.T) {
	// A form without id="download-form" but with a download action, and a
	// page with an extra search form first.
	page := `<html><form action="/search"><input name="q" value="x"></form>
<form method="get" action="https://drive.usercontent.google.com/download"><input type="hidden" name="id" value="ABCDEFGHIJKL"><input type="hidden" name="confirm" value="t"><input type="submit" value="Download anyway"></form></html>`
	got := parseDriveConfirm(page, "https://drive.usercontent.google.com/download?id=ABCDEFGHIJKL&export=download&authuser=1&resourcekey=0-k")
	for _, want := range []string{"id=ABCDEFGHIJKL", "confirm=t", "authuser=1", "resourcekey=0-k", "drive.usercontent.google.com/download?"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	link := `<a href="https://drive.usercontent.google.com/download?id=ABCDEFGHIJKL&amp;export=download&amp;confirm=t&amp;uuid=1">Download anyway</a>`
	if got := parseDriveConfirm(link, "https://drive.usercontent.google.com/download?id=ABCDEFGHIJKL"); got != "https://drive.usercontent.google.com/download?id=ABCDEFGHIJKL&export=download&confirm=t&uuid=1" {
		t.Errorf("link variant: %s", got)
	}
}

func TestRateLimitPage(t *testing.T) {
	if err := driveProblem("<html>Our systems have detected unusual traffic from your computer network</html>"); err != errDriveBusy {
		t.Errorf("sorry page: %v", err)
	}
	if isDriveFinal(errDriveBusy) {
		t.Error("busy must not be final")
	}
	c := driveCandidates(driveLink{ID: "ABCDEFGHIJKL", AuthUser: "1", ResourceKey: "0-k"})
	if len(c) != 3 {
		t.Fatal(c)
	}
	for _, u := range c {
		if !strings.Contains(u, "authuser=1") || !strings.Contains(u, "resourcekey=0-k") {
			t.Errorf("candidate lost authuser/resourcekey: %s", u)
		}
	}
}

func TestGroupAnnouncedOnce(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var got []string
	m, err := NewManager(dir, func(title, body string) {
		mu.Lock()
		got = append(got, title+": "+body)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	g := &Group{ID: "g1", Name: "Slides", Dir: dir, Created: time.Now()}
	m.groups = append(m.groups, g)
	for i, st := range []string{StatusDone, StatusDone, StatusDownloading} {
		m.jobs = append(m.jobs, &Job{ID: "j" + string(rune('a'+i)), URL: "https://x/" + string(rune('a'+i)), Name: "f", Status: st, Group: "g1"})
	}
	m.groupFinishedLocked("g1") // one still running: nothing yet
	m.jobs[2].Status = StatusDone
	for i := 0; i < 5; i++ {
		m.groupFinishedLocked("g1")
	}
	m.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	ev, _ := m.Events(0)
	if len(ev) != 1 || ev[0].Title != "Folder download complete" || !strings.Contains(ev[0].Body, "3 files") {
		t.Fatalf("events: %+v", ev)
	}
	mu.Lock()
	if len(got) != 1 {
		t.Errorf("notifications: %v", got)
	}
	mu.Unlock()
	// Saved and read back after a restart.
	m2, _ := NewManager(dir, nil)
	if len(m2.groups) != 1 || !m2.groups[0].Notified {
		t.Errorf("groups not persisted: %+v", m2.groups)
	}
	gv := m.Groups()
	if len(gv) != 1 || gv[0].Finished != 3 || gv[0].Status != "Done" {
		t.Errorf("view: %+v", gv)
	}
}

func TestViewOnlyAndFinal(t *testing.T) {
	page := `<html><head><title>Google Drive - Can&#39;t download file</title></head><body>Sorry, you can't download this file. The owner has disabled downloading.</body></html>`
	err := driveProblem(page)
	if err == nil || err.Error() != msgDriveViewOnly || !isDriveFinal(err) {
		t.Fatalf("view-only page: %v", err)
	}
	page2 := `<html><head><title>Google Drive - Can't download file</title></head><body></body></html>`
	if err := driveProblem(page2); err == nil || err.Error() != msgDriveViewOnly {
		t.Fatalf("title only: %v", err)
	}
}

func TestDoneMustBeOnDisk(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir, nil)
	u := "https://drive.google.com/file/d/ABCDEFGHIJKL/view"
	j := &Job{ID: "a", URL: u, Name: "x.pdf", Dir: dir, Size: 10, Status: StatusDone}
	m.jobs = append(m.jobs, j)
	if m.FindDone(u) != nil {
		t.Error("missing file counted as downloaded")
	}
	_ = os.WriteFile(filepath.Join(dir, "x.pdf"), []byte("12345"), 0644)
	if m.FindDone(u) != nil {
		t.Error("partial file counted as downloaded")
	}
	_ = os.WriteFile(filepath.Join(dir, "x.pdf"), []byte("1234567890"), 0644)
	if m.FindDone(u) == nil {
		t.Error("complete file not found")
	}
	j.Status = StatusError
	if m.FindDone(u) != nil {
		t.Error("failed job counted as downloaded")
	}
}

func TestAdaptiveLimit(t *testing.T) {
	a := &adaptiveLimit{max: 4, cur: 4}
	a.busy()
	a.busy() // same burst: once
	if a.limit() != 3 {
		t.Fatalf("limit %d", a.cur)
	}
	a.changed = time.Now().Add(-25 * time.Second)
	a.busy()
	if a.cur != 2 {
		t.Fatalf("limit %d", a.cur)
	}
	a.changed = time.Now().Add(-3 * time.Minute)
	if a.limit() != 3 {
		t.Fatalf("no recovery: %d", a.cur)
	}
	if a.limit() != 3 {
		t.Fatal("recovered too fast")
	}
}

func TestGroupViewOnlyCounts(t *testing.T) {
	m, _ := NewManager(t.TempDir(), nil)
	m.mu.Lock()
	m.groups = append(m.groups, &Group{ID: "g", Name: "New"})
	m.jobs = append(m.jobs,
		&Job{ID: "1", URL: "https://x/1", Name: "a", Status: StatusDone, Size: 100, Group: "g"},
		&Job{ID: "2", URL: "https://x/2", Name: "b", Status: StatusError, Error: msgDriveViewOnly, Size: 50, Group: "g"},
		&Job{ID: "3", URL: "https://x/3", Name: "c", Status: StatusError, Error: msgDriveViewOnly, Group: "g"})
	m.groupFinishedLocked("g")
	v := m.groupViewLocked(m.groups[0])
	m.mu.Unlock()
	if v.ViewOnly != 2 || v.Failed != 0 || v.Status != "Done" || v.Size != 100 {
		t.Fatalf("%+v", v)
	}
	ev, _ := m.Events(0)
	if len(ev) != 1 || !strings.Contains(ev[0].Body, "2 view-only files skipped") {
		t.Fatalf("%+v", ev)
	}
	if ids, _ := m.groupIDs("g", StatusError); len(ids) != 0 {
		t.Fatal("view-only files must not be retried")
	}
}
