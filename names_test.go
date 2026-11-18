package main

// 1.4.2 part 2: file names from the page title.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanTitle(t *testing.T) {
	cases := map[string]string{
		"Tokyo Revengers: War of the Three Titans Arc Episode 1 - AnikotoTV":   "Tokyo Revengers - War of the Three Titans Arc Episode 1",
		"Watch Tokyo Revengers Episode 1 English Sub Online Free - Anikoto.bz": "Tokyo Revengers Episode 1 English Sub",
		"(3) Big Buck Bunny | Example.com":                                     "Big Buck Bunny",
		"example.com - My Holiday Video":                                       "My Holiday Video",
		"Song Name - The Artist":                                               "Song Name - The Artist",
		"What? A <b>bold</b> \"quote\" / slash * star":                         "What A bbold-b 'quote' - slash star",
		"Report Q3 2026 &amp; Notes":                                           "Report Q3 2026 & Notes",
		"   ":                                                                  "",
		"CON":                                                                  "CON_",
		"Free Movies Online | Watch HD":                                        "Free Movies",
	}
	for in, want := range cases {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("word ", 60)
	if got := cleanTitle(long); len(got) > maxTitleName || strings.HasSuffix(got, " ") || got == "" {
		t.Errorf("long title: %d bytes %q", len(got), got)
	}
	if got := cleanTitle(strings.Repeat("ঘ", 100)); len(got) > maxTitleName || !strings.HasPrefix(got, "ঘ") || strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("Bangla title cut badly: %q", got)
	}
}

func TestUselessName(t *testing.T) {
	for _, n := range []string{"stream.mp4", "video.mp4", "download", "index.html", "master.m3u8", "videoplayback",
		"Video (1).mp4", "stream_2.mp4", "1699999999.mp4", "5d0d7ec084b840864a90a0fc.mp4",
		"0f8fad5b-d9cb-469f-a165-70867728950e.mkv", "aB3dE5fG7hJ9kL1mN2pQ4rS6tU8.mp4", "download.php", ""} {
		if !uselessName(n) {
			t.Errorf("%q should be useless", n)
		}
	}
	for _, n := range []string{"Big Buck Bunny.mp4", "setup-x64.exe", "IMG_2041.jpg", "report-2026.pdf", "ubuntu-24.04-desktop-amd64.iso"} {
		if uselessName(n) {
			t.Errorf("%q is a real name", n)
		}
	}
	if got := nameWithTitle("stream.mp4", "Episode 5 - Site.tv"); got != "Episode 5.mp4" {
		t.Errorf("got %q", got)
	}
	if got := nameWithTitle("download.php", "Annual Report"); got != "Annual Report" {
		t.Errorf("got %q", got)
	}
	if got := nameWithTitle("Real Name.zip", "Page"); got != "Real Name.zip" {
		t.Errorf("a good name was replaced: %q", got)
	}
	if got := nameWithTitle("stream.mp4", ""); got != "stream.mp4" {
		t.Errorf("no title: %q", got)
	}
}

// A link called stream.mp4 is saved under the page's title; a server name
// (Content-Disposition) still wins over the title.
func TestFileNamedAfterPage(t *testing.T) {
	data := bytes.Repeat([]byte("v"), 300<<10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/named/") {
			w.Header().Set("Content-Disposition", `attachment; filename="Server Name.mp4"`)
		}
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "x", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	m, dl := newTestManager(t, 2)
	for _, c := range []struct{ path, want string }{
		{"/v/stream.mp4", "My Show Episode 3.mp4"},
		{"/named/stream.mp4", "Server Name.mp4"},
	} {
		v, err := m.Add(AddRequest{URL: srv.URL + c.path, Filename: "stream.mp4", Title: "Watch My Show Episode 3 Online - Site.example"})
		if err != nil {
			t.Fatal(err)
		}
		done := waitDone(t, m, v.ID, time.Minute)
		if done.Status != StatusDone || done.Name != c.want {
			t.Fatalf("%s: %s %q (%s), want %q", c.path, done.Status, done.Name, done.Error, c.want)
		}
		if _, err := os.Stat(filepath.Join(done.Dir, done.Name)); err != nil {
			t.Fatal(err)
		}
	}
	_ = dl
	// The "Download File Info" window suggests the title too.
	pv, err := m.AddPending(AddRequest{URL: srv.URL + "/p/video.mp4", Title: "Lecture 7 | Uni.edu"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		p, err := m.Pending(pv.ID)
		if err == nil && !p.Checking {
			if p.Name != "Lecture 7.mp4" {
				t.Fatalf("suggested %q", p.Name)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the window never finished")
}
