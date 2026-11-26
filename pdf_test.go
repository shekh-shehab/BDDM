package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testPage(w, h int, asJPEG bool) string {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 200, 255})
		}
	}
	var b bytes.Buffer
	mime := "image/png"
	if asJPEG {
		jpeg.Encode(&b, img, nil)
		mime = "image/jpeg"
	} else {
		png.Encode(&b, img)
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b.Bytes())
}

func TestPDFFileName(t *testing.T) {
	for in, want := range map[string]string{
		"Lecture 3.pdf - Google Drive": "Lecture 3.pdf",
		"Notes.docx":                   "Notes.pdf",
		"Report v1.2":                  "Report v1.2.pdf",
		"":                             "Google Drive document.pdf",
		"a/b:c.pdf":                    "a_b_c.pdf",
	} {
		if got := pdfFileName(in); got != want && !(in == "a/b:c.pdf" && strings.HasSuffix(got, ".pdf") && !strings.ContainsAny(got, "/:")) {
			t.Errorf("pdfFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPDFCaptureFormats(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	dl := filepath.Join(dir, "dl")
	s := m.Settings()
	s.DownloadDir = dl
	if _, err := m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	run := func(o PDFOptions) (JobView, int, error) {
		id, err := m.PDFStart("Week 1 Notes.pdf - Google Drive", "https://drive.google.com/file/d/x/view", 3)
		if err != nil {
			t.Fatal(err)
		}
		// Out of order, PNG and JPEG mixed, one page sent twice (a retry).
		for _, p := range []struct {
			n    int
			data string
		}{{2, testPage(80, 60, false)}, {0, testPage(60, 80, true)}, {1, testPage(60, 80, false)}, {1, testPage(60, 80, false)}} {
			if err := m.PDFPage(id, p.n, p.data); err != nil {
				t.Fatal(err)
			}
		}
		if err := m.PDFPage(id, 3, testPage(10, 10, false)); err == nil {
			t.Fatal("page beyond the count was accepted")
		}
		if err := m.PDFPage(id, 0, "data:image/png;base64,AAAA"); err == nil {
			t.Fatal("broken image was accepted")
		}
		return m.PDFFinish(id, o)
	}

	j, pages, err := run(PDFOptions{Format: "pdf", PageSize: "fit"})
	if err != nil || pages != 3 || j.Name != "Week 1 Notes.pdf" || j.Status != StatusDone {
		t.Fatalf("pdf: %+v %d %v", j, pages, err)
	}
	data, _ := os.ReadFile(filepath.Join(dl, j.Name))
	if !bytes.HasPrefix(data, []byte("%PDF-1.4")) || bytes.Count(data, []byte("/Type /Page ")) != 3 || !bytes.Contains(data, []byte("%%EOF")) {
		t.Fatalf("bad pdf")
	}
	if int64(len(data)) != j.Size {
		t.Fatalf("size %d vs %d", len(data), j.Size)
	}
	// Second time: a new name, nothing replaced; A4 landscape; edited name.
	j2, _, err := run(PDFOptions{Format: "pdf", PageSize: "a4", Orientation: "landscape", Name: "Week 1 Notes"})
	if err != nil || j2.Name != "Week 1 Notes (1).pdf" {
		t.Fatalf("second: %+v %v", j2, err)
	}
	data, _ = os.ReadFile(filepath.Join(dl, j2.Name))
	if bytes.Count(data, []byte("/MediaBox [0 0 841.89 595.28]")) != 3 {
		t.Fatalf("not A4 landscape")
	}

	jz, _, err := run(PDFOptions{Format: "zip", Name: "Pages"})
	if err != nil || jz.Name != "Pages.zip" {
		t.Fatalf("zip: %+v %v", jz, err)
	}
	zr, err := zip.OpenReader(filepath.Join(dl, jz.Name))
	if err != nil || len(zr.File) != 3 || zr.File[0].Name != "Pages - page 001.png" {
		t.Fatalf("zip content: %v", err)
	}
	zr.Close()

	jp, _, err := run(PDFOptions{Format: "png", Name: "Pages"})
	if err != nil || jp.Name != "Pages" {
		t.Fatalf("png: %+v %v", jp, err)
	}
	list, _ := os.ReadDir(filepath.Join(dl, "Pages"))
	if len(list) != 3 {
		t.Fatalf("png files: %d", len(list))
	}
	if err := m.Delete(jp.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dl, "Pages")); !os.IsNotExist(err) {
		t.Fatal("png folder not deleted")
	}

	// Missing pages: refused unless allowed.
	id, _ := m.PDFStart("x", "", 3)
	m.PDFPage(id, 0, testPage(20, 20, false))
	if _, _, err := m.PDFFinish(id, PDFOptions{}); err == nil {
		t.Fatal("missing pages accepted")
	}
	if _, n, err := m.PDFFinish(id, PDFOptions{AllowMissing: true}); err != nil || n != 1 {
		t.Fatalf("allowMissing: %d %v", n, err)
	}
	// A captured file is never a "downloaded before" match.
	if m.FindDone("https://drive.google.com/file/d/x/view") != nil {
		t.Fatal("capture counted as downloaded before")
	}
}

func TestPDFFinishUsesChosenDir(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	dl := filepath.Join(root, "dl")
	s := m.Settings()
	s.DownloadDir = dl
	if _, err := m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	chosen := filepath.Join(root, "picked", "pdfs")
	id, err := m.PDFStart("Notes.pdf", "https://drive.google.com/file/d/x/view", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.PDFPage(id, 0, testPage(20, 20, false)); err != nil {
		t.Fatal(err)
	}
	j, pages, err := m.PDFFinish(id, PDFOptions{Format: "pdf", Dir: chosen})
	if err != nil || pages != 1 {
		t.Fatalf("finish: %+v %d %v", j, pages, err)
	}
	if j.Dir != filepath.Clean(chosen) {
		t.Fatalf("dir: %q, want %q", j.Dir, chosen)
	}
	if _, err := os.Stat(filepath.Join(chosen, j.Name)); err != nil {
		t.Fatalf("saved file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dl, j.Name)); err == nil {
		t.Fatal("file was also written to the default folder")
	}
	if got := m.PDFDefaultDir("Notes.pdf", "pdf"); got != filepath.Clean(chosen) {
		t.Fatalf("remembered Documents folder: %q, want %q", got, chosen)
	}
	time.Sleep(200 * time.Millisecond) // rememberDirLocked saves settings in the background
}

func TestPDFFinishRejectsRelativeDir(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := m.PDFStart("Notes.pdf", "https://drive.google.com/file/d/x/view", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.PDFPage(id, 0, testPage(20, 20, false)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.PDFFinish(id, PDFOptions{Format: "pdf", Dir: "relative/out"}); err == nil {
		t.Fatal("relative dir was accepted")
	}
	// The capture must still be there so the user can try again.
	if _, _, err := m.PDFFinish(id, PDFOptions{Format: "pdf", Dir: filepath.Join(root, "ok")}); err != nil {
		t.Fatalf("retry after bad dir: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // rememberDirLocked saves settings in the background
}

func TestPDFDefaultDir(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	docs := filepath.Join(root, "Documents")
	imgs := filepath.Join(root, "Pictures")
	s := m.Settings()
	s.DownloadDir = filepath.Join(root, "dl")
	for i := range s.Categories {
		switch s.Categories[i].Name {
		case "Documents":
			s.Categories[i].Dir = docs
		case "Images":
			s.Categories[i].Dir = imgs
		}
	}
	if _, err := m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := m.PDFDefaultDir("Notes.docx", "pdf"); got != docs {
		t.Fatalf("pdf default: %q, want %q", got, docs)
	}
	if got := m.PDFDefaultDir("Notes", "png"); got != imgs {
		t.Fatalf("png default: %q, want %q", got, imgs)
	}
}

func TestSavePDFPanelAsksFolder(t *testing.T) {
	js, err := os.ReadFile(filepath.Join("extension", "savepdf.js"))
	if err != nil {
		t.Fatal(err)
	}
	bg, err := os.ReadFile(filepath.Join("extension", "savepdf-bg.js"))
	if err != nil {
		t.Fatal(err)
	}
	assertEnglishUI(t, "extension/savepdf.js", string(js))
	assertEnglishUI(t, "extension/savepdf-bg.js", string(bg))
	src := string(js) + "\n" + string(bg)
	for _, want := range []string{
		`"Save to"`,
		`"Change..."`,
		`id: "dir"`,
		`id: "browse"`,
		`type: "pdfSaveDir"`,
		`type: "pdfPickFolder"`,
		`dir,`,
		`"/ext/pdf/savedir"`,
		`"/ext/pdf/pickfolder"`,
		`Choose a folder to save to.`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("Save as PDF panel missing %q", want)
		}
	}
}
