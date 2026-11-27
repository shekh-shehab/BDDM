package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirefoxCaptureHelpers(t *testing.T) {
	cmd := exec.Command("node", filepath.Join("extension", "firefox_capture_test.js"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("firefox_capture_test.js: %v\n%s", err, out)
	}
}

func TestFirefoxManifestGetsBlockingPermissionOnly(t *testing.T) {
	cmd := exec.Command("python3", "build.py")
	cmd.Dir = "extension"
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build.py: %v\n%s", err, out)
	}

	ff, err := os.ReadFile(filepath.Join("extension", "dist", "firefox", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var firefox map[string]any
	if err := json.Unmarshal(ff, &firefox); err != nil {
		t.Fatal(err)
	}
	if !permListContains(firefox["permissions"], "webRequest") {
		t.Error("Firefox manifest should still include webRequest")
	}
	if !permListContains(firefox["permissions"], "webRequestBlocking") {
		t.Error("Firefox manifest should include webRequestBlocking for early capture")
	}
	if !permListContains(firefox["permissions"], "downloads") {
		t.Error("Firefox merge should keep the base permissions (downloads)")
	}
	bg, _ := firefox["background"].(map[string]any)
	scripts, _ := bg["scripts"].([]any)
	joined := joinAny(scripts)
	if !strings.Contains(joined, "firefox_capture.js") {
		t.Error("Firefox background scripts should load firefox_capture.js")
	}
	if !strings.Contains(joined, "background.js") {
		t.Error("Firefox background scripts should still load background.js")
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
		t.Error("Chromium manifest must not include webRequestBlocking (MV3 would reject it)")
	}
	if !permListContains(chrome["permissions"], "webRequest") {
		t.Error("Chromium should still have webRequest")
	}
	cbg, _ := chrome["background"].(map[string]any)
	if _, ok := cbg["service_worker"]; !ok {
		t.Error("Chromium should still use a service_worker background")
	}
	if _, ok := cbg["scripts"]; ok {
		t.Error("Chromium must not switch to Firefox-style background scripts")
	}
}

func TestFirefoxEarlyCaptureWiredAndChromePathUnchanged(t *testing.T) {
	bg, err := os.ReadFile(filepath.Join("extension", "background.js"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(bg)
	for _, want := range []string{
		`importScripts("compat.js", "firefox_capture.js", "handback.js")`,
		"installFirefoxEarlyCapture",
		"onFirefoxHeadersReceived",
		"shouldInterceptFirefoxDownload",
		`["blocking", "responseHeaders"]`,
		"onDeterminingFilename",
		"onCreated",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("background.js missing %q", want)
		}
	}
	// Chrome still decides with onDeterminingFilename; the early intercept
	// is wrapped in IS_FIREFOX so Chromium never registers it.
	if !strings.Contains(s, "if (!IS_FIREFOX") && !strings.Contains(s, "if (!IS_FIREFOX ||") {
		t.Error("early capture must be Firefox-only")
	}
	if !strings.Contains(s, "isBlobOrDataUrl") && !fileContains(t, filepath.Join("extension", "firefox_capture.js"), "isBlobOrDataUrl") {
		t.Error("blob:/data: must be rejected by the Firefox helper")
	}

	base, err := os.ReadFile(filepath.Join("extension", "manifests", "base.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(base), `"version": "1.5.8"`) {
		t.Errorf("extension version should be 1.5.8, got %s", base)
	}
	ff, err := os.ReadFile(filepath.Join("extension", "manifests", "firefox.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ff), "webRequestBlocking") {
		t.Error("firefox.json should add webRequestBlocking")
	}
	chrom, err := os.ReadFile(filepath.Join("extension", "manifests", "chromium.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(chrom), "webRequestBlocking") {
		t.Error("chromium.json must not mention webRequestBlocking")
	}
}

func permListContains(v any, want string) bool {
	list, ok := v.([]any)
	if !ok {
		return false
	}
	for _, x := range list {
		s, _ := x.(string)
		if s == want {
			return true
		}
	}
	return false
}

func joinAny(list []any) string {
	parts := make([]string, 0, len(list))
	for _, x := range list {
		s, _ := x.(string)
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func fileContains(t *testing.T, path, want string) bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
		return false
	}
	return strings.Contains(string(b), want)
}
