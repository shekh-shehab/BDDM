package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestThemeSettingDefaultsAndPersists(t *testing.T) {
	if d := defaultSettings(); d.Theme != themeSystem {
		t.Fatalf("default theme = %q, want system", d.Theme)
	}
	for in, want := range map[string]string{"": "system", "system": "system", "light": "light", "dark": "dark", " DARK ": "dark", "blue": "system", "<script>": "system"} {
		if got := normalizeTheme(in); got != want {
			t.Errorf("normalizeTheme(%q) = %q, want %q", in, got, want)
		}
	}
	if !themeIsDark("dark", false) || themeIsDark("light", true) || !themeIsDark("system", true) || themeIsDark("system", false) || themeIsDark("odd", false) {
		t.Fatal("themeIsDark")
	}

	m, _ := newTestManager(t, 4)
	base := startUITestServer(t, m)
	getTheme := func() string {
		t.Helper()
		res, err := http.Get(base + "/api/theme")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if cc := res.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("/api/theme Cache-Control = %q", cc)
		}
		var body struct{ Theme string }
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body.Theme
	}
	if got := getTheme(); got != "system" {
		t.Fatalf("fresh theme = %q", got)
	}
	post := func(theme string) Settings {
		t.Helper()
		res, err := http.Post(base+"/api/settings", "application/json", strings.NewReader(`{"theme":"`+theme+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var s Settings
		_ = json.NewDecoder(res.Body).Decode(&s)
		return s
	}
	before := m.Settings()
	s := post("dark")
	if s.Theme != "dark" || getTheme() != "dark" || uiTheme() != "dark" {
		t.Fatalf("dark not saved: %q %q %q", s.Theme, getTheme(), uiTheme())
	}
	if s.DownloadDir != before.DownloadDir || s.Parts != before.Parts {
		t.Fatal("changing the theme must keep the other settings")
	}
	data, err := os.ReadFile(m.settingsPath())
	if err != nil || !strings.Contains(string(data), `"theme": "dark"`) {
		t.Fatalf("theme not written to settings.json: %v %s", err, data)
	}
	if s := post("bogus"); s.Theme != "system" {
		t.Fatalf("bad theme should fall back to system, got %q", s.Theme)
	}
	post("light")
	if getTheme() != "light" {
		t.Fatal("light")
	}
	post("system")
}

func TestThemeHostMessagesAndArgs(t *testing.T) {
	if c, a := parseHostMessage("bddm:theme:dark"); c != "theme" || a != "dark" {
		t.Fatalf("dark message: %q %q", c, a)
	}
	if c, a := parseHostMessage("bddm:theme:light"); c != "theme" || a != "light" {
		t.Fatalf("light message: %q %q", c, a)
	}
	if c, _ := parseHostMessage("bddm:theme:purple"); c != "" {
		t.Fatal("unknown theme message must be ignored")
	}
	if got := strings.Join(windowHostThemeArgs("dark"), " "); got != "-window-theme dark" {
		t.Fatalf("theme args = %q", got)
	}
	if got := strings.Join(windowHostThemeArgs("weird"), " "); got != "-window-theme system" {
		t.Fatalf("theme args = %q", got)
	}
}

var headRe = regexp.MustCompile(`(?s)<head>(.*?)</head>`)

func TestThemeScriptInEveryWindow(t *testing.T) {
	pages, _ := filepath.Glob(filepath.Join("ui", "*.html"))
	if len(pages) < 9 {
		t.Fatalf("expected all BDDM pages, got %v", pages)
	}
	for _, p := range pages {
		b, _ := os.ReadFile(p)
		head := headRe.FindStringSubmatch(string(b))
		if head == nil || !strings.Contains(head[1], `<script src="scheme.js"></script>`) {
			t.Errorf("%s: scheme.js must load in <head> so the theme applies before drawing", p)
		}
		if strings.Index(string(b), `href="theme.css"`) > strings.Index(string(b), `scheme.js`) {
			t.Errorf("%s: scheme.js should come after theme.css", p)
		}
	}
	js := readUI(t, "scheme.js")
	for _, want := range []string{"/api/theme", "bddm:theme:", "localStorage", "prefers-color-scheme", `addEventListener("storage"`, "bddmSetTheme", `get("scheme")`, "delete root.dataset.scheme", "setInterval(refresh"} {
		if !strings.Contains(js, want) {
			t.Errorf("scheme.js missing %q", want)
		}
	}
	idx := readUI(t, "index.html")
	for _, want := range []string{`<label for="setTheme">Theme</label>`, `<option value="system">System (follow Windows)</option>`, `<option value="light">Light</option>`, `<option value="dark">Dark</option>`, `$("setTheme").onchange`, `api("/api/settings", { theme })`, "Applies at once to every BDDM window."} {
		if !strings.Contains(idx, want) {
			t.Errorf("index.html missing %q", want)
		}
	}
	assertEnglishUI(t, "ui/scheme.js", js)
	assertEnglishUI(t, "ui/index.html", idx)
}

func chromeDOM(t *testing.T, chrome, url string) string {
	t.Helper()
	cmd := exec.Command(chrome, "--headless=new", "--disable-gpu", "--no-sandbox", "--virtual-time-budget=3000", "--dump-dom", url)
	cmd.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS=")
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("dump-dom %s: %v", url, err)
	}
	return string(out)
}

