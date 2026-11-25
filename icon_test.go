package main

import (
	"encoding/binary"
	"image/png"
	"os"
	"strings"
	"testing"
)

func TestIconFilesHaveAlphaAndSizes(t *testing.T) {
	checks := []struct {
		path string
		size int
	}{
		{"assets/icon.png", 1024},
		{"assets/icon-symbol.png", 1024},
		{"ui/icon.png", 128},
		{"ui/icon-32.png", 32},
		{"extension/icons/icon16.png", 16},
		{"extension/icons/icon32.png", 32},
		{"extension/icons/icon48.png", 48},
		{"extension/icons/icon128.png", 128},
	}
	for _, c := range checks {
		f, err := os.Open(c.path)
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		im, err := png.Decode(f)
		f.Close()
		if err != nil {
			t.Fatalf("%s: not a PNG: %v", c.path, err)
		}
		b := im.Bounds()
		if b.Dx() != c.size || b.Dy() != c.size {
			t.Fatalf("%s: size %dx%d, want %d", c.path, b.Dx(), b.Dy(), c.size)
		}
		_, _, _, a0 := im.At(0, 0).RGBA()
		if a0 > 0x2000 {
			t.Fatalf("%s: top-left corner should be transparent, alpha=%d", c.path, a0)
		}
		cx, cy := c.size/2, c.size/2
		_, _, _, ac := im.At(cx, cy).RGBA()
		if ac < 0xC000 {
			t.Fatalf("%s: centre should be opaque, alpha=%d", c.path, ac)
		}
	}
}

func TestWindowsIconContainsExpectedSizes(t *testing.T) {
	data, err := os.ReadFile("installer/BDDM.ico")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 6 || binary.LittleEndian.Uint16(data[0:2]) != 0 || binary.LittleEndian.Uint16(data[2:4]) != 1 {
		t.Fatal("installer/BDDM.ico is not a Windows icon")
	}
	n := int(binary.LittleEndian.Uint16(data[4:6]))
	want := map[int]bool{16: false, 24: false, 32: false, 48: false, 64: false, 96: false, 128: false, 256: false}
	for i := 0; i < n; i++ {
		off := 6 + i*16
		if off+16 > len(data) {
			t.Fatal("truncated icon directory")
		}
		w := int(data[off])
		if w == 0 {
			w = 256
		}
		if _, ok := want[w]; ok {
			want[w] = true
		}
	}
	for size, ok := range want {
		if !ok {
			t.Errorf("installer/BDDM.ico is missing the %dpx image", size)
		}
	}
}

func TestUIUsesNewIcons(t *testing.T) {
	pages := []string{
		"ui/index.html", "ui/confirm.html", "ui/drive.html", "ui/torrent.html", "ui/video.html", "ui/progress.html", "ui/complete.html", "ui/error.html",
	}
	for _, p := range pages {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if !strings.Contains(s, `href="icon-32.png"`) || !strings.Contains(s, `href="icon.png"`) {
			t.Errorf("%s is missing the new favicon links", p)
		}
		if strings.Contains(s, "fill='#3b82f6'") && strings.Contains(s, "M32 14v26") {
			t.Errorf("%s still uses the old SVG logo", p)
		}
	}
	index, err := os.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	s := string(index)
	if !strings.Contains(s, `src="icon-32.png"`) {
		t.Error("the header logo should use the 32px icon")
	}
	if !strings.Contains(s, `src="icon.png"`) {
		t.Error("the About logo should use the full icon")
	}
}
