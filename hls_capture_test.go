package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHLSCaptureHelpers(t *testing.T) {
	cmd := exec.Command("node", filepath.Join("extension", "hls_capture_test.js"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hls_capture_test.js: %v\n%s", err, out)
	}
}

func TestHLSCaptureWiredAndExistingFlowsUnchanged(t *testing.T) {
	cmd := exec.Command("python3", "build.py")
	cmd.Dir = "extension"
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build.py: %v\n%s", err, out)
	}

	if !fileContains(t, filepath.Join("extension", "build.py"), `"hls_capture.js"`) {
		t.Error("build.py FILES must include hls_capture.js")
	}
	if !fileExists(filepath.Join("extension", "dist", "firefox", "hls_capture.js")) {
		t.Error("Firefox build is missing hls_capture.js")
	}
	if !fileExists(filepath.Join("extension", "dist", "chromium", "hls_capture.js")) {
		t.Error("Chromium build is missing hls_capture.js")
	}

	ff, err := os.ReadFile(filepath.Join("extension", "dist", "firefox", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var firefox map[string]any
	if err := json.Unmarshal(ff, &firefox); err != nil {
		t.Fatal(err)
	}
	bg, _ := firefox["background"].(map[string]any)
	scripts, _ := bg["scripts"].([]any)
	joined := joinAny(scripts)
	if !strings.Contains(joined, "hls_capture.js") {
		t.Error("Firefox background scripts should load hls_capture.js")
	}
	hi, vi := strings.Index(joined, "hls_capture.js"), strings.Index(joined, "videos.js")
	if hi < 0 || vi < 0 || hi > vi {
		t.Errorf("hls_capture.js must load before videos.js, got %q", joined)
	}

	cr, err := os.ReadFile(filepath.Join("extension", "dist", "chromium", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var chrome map[string]any
	if err := json.Unmarshal(cr, &chrome); err != nil {
		t.Fatal(err)
	}
	if permListContains(chrome["permissions"], "webRequestBlocking") {
		t.Error("Chromium must still not include webRequestBlocking")
	}

	bgjs, err := os.ReadFile(filepath.Join("extension", "background.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bgjs), `importScripts("hls_capture.js", "videos.js")`) {
		t.Error(`background.js should importScripts("hls_capture.js", "videos.js")`)
	}

	vj, err := os.ReadFile(filepath.Join("extension", "videos.js"))
	if err != nil {
		t.Fatal(err)
	}
	vs := string(vj)
	for _, want := range []string{
		"shouldFilterDisguisedPlaylist",
		"shouldAutoOfferStream",
		"maybeAutoOffer",
		"filterResponseData",
		`if (typeof IS_FIREFOX !== "undefined" && IS_FIREFOX && api.webRequest.filterResponseData)`,
		`["blocking", "responseHeaders"]`,
		"ensureReqOrigin",
	} {
		if !strings.Contains(vs, want) {
			t.Errorf("videos.js missing %q", want)
		}
	}
	// Auto-popup is skipped for YouTube/Facebook (existing flows unchanged).
	if !strings.Contains(vs, "YouTube, Facebook") && !strings.Contains(vs, "shouldAutoOfferStream") {
		t.Error("videos.js should mention the YouTube/Facebook auto-offer skip")
	}

	cj, err := os.ReadFile(filepath.Join("extension", "content.js"))
	if err != nil {
		t.Fatal(err)
	}
	cs := string(cj)
	if !strings.Contains(cs, `"omit"`) || !strings.Contains(cs, `"include"`) {
		t.Error("content.js peek should try credentials omit then include")
	}
	omitAt, incAt := strings.Index(cs, `"omit"`), strings.Index(cs, `"include"`)
	if omitAt < 0 || incAt < 0 || omitAt > incAt {
		t.Error("content.js must try omit before include")
	}

	// File early-capture path is unchanged and still Firefox-only.
	if !strings.Contains(string(bgjs), "installFirefoxEarlyCapture") {
		t.Error("Firefox early file capture must still be wired")
	}
}
