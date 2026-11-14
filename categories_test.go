package main

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestCategoryOf(t *testing.T) {
	s := defaultSettings()
	s.fix()
	for name, want := range map[string]string{
		"movie.MKV": "Video", "song.mp3": "Audio", "a.pdf": "Documents", "x.tar.gz": "Compressed",
		"setup.exe": "Programs", "p.JPG": "Images", "noext": "Other", "data.xyz": "Other",
	} {
		if got := s.categoryOf(name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
	// Edited lists are cleaned; unknown categories are dropped, missing ones come back.
	got := fixCategories([]Category{{Name: "video", Exts: " .MP4, *.xyz;mp4 "}, {Name: "Mine", Exts: "abc"}})
	if len(got) != len(defaultCategories()) || got[0].Exts != "mp4 xyz" {
		t.Fatalf("fixCategories: %+v", got)
	}
}

// Files go to the folder of their type; a folder picked in the "Download
// File Info" window is remembered for that type.
func TestCategoryFolders(t *testing.T) {
	data := randomData(t, 300<<10)
	fs := &flexServer{}
	fs.set(data, `"v"`)
	srv := httptest.NewServer(fs)
	defer srv.Close()
	m, dl := newTestManager(t, 2)
	videos := filepath.Join(filepath.Dir(dl), "Videos")
	s := m.Settings()
	s.Categories = []Category{{Name: "Video", Exts: "mp4", Dir: videos}}
	if _, err := m.SetSettings(s); err != nil {
		t.Fatal(err)
	}

	j, _ := m.Add(AddRequest{URL: srv.URL + "/clip.mp4", Force: true})
	if v := waitDone(t, m, j.ID, time.Minute); v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	checkFile(t, filepath.Join(videos, "clip.mp4"), data)
	j, _ = m.Add(AddRequest{URL: srv.URL + "/notes.pdf", Force: true})
	waitDone(t, m, j.ID, time.Minute)
	checkFile(t, filepath.Join(dl, "notes.pdf"), data)

	// Confirm window: the suggested folder is the Documents folder (main).
	p, err := m.AddPending(AddRequest{URL: srv.URL + "/book.pdf", Filename: "book.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	pv, _ := m.Pending(p.ID)
	if pv.Category != "Documents" || pv.Dir != dl {
		t.Fatalf("pending: %s %s", pv.Category, pv.Dir)
	}
	docs := filepath.Join(filepath.Dir(dl), "Docs")
	if _, err := m.StartPending(p.ID, "", docs, false, ""); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m, p.ID, time.Minute)
	checkFile(t, filepath.Join(docs, "book.pdf"), data)
	cur := m.Settings()
	if got := cur.dirFor("next.docx"); got != docs {
		t.Fatalf("folder not remembered: %s", got)
	}
	// Without a folder from the window, the type's folder is used.
	p, _ = m.AddPending(AddRequest{URL: srv.URL + "/movie.mp4", Filename: "movie.mp4"})
	m.StartPending(p.ID, "", "", false, "")
	waitDone(t, m, p.ID, time.Minute)
	checkFile(t, filepath.Join(videos, "movie.mp4"), data)
	time.Sleep(100 * time.Millisecond) // the settings file is written in the background
}
