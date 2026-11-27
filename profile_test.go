package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestProfileSite(t *testing.T) {
	cases := []struct{ url, site, name string }{
		{"https://www.tiktok.com/@soloshinee", "TikTok", "@soloshinee"},
		{"https://www.tiktok.com/@solo.shinee?lang=en", "TikTok", "@solo.shinee"},
		{"https://www.instagram.com/natgeo/", "Instagram", "natgeo"},
		{"https://instagram.com/natgeo/reels/", "Instagram", "natgeo"},
		{"https://www.redgifs.com/users/some-user", "RedGIFs", "some-user"},
		{"https://fansly.com/somecreator/posts", "Fansly", "somecreator"},
		{"https://x.com/nasa/media", "X", "@nasa"},
		{"https://www.youtube.com/@mkbhd/videos", "YouTube", "@mkbhd"},
	}
	for _, c := range cases {
		site, name, ok := profileSite(c.url)
		if !ok || site != c.site || name != c.name {
			t.Errorf("%s: got %q %q %v, want %q %q", c.url, site, name, ok, c.site, c.name)
		}
	}
	for _, u := range []string{
		"https://www.tiktok.com/@soloshinee/video/7312345678901234567", // one video
		"https://www.instagram.com/p/Cabc123/",
		"https://www.instagram.com/explore/",
		"https://www.instagram.com/accounts/login/",
		"https://www.redgifs.com/watch/abc",
		"https://fansly.com/post/123456",
		"https://fansly.com/explore",
		"https://x.com/nasa/status/123",
		"https://x.com/home",
		"https://www.youtube.com/watch?v=abc",
		"https://example.com/@someone",
		"ftp://www.tiktok.com/@a",
	} {
		if site, _, ok := profileSite(u); ok {
			t.Errorf("%s: should not be a profile, got %s", u, site)
		}
	}
}

func TestPostKey(t *testing.T) {
	cases := map[string]string{
		"https://www.instagram.com/p/Cabc_1-2/":              "ig:Cabc_1-2",
		"https://www.instagram.com/natgeo/reel/Cxyz/":        "ig:Cxyz",
		"https://www.tiktok.com/@a/video/7312":               "tt:7312",
		"https://www.tiktok.com/@a/photo/7313":               "tt:7313",
		"https://www.redgifs.com/watch/HappyBigCat":          "rg:happybigcat",
		"https://fansly.com/post/555":                        "fs:555",
		"https://twitter.com/nasa/status/99":                 "x:99",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=x": "yt:dQw4w9WgXcQ",
		"https://www.youtube.com/shorts/abc":                 "yt:abc",
		"https://www.tiktok.com/@a":                          "",
		"https://example.com/p/1":                            "",
	}
	for u, want := range cases {
		if got := postKey(u); got != want {
			t.Errorf("postKey(%s) = %q, want %q", u, got, want)
		}
	}
	if got := profileFolderName("TikTok", "@soloshinee"); got != "soloshinee (TikTok)" {
		t.Errorf("folder %q", got)
	}
	if got := profileFolderName("example.com", "gallery"); got != "gallery" {
		t.Errorf("folder %q", got)
	}
}

// gallery-dl -J output of an Instagram profile: a carousel of two photos
// and a video (Reel), plus an avatar that is not media of a post.
const instagramGDL = `[
 [2, {"post_shortcode": "Cabc", "description": "Beach day"}],
 [3, "https://cdn.test/a1.jpg", {"post_shortcode": "Cabc", "post_url": "https://www.instagram.com/p/Cabc/", "description": "Beach day", "num": 1, "extension": "jpg", "display_url": "https://cdn.test/a1_s.jpg"}],
 [3, "https://cdn.test/a2.jpg", {"post_shortcode": "Cabc", "post_url": "https://www.instagram.com/p/Cabc/", "description": "Beach day", "num": 2, "extension": "jpg"}],
 [3, "https://cdn.test/r.mp4", {"post_shortcode": "Cdef", "post_url": "https://www.instagram.com/reel/Cdef/", "description": "A reel", "num": 1, "extension": "mp4", "video_url": "https://cdn.test/r.mp4", "display_url": "https://cdn.test/r.jpg"}],
 [3, "https://cdn.test/avatar.jpg", {"type": "avatar", "extension": "jpg"}]
]`

