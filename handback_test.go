package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandBackHelpersJS(t *testing.T) {
	out, err := exec.Command("node", filepath.Join("extension", "handback_test.js")).CombinedOutput()
	if err != nil {
		t.Fatalf("handback_test.js: %v\n%s", err, out)
	}
}

func TestHandBackKind(t *testing.T) {
	for _, c := range []struct {
		msg           string
		hand, blocked bool
	}{
		{"The server refused the download (HTTP 403 Forbidden).", true, true},
		{"The server refused the download (HTTP 401 Unauthorized).", true, true},
		{"The server refused the download (HTTP 407 Proxy Authentication Required).", true, true},
		{"could not connect: server returned HTTP 407", true, true},
		{msgSitePage, true, true},
		{"Google Drive sent a web page instead of the file data", true, true},
		{"Please complete the captcha first", true, true},
		{"download stalled", true, false},
		{"could not connect: server returned HTTP 502", true, false},
		{"", true, false},
		{"There is not enough space on the disk.", false, false},
		{"open x: Access is denied.", false, false},
		{"checksum mismatch", false, false},
		{msgLinkExpired, false, false},
		{msgDriveViewOnly, false, false},
	} {
		hand, blocked := handBackKind(c.msg)
		if hand != c.hand || blocked != c.blocked {
			t.Errorf("%q: hand=%v blocked=%v, want %v %v", c.msg, hand, blocked, c.hand, c.blocked)
		}
	}
}

func TestHTMLInsteadOfFile(t *testing.T) {
	for _, c := range []struct {
		ct, want, raw string
		attach, res   bool
	}{
		{"text/html; charset=utf-8", "tool.zip", "https://x.test/get?id=1", false, true},
		{"text/html", "", "https://x.test/files/tool.exe", false, true},
		{"application/xhtml+xml", "movie.mkv", "https://x.test/d", false, true},
		{"text/html", "page.html", "https://x.test/page", false, false}, // an HTML file was asked for
		{"text/html", "", "https://x.test/page", false, false},          // nothing says it is a file
		{"text/html", "", "https://x.test/index.php", false, false},     // a dynamic page
		{"text/html", "tool.zip", "https://x.test/get", true, false},    // the site really names a file
		{"application/zip", "tool.zip", "https://x.test/tool.zip", false, false},
		{"", "tool.zip", "https://x.test/tool.zip", false, false},
	} {
		if got := htmlInsteadOfFile(c.ct, c.attach, c.want, c.raw, ""); got != c.res {
			t.Errorf("%q want=%q url=%s attach=%v: got %v want %v", c.ct, c.want, c.raw, c.attach, got, c.res)
		}
	}
}

