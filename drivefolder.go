package main

// drivefolder.go: the "Download Google Drive folder" window (ui/drive.html).
// The folder is listed in the background; the user picks files in a tree,
// and every picked file becomes a normal download in a folder structure
// like the one on Drive.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type driveFolder struct {
	id       string
	link     driveLink
	referer  string
	cookie   string
	state    string // "loading", "ready", "error"
	err      string
	root     *DriveNode
	foreign  int
	dir      string
	cancel   context.CancelFunc
	finished bool
}

// DriveFolderView is what the folder window shows.
type DriveFolderView struct {
	ID      string     `json:"id"`
	URL     string     `json:"url"`
	State   string     `json:"state"`
	Error   string     `json:"error,omitempty"`
	Root    *DriveNode `json:"root,omitempty"`
	Foreign int        `json:"foreign,omitempty"` // items left out (other parent)
	Dir     string     `json:"dir"`
}

// AddDriveFolder starts listing a Drive folder for the folder window.
func (m *Manager) AddDriveFolder(r AddRequest, l driveLink) (string, error) {
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return "", errors.New("BDDM is shutting down")
	}
	if m.driveFolders == nil {
		m.driveFolders = map[string]*driveFolder{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	f := &driveFolder{id: newID(), link: l, referer: strings.TrimSpace(r.Referer), cookie: strings.TrimSpace(r.Cookie),
		state: "loading", dir: m.settings.DownloadDir, cancel: cancel}
	m.driveFolders[f.id] = f
	m.mu.Unlock()

	go func() {
		defer cancel()
		root, foreign, err := listDriveFolder(ctx, m.client, l, reqHeaders{Referer: f.referer, Cookie: f.cookie})
		m.mu.Lock()
		defer m.mu.Unlock()
		if err != nil {
			f.state, f.err = "error", err.Error()
			if ctx.Err() == context.DeadlineExceeded {
				f.err = "Listing the folder took too long."
			}
			return
		}
		f.state, f.root, f.foreign = "ready", root, foreign
	}()
	go func() {
		time.Sleep(pendingTimeout)
		m.CancelDriveFolder(f.id)
	}()
	return f.id, nil
}

func (m *Manager) DriveFolder(id string) (DriveFolderView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.driveFolders[id]
	if f == nil {
		return DriveFolderView{}, errors.New("This folder window has expired. Paste the folder link again.")
	}
	u := "https://drive.google.com/drive/folders/" + f.link.ID
	return DriveFolderView{ID: f.id, URL: u, State: f.state, Error: f.err, Root: f.root, Foreign: f.foreign, Dir: f.dir}, nil
}

func (m *Manager) CancelDriveFolder(id string) {
	m.mu.Lock()
	f := m.driveFolders[id]
	delete(m.driveFolders, id)
	m.mu.Unlock()
	if f != nil {
		f.cancel()
	}
}

// DriveFolderResult tells the window what happened.
type DriveFolderResult struct {
	Added   int      `json:"added"`
	Skipped []string `json:"skipped,omitempty"` // downloaded before
	Folder  string   `json:"folder"`
}

// StartDriveFolder queues the chosen files. again=true downloads files that
// were downloaded before too.
func (m *Manager) StartDriveFolder(id string, ids []string, dir string, later, again bool) (DriveFolderResult, error) {
	var res DriveFolderResult
	dir = strings.TrimSpace(dir)
	m.mu.Lock()
	f := m.driveFolders[id]
	if f == nil || f.root == nil {
		m.mu.Unlock()
		return res, errors.New("This folder window has expired. Paste the folder link again.")
	}
	if dir == "" {
		dir = f.dir
	}
	m.mu.Unlock()
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) {
		return res, errors.New("Please choose a full folder path, for example D:\\Downloads.")
	}
	want := map[string]bool{}
	for _, x := range ids {
		want[x] = true
	}
	base := filepath.Join(dir, cleanDriveName(f.root.Name))
	res.Folder = base
	type pick struct {
		n   *DriveNode
		dir string
	}
	var picks []pick
	var walk func(n *DriveNode, d string)
	walk = func(n *DriveNode, d string) {
		for _, c := range n.Children {
			if c.Folder {
				walk(c, filepath.Join(d, cleanDriveName(c.Name)))
				continue
			}
			if want[c.ID] && c.Skip == "" {
				picks = append(picks, pick{c, d})
			}
		}
	}
	walk(f.root, base)
	if len(picks) == 0 {
		return res, errors.New("Nothing is selected.")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.driveFolders[id] == nil {
		return res, errors.New("This folder window has expired. Paste the folder link again.")
	}
	g := &Group{ID: newID(), Name: f.root.Name, Dir: base, URL: "https://drive.google.com/drive/folders/" + f.link.ID, Created: time.Now()}
	if g.Name == "" {
		g.Name = filepath.Base(base)
	}
	for _, p := range picks {
		n := p.n
		u := "https://drive.google.com/file/d/" + n.ID + "/view" + f.link.authQuery(true)
		name := cleanDriveName(n.Name)
		export := ""
		if n.DocType != "" {
			u = "https://docs.google.com/" + n.DocType + "/d/" + n.ID + "/edit" + f.link.authQuery(true)
			export = n.Export
			name = withExt(name, export)
		}
		if !again && m.findDoneLocked(u) != nil {
			res.Skipped = append(res.Skipped, n.Name)
			continue
		}
		if err := os.MkdirAll(p.dir, 0755); err != nil {
			return res, errors.New("Cannot create the folder " + p.dir + ": " + err.Error())
		}
		j := &Job{
			ID: newID(), URL: u, Referer: f.referer, Cookie: f.cookie, Want: name, Dir: p.dir,
			Parts: m.settings.Parts, Started: time.Now(), Export: export, Size: n.Size, Group: g.ID,
		}
		if later {
			j.Status = StatusPaused
		} else {
			j.Status = StatusQueued
		}
		m.jobs = append(m.jobs, j)
		res.Added++
	}
	if res.Added > 0 {
		m.groups = append(m.groups, g)
		m.saveGroupsLocked()
	}
	m.scheduleLocked()
	m.saveHistoryLocked()
	f.finished = true
	return res, nil
}