func TestParseGalleryDLInstagram(t *testing.T) {
	items, limited, err := parseGalleryDL([]byte(instagramGDL), "Instagram")
	if err != nil || limited {
		t.Fatalf("err %v limited %v", err, limited)
	}
	if len(items) != 3 {
		t.Fatalf("want 3 items, got %d: %+v", len(items), items)
	}
	a1, a2, r := items[0], items[1], items[2]
	if a1.Kind != "photo" || a1.Post != "ig:Cabc" || a1.Index != 1 || a1.Media != "https://cdn.test/a1.jpg" || a1.Thumb != "https://cdn.test/a1_s.jpg" || a1.Title != "Beach day" {
		t.Errorf("photo 1: %+v", a1)
	}
	if a2.Kind != "photo" || a2.Index != 2 || a2.Thumb != "https://cdn.test/a2.jpg" {
		t.Errorf("photo 2: %+v", a2)
	}
	if r.Kind != "video" || r.Post != "ig:Cdef" || r.Media != "https://cdn.test/r.mp4" || r.Page != "" {
		t.Errorf("reel: %+v", r)
	}
	// Both photos of the carousel stay; the same file twice is listed once.
	m := mergeMedia(items, items)
	if len(m) != 3 || m[0].ID != "m1" || m[2].ID != "m3" {
		t.Errorf("merge: %+v", m)
	}
}

func TestParseGalleryDLTikTok(t *testing.T) {
	out := `[
 [3, "ytdl:https://www.tiktok.com/@soloshinee/video/111", {"id": "111", "type": "video", "title": "Dance", "author": {"uniqueId": "soloshinee"}, "extension": "mp4", "cover": "https://p16.test/c.jpg"}],
 [3, "https://p16.test/photo1.jpg", {"id": "222", "type": "image", "num": 1, "title": "Slides", "author": {"uniqueId": "soloshinee"}, "extension": "jpg"}],
 [3, "https://p16.test/song.mp3", {"id": "222", "type": "audio", "extension": "mp3"}],
 [-1, {"error": "HttpError", "message": "403 Forbidden"}]
]`
	items, _, err := parseGalleryDL([]byte(out), "TikTok")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("want the 403 error kept, got %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want a video and a photo (no audio), got %+v", items)
	}
	v, p := items[0], items[1]
	if v.Kind != "video" || v.Page != "https://www.tiktok.com/@soloshinee/video/111" || v.Media != "" || v.Post != "tt:111" || v.Thumb != "https://p16.test/c.jpg" {
		t.Errorf("video: %+v", v)
	}
	if p.Kind != "photo" || p.Media != "https://p16.test/photo1.jpg" || p.Post != "tt:222" || p.Index != 1 {
		t.Errorf("photo: %+v", p)
	}
	if _, _, err := parseGalleryDL([]byte("not json"), "TikTok"); err == nil {
		t.Error("bad output should be an error")
	}
	if items, _, err := parseGalleryDL(nil, "TikTok"); err != nil || items != nil {
		t.Error("empty output: no items, no error")
	}
}