// A site that blocks BDDM: the job fails with a plain reason and leaves no
// broken file; after the extension hands it to the browser the row stays with
// "handed back to browser" and the .part file is gone. A normal file is not
// affected.
func TestBlockedSiteHandBackEndToEnd(t *testing.T) {
	good := make([]byte, 64*1024)
	for i := range good {
		good[i] = byte(i * 7)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/login.zip", func(w http.ResponseWriter, r *http.Request) { // sign-in page instead of the file
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><body>Please sign in</body></html>")
	})
	mux.HandleFunc("/forbidden.zip", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusForbidden) })
	mux.HandleFunc("/proxy.zip", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusProxyAuthRequired) })
	mux.HandleFunc("/page.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html>"+strings.Repeat("hello ", 500)+"</html>")
	})
	mux.HandleFunc("/ok.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Write(good)
	})
	site := httptest.NewServer(mux)
	defer site.Close()

	m, dl := newTestManager(t, 1)
	base := startUITestServer(t, m)
	add := func(path, name string) string {
		t.Helper()
		v, err := m.Add(AddRequest{URL: site.URL + path, Filename: name})
		if err != nil {
			t.Fatal(err)
		}
		return v.ID
	}
	ok := add("/ok.zip", "ok.zip")
	htmlFile := add("/page.html", "page.html")
	login := add("/login.zip", "tool.zip")
	forbidden := add("/forbidden.zip", "secret.zip")
	proxy := add("/proxy.zip", "proxied.zip")

	if v := waitDone(t, m, ok, 20*time.Second); v.Status != StatusDone {
		t.Fatalf("a normal file must still download: %s %s", v.Status, v.Error)
	}
	if v := waitDone(t, m, htmlFile, 20*time.Second); v.Status != StatusDone {
		t.Fatalf("an HTML file that was asked for must still download: %s %s", v.Status, v.Error)
	}
	for id, name := range map[string]string{login: "tool.zip", forbidden: "secret.zip", proxy: "proxied.zip"} {
		v := waitDone(t, m, id, 20*time.Second)
		if v.Status != StatusError {
			t.Fatalf("%s: status %s, want Error", name, v.Status)
		}
		if _, err := os.Stat(filepath.Join(dl, name)); err == nil {
			t.Errorf("%s: a broken file was left behind", name)
		}
		hand, blocked := handBackKind(v.Error)
		if !hand || !blocked {
			t.Errorf("%s: error %q should be handed back as blocked", name, v.Error)
		}
	}
	if v := jobView(m, login); !strings.Contains(v.Error, "web page instead of the file") {
		t.Errorf("sign-in page: error %q", v.Error)
	}

	// What the extension asks BDDM.
	res, err := http.Get(base + "/ext/status?ids=" + login + "," + ok)
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Jobs []struct {
			ID         string `json:"id"`
			Status     string `json:"status"`
			HandBack   bool   `json:"handBack"`
			Blocked    bool   `json:"blocked"`
			HandedBack bool   `json:"handedBack"`
		} `json:"jobs"`
	}
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	for _, j := range st.Jobs {
		switch j.ID {
		case login:
			if j.Status != "Error" || !j.HandBack || !j.Blocked || j.HandedBack {
				t.Errorf("blocked job status: %+v", j)
			}
		case ok:
			if j.Status != "Done" {
				t.Errorf("ok job status: %+v", j)
			}
		}
	}

	// Leave a part file, like an interrupted download, then hand back.
	m.mu.Lock()
	m.findLocked(login).Name = "tool.zip" // a download that had started and was named
	m.mu.Unlock()
	part := filepath.Join(dl, "tool.zip.part")
	if err := os.MkdirAll(dl, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(part, []byte("half"), 0644)
	r, err := http.Post(base+"/ext/jobs/"+login+"/handedback", "application/json", nil)
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("handedback: %v %v", err, r)
	}
	r.Body.Close()
	v := jobView(m, login)
	if !v.HandedBack || v.Removed || v.Status != StatusError {
		t.Fatalf("after hand-back the row stays: %+v", v)
	}
	if v.Reason != msgHandedBack || v.Fix != fixHandedBack {
		t.Errorf("row message: %q / %q", v.Reason, v.Fix)
	}
	if _, err := os.Stat(part); err == nil {
		t.Error("the unfinished .part file should be deleted")
	}
	// A second call changes nothing; a finished download is never touched.
	if err := m.MarkHandedBack(ok); err != nil {
		t.Fatal(err)
	}
	if jobView(m, ok).HandedBack {
		t.Error("a finished download must not be marked handed back")
	}
	if _, err := os.Stat(filepath.Join(dl, "ok.zip")); err != nil {
		t.Error("the finished file must stay")
	}
	// A generic failure gets the other plain message.
	if err := m.MarkHandedBack("nope"); err == nil {
		t.Error("an unknown id should be an error")
	}
	// Retry (Resume) clears the handed-back state so the row is normal again.
	if err := m.Resume(login); err != nil {
		t.Fatal(err)
	}
	if jobView(m, login).HandedBack {
		t.Error("Retry should clear the handed-back state")
	}
}

func TestHandedBackRowMessageAndUI(t *testing.T) {
	j := &Job{ID: "a", Status: StatusError, Error: "download stalled", HandedBack: true}
	v := j.viewLocked()
	if !v.HandedBack || v.Reason != msgHandedBackOther || v.Fix != fixHandedBack {
		t.Errorf("generic hand-back view: %+v", v)
	}
	j.Error = "The server refused the download (HTTP 403 Forbidden)."
	if v = j.viewLocked(); v.Reason != msgHandedBack {
		t.Errorf("blocked hand-back reason %q", v.Reason)
	}
	if msgHandedBack != "Site blocked BDDM - handed back to browser" {
		t.Errorf("the row message changed: %q", msgHandedBack)
	}
	for _, s := range []string{msgHandedBack, msgHandedBackOther, fixHandedBack, msgSitePage} {
		assertEnglishUI(t, "handback message", s)
	}
	idx := readUI(t, "index.html")
	for _, want := range []string{"Handed to browser", "j.handedBack", "badge.HandedBack"} {
		if !strings.Contains(idx, want) {
			t.Errorf("index.html missing %q", want)
		}
	}
}

// handback.js must be in both builds (the Chromium service worker imports it,
// Firefox lists it in its manifest) and in the build file list.
func TestHandBackScriptShipped(t *testing.T) {
	for _, f := range []string{"build.py"} {
		b, _ := os.ReadFile(filepath.Join("extension", f))
		if !strings.Contains(string(b), `"handback.js"`) {
			t.Errorf("%s does not ship handback.js", f)
		}
	}
	b, _ := os.ReadFile(filepath.Join("extension", "manifests", "firefox.json"))
	if !strings.Contains(string(b), `"handback.js"`) {
		t.Error("firefox.json does not load handback.js")
	}
	bg, _ := os.ReadFile(filepath.Join("extension", "background.js"))
	s := string(bg)
	for _, want := range []string{"decideHandBack(", "handBackNotice(", "quickPolls()", "/handedback"} {
		if !strings.Contains(s, want) {
			t.Errorf("background.js missing %q", want)
		}
	}
	// The hand-back must mark the link before the browser starts it, so the
	// browser's own download is not captured again (a loop).
	if i, j := strings.Index(s, "await markHandedBack([info.url, info.origUrl]);"), strings.Index(s, "api.downloads.download({ url, conflictAction"); i < 0 || j < 0 || i > j {
		t.Error("handBack must mark the link before it starts the browser download")
	}
}