var htmlTagRe = regexp.MustCompile(`<html[^>]*>`)

// The saved theme reaches every window (no ?scheme in the address), and a
// change is picked up without reopening anything.
func TestThemeAppliesToEveryWindow(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no chrome")
	}
	m, _ := newTestManager(t, 4)
	base := startUITestServer(t, m)
	pages := []string{"/", "/?tab=settings", "/tray.html", "/video.html", "/confirm.html", "/progress.html", "/complete.html", "/error.html", "/drive.html", "/torrent.html", "/profile.html"}
	set := func(theme string) {
		s := m.Settings()
		s.Theme = theme
		if _, err := m.SetSettings(s); err != nil {
			t.Fatal(err)
		}
	}
	for _, theme := range []string{"dark", "light", "system"} {
		set(theme)
		for _, p := range pages {
			tag := htmlTagRe.FindString(chromeDOM(t, chrome, base+p))
			switch theme {
			case "system":
				if strings.Contains(tag, "data-scheme") {
					t.Errorf("%s with System theme should follow Windows, got %s", p, tag)
				}
			default:
				if !strings.Contains(tag, `data-scheme="`+theme+`"`) {
					t.Errorf("%s with %s theme: html tag %s", p, theme, tag)
				}
			}
		}
	}
	// ?scheme= for screenshots still wins over the saved theme.
	set("dark")
	if tag := htmlTagRe.FindString(chromeDOM(t, chrome, base+"/tray.html?scheme=light")); !strings.Contains(tag, `data-scheme="light"`) {
		t.Errorf("?scheme=light should win: %s", tag)
	}
	set("system")
}

func TestThemeScreenshots(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no chrome for a screenshot")
	}
	m, _ := newTestManager(t, 4)
	base := startUITestServer(t, m)
	set := func(theme string) {
		s := m.Settings()
		s.Theme = theme
		if _, err := m.SetSettings(s); err != nil {
			t.Fatal(err)
		}
	}
	dir := "ui-shots"
	// No ?scheme: the colours come from Settings > Theme (headless Chrome
	// itself reports a light system theme).
	set("dark")
	chromeShot(t, chrome, base+"/?tab=settings", filepath.Join(dir, "78-settings-theme-dark.png"), mainWindowWidth, mainWindowHeight)
	chromeShot(t, chrome, base+"/tray.html", filepath.Join(dir, "78-tray-theme-dark.png"), trayPopupWidth, trayPopupHeight)
	chromeShot(t, chrome, base+"/progress.html", filepath.Join(dir, "78-progress-theme-dark.png"), 460, 300)
	set("light")
	chromeShot(t, chrome, base+"/?tab=settings", filepath.Join(dir, "78-settings-theme-light.png"), mainWindowWidth, mainWindowHeight)
	chromeShot(t, chrome, base+"/tray.html", filepath.Join(dir, "78-tray-theme-light.png"), trayPopupWidth, trayPopupHeight)
	set("system")
	chromeShot(t, chrome, base+"/?tab=settings", filepath.Join(dir, "78-settings-theme-system.png"), mainWindowWidth, mainWindowHeight)
}