func TestParseYtdlpProfile(t *testing.T) {
	data := map[string]any{"_type": "playlist", "entries": []any{
		map[string]any{"url": "https://www.tiktok.com/@a/video/111", "title": "Dance", "duration": 15.0,
			"thumbnails": []any{map[string]any{"url": "https://p.test/s.jpg", "width": 100.0}, map[string]any{"url": "https://p.test/m.jpg", "width": 480.0}}},
		map[string]any{"url": "javascript:alert(1)"},
		map[string]any{"webpage_url": "https://www.redgifs.com/watch/abc", "thumbnail": "https://t.test/abc.jpg"},
	}}
	items := parseYtdlpProfile(data)
	if len(items) != 2 {
		t.Fatalf("got %+v", items)
	}
	if items[0].Post != "tt:111" || items[0].Thumb != "https://p.test/m.jpg" || items[0].Duration != 15 || items[0].Kind != "video" {
		t.Errorf("first: %+v", items[0])
	}
	if items[1].Post != "rg:abc" || items[1].Thumb != "https://t.test/abc.jpg" {
		t.Errorf("second: %+v", items[1])
	}
	// gallery-dl and yt-dlp both list TikTok video 111: listed once, with
	// the duration yt-dlp knows.
	g, _, _ := parseGalleryDL([]byte(`[[3, "ytdl:https://www.tiktok.com/@a/video/111", {"id": "111", "type": "video"}]]`), "TikTok")
	m := mergeMedia(g, items)
	if len(m) != 2 || m[0].Duration != 15 {
		t.Errorf("merge: %+v", m)
	}
}

func TestCleanPageItemsAndMerge(t *testing.T) {
	in := []MediaItem{
		{Kind: "photo", Media: "https://cdn.test/p1.jpg?x=1", Page: "https://www.instagram.com/p/Cabc/", Thumb: "https://cdn.test/t.jpg", Title: "  Hi  "},
		{Kind: "photo", Media: "javascript:alert(1)"},
		{Kind: "video", Media: "https://cdn.test/live/master.m3u8"},
		{Kind: "post", Page: "https://www.instagram.com/p/Cabc/"},
		{Kind: "post", Page: "https://www.instagram.com/p/Czzz/"},
		{Kind: "post"},
		{Kind: "script", Media: "https://cdn.test/x.js"},
		{Kind: "video", Thumb: "javascript:alert(1)", Media: "https://cdn.test/v.mp4"},
	}
	out := cleanPageItems(in, "https://www.instagram.com/someone/")
	if len(out) != 5 {
		t.Fatalf("want 5 items, got %d: %+v", len(out), out)
	}
	if out[0].Title != "Hi" || out[0].Post != "ig:Cabc" || out[0].Ext != "jpg" {
		t.Errorf("photo: %+v", out[0])
	}
	if out[1].Stream != "hls" {
		t.Errorf("stream: %+v", out[1])
	}
	if out[4].Thumb != "" {
		t.Errorf("a javascript: thumbnail must be dropped: %+v", out[4])
	}
	m := mergeMedia(out, nil)
	// The bare link to post Cabc is covered by its photo; Czzz stays.
	posts := 0
	for _, it := range m {
		if it.Kind == "post" {
			posts++
			if it.Post != "ig:Czzz" {
				t.Errorf("post kept: %+v", it)
			}
		}
	}
	if posts != 1 || len(m) != 4 {
		t.Errorf("merge: %+v", m)
	}
}

func TestFriendlyProfileError(t *testing.T) {
	msg := friendlyProfileError("TikTok", "ERROR: [TikTok] soloshinee: HTTP Error 403: Forbidden", false)
	if !strings.Contains(msg, "403") || !strings.Contains(msg, "Download all media on this page with BDDM") {
		t.Errorf("403: %q", msg)
	}
	if msg := friendlyProfileError("Instagram", "AuthRequired: login required", false); !strings.Contains(msg, "signed in") {
		t.Errorf("login: %q", msg)
	}
	if msg := friendlyProfileError("Fansly", "anything", true); msg != fanslyHelp {
		t.Errorf("fansly: %q", msg)
	}
	if msg := friendlyProfileError("RedGIFs", "NotFoundError: user not found", false); !strings.Contains(msg, "not found") {
		t.Errorf("404: %q", msg)
	}
	for _, s := range []string{fanslyHelp, extensionHelp} {
		if strings.ContainsAny(s, "\u0980\u09FF") {
			t.Error("messages must be English")
		}
	}
}

