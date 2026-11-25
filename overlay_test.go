package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOverlayRulesHelpers(t *testing.T) {
	cmd := exec.Command("node", filepath.Join("extension", "overlay_rules_test.js"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("overlay_rules_test.js: %v\n%s", err, out)
	}
}

func TestOverlayWiredAndAutoOfferUnchanged(t *testing.T) {
	cmd := exec.Command("python3", "build.py")
	cmd.Dir = "extension"
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build.py: %v\n%s", err, out)
	}
	if !fileExists(filepath.Join("extension", "dist", "firefox", "overlay.js")) ||
		!fileExists(filepath.Join("extension", "dist", "chromium", "overlay.js")) ||
		!fileExists(filepath.Join("extension", "dist", "firefox", "overlay_rules.js")) {
		t.Fatal("overlay scripts missing from the built extension")
	}

	for _, dest := range []string{
		filepath.Join("extension", "dist", "firefox", "manifest.json"),
		filepath.Join("extension", "dist", "chromium", "manifest.json"),
	} {
		b, err := os.ReadFile(dest)
		if err != nil {
			t.Fatal(err)
		}
		var man map[string]any
		if err := json.Unmarshal(b, &man); err != nil {
			t.Fatal(err)
		}
		cs, _ := man["content_scripts"].([]any)
		joined := ""
		idle := false
		allFrames := false
		for _, raw := range cs {
			m, _ := raw.(map[string]any)
			js := joinAny(m["js"].([]any))
			joined += js + " "
			if strings.Contains(js, "overlay.js") {
				if m["all_frames"] == true {
					allFrames = true
				}
				if m["run_at"] == "document_idle" {
					idle = true
				}
			}
		}
		if !strings.Contains(joined, "overlay_rules.js") || !strings.Contains(joined, "overlay.js") {
			t.Errorf("%s content_scripts missing overlay files: %s", dest, joined)
		}
		if !allFrames || !idle {
			t.Errorf("%s overlay script must run in all frames at document_idle", dest)
		}
	}

	vj, err := os.ReadFile(filepath.Join("extension", "videos.js"))
	if err != nil {
		t.Fatal(err)
	}
	vs := string(vj)
	for _, want := range []string{"handleOverlayDownload", "overlayClickTarget", "overlayDownload"} {
		if !strings.Contains(vs, want) {
			t.Errorf("videos.js missing %q", want)
		}
	}
	oj, err := os.ReadFile(filepath.Join("extension", "overlay.js"))
	if err != nil {
		t.Fatal(err)
	}
	osrc := string(oj)
	for _, want := range []string{"Download with BDDM", "shouldShowButton", "pickActiveVideo", "fullscreen", "overlayDownload"} {
		if !strings.Contains(osrc, want) {
			t.Errorf("overlay.js missing %q", want)
		}
	}
	// The button must not create a new auto-popup path on YouTube/Facebook.
	if strings.Contains(osrc, "maybeAutoOffer") {
		t.Error("the overlay content script must not auto-offer (item 77)")
	}
}

func TestOverlayThumbnailAndClickJob(t *testing.T) {
	m, _ := newTestManager(t, 1)
	thumb := "https://i.ytimg.com/vi/abc/hqdefault.jpg"
	j, err := m.AddVideoPending(AddRequest{
		URL: "https://www.youtube.com/watch?v=dQw4w9wgWcQ", Kind: "video",
		Title: "Never Gonna Give You Up", Thumbnail: thumb, Dialog: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	v, err := m.VideoPending(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.URL != "https://www.youtube.com/watch?v=dQw4w9wgWcQ" {
		t.Fatalf("YouTube job URL: %q", v.URL)
	}
	if v.Title != "Never Gonna Give You Up" {
		t.Fatalf("title: %q", v.Title)
	}
	if v.Info == nil || v.Info.Thumbnail != thumb {
		t.Fatalf("preview thumbnail missing: %+v", v.Info)
	}

	// HLS stream click: pending job keeps the playlist URL and stream kind.
	hls := "https://watch.movielink.tv/m/token123"
	j2, err := m.AddVideoPending(AddRequest{
		URL: hls, Kind: "video", Stream: "hls", Title: "High Heel Homicide",
		Thumbnail: "data:image/jpeg;base64,AAAA", Dialog: true,
		Frame: "https://qmr7cm.movielinkbd.at/p/player",
		Page:  "https://qmr7cm.movielinkbd.at/download18plus/x",
	})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	job := m.findLocked(j2.ID)
	if job == nil || job.Stream != "hls" || job.URL != hls {
		m.mu.Unlock()
		t.Fatalf("HLS job: %+v", job)
	}
	if job.refFrame != "https://qmr7cm.movielinkbd.at/p/player" {
		m.mu.Unlock()
		t.Fatalf("frame: %q", job.refFrame)
	}
	m.mu.Unlock()
	v2, err := m.VideoPending(j2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Info == nil || !strings.HasPrefix(v2.Info.Thumbnail, "data:image/jpeg;") {
		t.Fatalf("HLS preview: %+v", v2.Info)
	}

	// Direct file from the overlay button opens the video window (Dialog).
	if !isVideoRequest(AddRequest{URL: "https://cdn.example/movie.mp4", Kind: "video", Dialog: true}) {
		t.Fatal("overlay click on a direct file should open the video window")
	}
	// Without Dialog, a video file still uses the fast engine (existing path).
	if isVideoRequest(AddRequest{URL: "https://cdn.example/movie.mp4", Kind: "video"}) {
		t.Fatal("a regular file send must not switch to the video window")
	}
	if safeVideoThumb("javascript:alert(1)") != "" || safeVideoThumb("https://ok.example/p.jpg") != "https://ok.example/p.jpg" {
		t.Fatal("safeVideoThumb")
	}
}
