package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Bug 85: the Download File Info window must size itself to the content
// (width capped) instead of staying at the large size it was opened at.
func TestConfirmPopupFitsContent(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no chrome for measuring")
	}
	page := readUI(t, "confirm.html")
	for _, want := range []string{
		`src="fit.js"`,
		"bddmFitWindow",
		fmt.Sprintf(`data-fit-min="%d"`, confirmFitMinWidth),
		fmt.Sprintf(`data-fit-max="%d"`, confirmFitMaxWidth),
	} {
		if !strings.Contains(page, want) {
			t.Errorf("ui/confirm.html missing %q", want)
		}
	}
	if strings.Contains(page, "height: 100%") {
		t.Error("confirm.html should not force html/body to 100% height (that leaves a blank band)")
	}

	m, _ := newTestManager(t, 4)
	var ids []string
	for i := 0; i < 3; i++ {
		pend, err := m.AddPending(AddRequest{URL: "https://example.com/Ubuntu-desktop-amd64.iso", Filename: "Ubuntu-desktop-amd64.iso"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, pend.ID)
	}
	base := startUITestServer(t, m)

	w1, h1 := chromeFit(t, chrome, base+"/confirm.html?id="+ids[0]+"&scheme=light", 600, 520)
	w2, h2 := chromeFit(t, chrome, base+"/confirm.html?id="+ids[1]+"&scheme=light", 900, 900)
	t.Logf("confirm fit: %dx%d (opened 600x520), %dx%d (opened 900x900)", w1, h1, w2, h2)
	if w1 != w2 || h1 != h2 {
		t.Errorf("size depends on the window it was opened in: %dx%d vs %dx%d", w1, h1, w2, h2)
	}
	if w1 < confirmFitMinWidth || w1 > confirmFitMaxWidth {
		t.Errorf("width %d should be %d..%d, not the 600 the window opened with", w1, confirmFitMinWidth, confirmFitMaxWidth)
	}
	if h1 >= 520 {
		t.Errorf("height %d is not compact: the window opened at 520 and should shrink to the form", h1)
	}
	if h1 < 200 {
		t.Errorf("height %d is oddly small", h1)
	}
}

// Bug 86: download rows must be one grid row (no empty band under the stats).
func TestDownloadRowsAreCompact(t *testing.T) {
	index := readUI(t, "index.html")
	if strings.Contains(index, "grid-row: 1 / span 4") || strings.Contains(index, "grid-row:1 / span 4") {
		t.Error("the icon must not span empty grid rows: that left a blank band under the stats")
	}
	if !strings.Contains(index, "align-items: start") {
		t.Error("row cards should align to the start so they shrink to their content")
	}
	assertEnglishUI(t, "ui/index.html", index)
}

func TestCompactLayoutScreenshots(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no chrome for a screenshot")
	}
	m, dl := newTestManager(t, 4)
	seedSidebarJobs(t, m, dl)
	// Each confirm page-view cancels the pending job on close, so measure
	// and each theme shot get their own job.
	var ids []string
	for i := 0; i < 3; i++ {
		pend, err := m.AddPending(AddRequest{URL: "https://example.com/Ubuntu-desktop-amd64.iso", Filename: "Ubuntu-desktop-amd64.iso"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, pend.ID)
	}
	base := startUITestServer(t, m)
	dir := filepath.Join("ui-shots")

	fw, fh := chromeFit(t, chrome, base+"/confirm.html?id="+ids[0]+"&scheme=light", confirmWindowWidth, confirmWindowHeight)
	t.Logf("confirm after-fit size for screenshots: %dx%d", fw, fh)
	for i, theme := range []string{"light", "dark"} {
		chromeShot(t, chrome, base+"/?scheme="+theme+"&filter=active", filepath.Join(dir, "85-86-main-"+theme+".png"), mainWindowWidth, mainWindowHeight)
		chromeShot(t, chrome, base+"/confirm.html?id="+ids[i+1]+"&scheme="+theme, filepath.Join(dir, "85-86-confirm-"+theme+".png"), fw, fh)
	}
	if _, err := os.Stat(filepath.Join(dir, "85-86-main-light.png")); err != nil {
		t.Error(err)
	}
}