func TestProfileJobKinds(t *testing.T) {
	p := &profileList{req: AddRequest{URL: "https://www.tiktok.com/@a", Cookie: "sid=1"}}
	names := map[string]bool{}
	j := profileJob(MediaItem{Kind: "video", Page: "https://www.tiktok.com/@a/video/111", Media: "https://v.test/x.mp4", Title: "Dance", Post: "tt:111"}, p, "/d", "720", 1, names, 4)
	if j.Kind != "video" || j.URL != "https://www.tiktok.com/@a/video/111" || j.Video == nil || j.Video.Quality != "720" || j.Cookie != "sid=1" {
		t.Errorf("tiktok video: %+v", j)
	}
	j = profileJob(MediaItem{Kind: "photo", Media: "https://p.test/1.jpg?s=1", Ext: "jpg", Title: "Slides", Post: "tt:222", Index: 2}, p, "/d", "best", 2, names, 4)
	if j.Kind != "" || j.Want != "Slides [222_2].jpg" || j.Referer != "https://www.tiktok.com/@a" {
		t.Errorf("photo: %+v", j)
	}
	j = profileJob(MediaItem{Kind: "photo", Media: "https://p.test/2.jpg", Ext: "jpg", Title: "Slides", Post: "tt:222", Index: 2}, p, "/d", "best", 3, names, 4)
	if j.Want != "Slides [222_2] (2).jpg" {
		t.Errorf("same name twice: %q", j.Want)
	}
	j = profileJob(MediaItem{Kind: "video", Media: "https://cdn.test/master.m3u8", Stream: "hls"}, p, "/d", "best", 4, names, 4)
	if j.Kind != "video" || j.Stream != "hls" || j.Video == nil {
		t.Errorf("stream: %+v", j)
	}
	if j := profileJob(MediaItem{Kind: "photo"}, p, "/d", "best", 5, names, 4); j != nil {
		t.Errorf("nothing to download: %+v", j)
	}
}

// writeFakeTool puts a shell script named like a tool in cfg/tools.
func writeFakeTool(t *testing.T, m *Manager, name, script string) {
	t.Helper()
	dir := filepath.Join(m.configDir, "tools")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0755); err != nil {
		t.Fatal(err)
	}
}

func waitProfile(t *testing.T, m *Manager, id string) ProfileView {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		v, err := m.Profile(id)
		if err != nil {
			t.Fatal(err)
		}
		if v.State != "loading" {
			return v
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the profile was still loading")
	return ProfileView{}
}

// An Instagram profile listed by a fake gallery-dl: two carousel photos are
// downloaded into "<dir>/natgeo (Instagram)" with the browser's cookies.
func TestProfileListAndDownload(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as the fake gallery-dl")
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	files := map[string][]byte{"/a1.jpg": bytes.Repeat([]byte{1}, 3000), "/a2.jpg": bytes.Repeat([]byte{2}, 4000)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(b)
	}))
	defer srv.Close()
	m, dl := newTestManager(t, 1)
	argsFile := filepath.Join(t.TempDir(), "args")
	out := fmt.Sprintf(`[[3, "%[1]s/a1.jpg", {"post_shortcode": "Cabc", "num": 1, "extension": "jpg", "description": "Beach"}],`+
		`[3, "%[1]s/a2.jpg", {"post_shortcode": "Cabc", "num": 2, "extension": "jpg", "description": "Beach"}]]`, srv.URL)
	writeFakeTool(t, m, "gallery-dl", fmt.Sprintf("for a in \"$@\"; do echo \"$a\" >> %q; done\ncat <<'EOF'\n%s\nEOF\n", argsFile, out))

	id, err := m.AddProfile(AddRequest{URL: "https://www.instagram.com/natgeo/", Cookie: "sessionid=abc"}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	v := waitProfile(t, m, id)
	if v.State != "ready" || len(v.Items) != 2 || v.Photos != 2 || v.Videos != 0 || v.Site != "Instagram" || v.Folder != "natgeo (Instagram)" {
		t.Fatalf("view: %+v", v)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--cookies\n") || !strings.Contains(string(args), "https://www.instagram.com/natgeo/") {
		t.Errorf("gallery-dl should get the browser's cookies and the link: %s", args)
	}
	dir := filepath.Join(dl, "pick")
	res, err := m.StartProfile(id, []string{v.Items[0].ID, v.Items[1].ID}, dir, "", "best", false, false)
	if err != nil || res.Added != 2 || res.Folder != filepath.Join(dir, "natgeo (Instagram)") {
		t.Fatalf("start: %+v %v", res, err)
	}
	for _, name := range []string{"Beach [Cabc_1].jpg", "Beach [Cabc_2].jpg"} {
		p := filepath.Join(res.Folder, name)
		deadline := time.Now().Add(20 * time.Second)
		for {
			if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s was not downloaded; jobs: %+v", name, m.History(""))
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	if _, err := m.Profile(id); err == nil {
		t.Error("the list should be gone after Download")
	}

	// The same profile again: both photos were downloaded before.
	id, _ = m.AddProfile(AddRequest{URL: "https://www.instagram.com/natgeo/"}, nil, 0)
	v = waitProfile(t, m, id)
	deadline := time.Now().Add(10 * time.Second)
	for {
		res, err = m.StartProfile(id, []string{v.Items[0].ID, v.Items[1].ID}, dir, "", "best", false, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Skipped) == 2 || time.Now().After(deadline) {
			break
		}
		id, _ = m.AddProfile(AddRequest{URL: "https://www.instagram.com/natgeo/"}, nil, 0)
		v = waitProfile(t, m, id)
		time.Sleep(100 * time.Millisecond)
	}
	if res.Added != 0 || len(res.Skipped) != 2 {
		t.Errorf("downloaded before: %+v", res)
	}
}

// Fansly is listed from the open page only (the tools are never run), DRM
// players are counted, and with nothing on the page the help is shown.
func TestProfileFanslyFromPage(t *testing.T) {
	m, _ := newTestManager(t, 1)
	items := []MediaItem{
		{Kind: "photo", Media: "https://cdn3.fansly.com/a.jpg", Title: "One"},
		{Kind: "video", Media: "https://cdn3.fansly.com/b.mp4", Thumb: "https://cdn3.fansly.com/b.jpg"},
	}
	id, err := m.AddProfile(AddRequest{URL: "https://fansly.com/somecreator/posts"}, items, 1)
	if err != nil {
		t.Fatal(err)
	}
	v := waitProfile(t, m, id)
	if v.State != "ready" || len(v.Items) != 2 || v.Photos != 1 || v.Videos != 1 {
		t.Fatalf("view: %+v", v)
	}
	if !strings.Contains(v.Note, "Scroll further") || !strings.Contains(v.Note, "protected (DRM)") {
		t.Errorf("note: %q", v.Note)
	}
	id, _ = m.AddProfile(AddRequest{URL: "https://fansly.com/somecreator"}, nil, 0)
	v = waitProfile(t, m, id)
	if v.State != "error" || v.Error != fanslyHelp {
		t.Errorf("no page items: %+v", v)
	}
	if _, err := m.StartProfile(id, []string{"m1"}, t.TempDir(), "", "best", false, false); err == nil {
		t.Error("a failed list can't be downloaded")
	}
}

// A pasted TikTok profile link opens the profile window (it used to go to
// the video window, which failed with HTTP 403); the extension's page list
// goes to the same window.
func TestPastedProfileOpensProfileWindow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses shell scripts as fake tools")
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	m, _ := newTestManager(t, 1)
	writeFakeTool(t, m, "gallery-dl", "echo '[[3, \"ytdl:https://www.tiktok.com/@soloshinee/video/111\", {\"id\": \"111\", \"type\": \"video\", \"title\": \"Dance\"}]]'\n")
	writeFakeTool(t, m, "yt-dlp", "echo 'ERROR: [TikTok] soloshinee: HTTP Error 403: Forbidden' >&2\nexit 1\n")
	srv := httptest.NewServer(newServer(m, "0", func() {}, nil))
	defer srv.Close()

	post := func(path string, body any) map[string]any {
		b, _ := json.Marshal(body)
		r, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(r.Body).Decode(&out)
		if r.StatusCode != 200 {
			t.Fatalf("%s: HTTP %d %v", path, r.StatusCode, out)
		}
		return out
	}
	out := post("/api/add", map[string]any{"url": "https://www.tiktok.com/@soloshinee"})
	win, _ := out["window"].(string)
	if out["pending"] != true || !strings.Contains(win, "/profile.html?id=") {
		t.Fatalf("paste: %v", out)
	}
	id := win[strings.Index(win, "id=")+3:]
	v := waitProfile(t, m, id)
	if v.State != "ready" || len(v.Items) != 1 || v.Items[0].Post != "tt:111" || v.Name != "@soloshinee" {
		t.Fatalf("view: %+v", v)
	}
	r, err := http.Get(srv.URL + "/api/profile/" + id)
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("GET: %v %v", r, err)
	}
	r.Body.Close()
	post("/api/profile/"+id+"/cancel", map[string]any{})
	if _, err := m.Profile(id); err == nil {
		t.Error("cancel should drop the list")
	}

	// The extension sends a generic page with what it found on it.
	out = post("/ext/profile", map[string]any{"url": "https://example.com/gallery/summer", "items": []map[string]any{
		{"kind": "photo", "media": "https://example.com/i/1.jpg", "title": "Sea"},
		{"kind": "video", "media": "https://example.com/v/1.mp4"},
	}})
	win, _ = out["window"].(string)
	v = waitProfile(t, m, win[strings.Index(win, "id=")+3:])
	if v.State != "ready" || v.Photos != 1 || v.Videos != 1 || v.Folder != "summer" {
		t.Errorf("page: %+v", v)
	}
	// A single TikTok video still goes to the video window.
	if isProfileRequest(AddRequest{URL: "https://www.tiktok.com/@soloshinee/video/7312345678901234567"}) {
		t.Error("a single video is not a profile")
	}
	if isProfileRequest(AddRequest{URL: "https://www.tiktok.com/@soloshinee", Kind: "media"}) {
		t.Error("a media file is never a profile")
	}
}

func TestProfilePageIsCompactAndEnglish(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("ui", "profile.html"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`data-fit-min="560"`, `data-fit-max="680"`, `src="fit.js"`, "bddmFitWindow()", `referrerPolicy = "no-referrer"`,
		"Select all", "Download Later", "Also download items I downloaded before", "theme.css"} {
		if !strings.Contains(s, want) {
			t.Errorf("profile.html: missing %q", want)
		}
	}
	for _, r := range s {
		if r >= 0x0980 && r <= 0x09FF {
			t.Fatal("profile.html must be English only")
		}
	}
	if !isPopupAppURL("http://127.0.0.1:9614/profile.html?id=x") || !isPopupWindowTitle("Download profile media") {
		t.Error("profile.html must be a popup window")
	}
	if windowTitleForURL("http://127.0.0.1:9614/profile.html?id=x") != "Download profile media" {
		t.Error("window title")
	}
}

func TestCollectScript(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command("node", filepath.Join("extension", "collect_test.js")).CombinedOutput()
	if err != nil {
		t.Fatalf("collect_test.js: %v\n%s", err, out)
	}
	for _, f := range []string{"base.json", "chromium.json"} {
		b, _ := os.ReadFile(filepath.Join("extension", "manifests", f))
		if !strings.Contains(string(b), `"collect.js"`) {
			t.Errorf("%s: collect.js is not a content script", f)
		}
	}
	b, _ := os.ReadFile(filepath.Join("extension", "build.py"))
	if !strings.Contains(string(b), `"collect.js"`) {
		t.Error("build.py does not pack collect.js")
	}
	b, _ = os.ReadFile(filepath.Join("extension", "videos.js"))
	if !strings.Contains(string(b), "Download all media on this page with BDDM") || !strings.Contains(string(b), "/ext/profile") {
		t.Error("videos.js: the page media menu is missing")
	}
}
