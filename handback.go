package main

// handback.go: browser fallback (item 18).
//
// Some sites do not let BDDM download a file that the browser can download:
// the file needs the browser's sign-in, a security or anti-bot check, or the
// site answers 401/403/407 or sends a web page instead of the file. BDDM must
// not leave a broken download then. The browser extension notices the failed
// job, gives the link back to the browser (which downloads it itself, with
// its own cookies) and tells BDDM with POST /ext/jobs/{id}/handedback. The
// row stays in the list with a clear message instead of an error.

import (
	"errors"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	msgHandedBack      = "Site blocked BDDM - handed back to browser"
	msgHandedBackOther = "BDDM could not finish this download - handed back to browser"
	fixHandedBack      = "Your browser is downloading this file itself: look in your browser's Downloads list. " +
		"You can remove this row from BDDM."
	msgSitePage = "The site sent a web page instead of the file (it probably wants a sign-in or a security check)."
)

var errSitePage = errors.New(msgSitePage)

// localProblemWords: errors that are about this computer, not about the site.
// The browser could not fix them either, so the download is not handed back.
var localProblemWords = []string{
	"checksum mismatch", "not enough free space", "no space left", "disk full", "there is not enough space",
	"access is denied", "permission denied", "operation not permitted", "read-only file system",
	"file name too long", "filename too long", "name too long", "the filename, directory name",
	"being used by another process", "could not rename file", "cannot create folder",
}

// siteBlockWords: the site itself said no to BDDM (besides HTTP 401/403/407/451).
var siteBlockWords = []string{
	"web page instead of the file", "wants you to sign in", "sign-in", "captcha",
	"security check", "anti-bot", "verify you are human", "just a moment",
}

// handBackKind decides what to do with a failed download that came from the
// browser extension: hand is true when the browser should try it, blocked is
// true when the site refused BDDM (not just a bad connection).
func handBackKind(errMsg string) (hand, blocked bool) {
	low := strings.ToLower(strings.TrimSpace(errMsg))
	if low == "" {
		return true, false
	}
	if errMsg == msgDriveViewOnly || strings.Contains(low, "view only") || strings.Contains(low, "link has expired") {
		// Nothing the browser can do (view only), or the old link is dead for
		// the browser too: BDDM continues when the user clicks it again.
		return false, false
	}
	for _, w := range localProblemWords {
		if strings.Contains(low, w) {
			return false, false
		}
	}
	switch httpCode(low) {
	case "401", "403", "407", "451":
		return true, true
	}
	for _, w := range siteBlockWords {
		if strings.Contains(low, w) {
			return true, true
		}
	}
	return true, false
}

// htmlExts: names where a web page is what the user asked for.
var htmlExts = map[string]bool{
	"": true, ".htm": true, ".html": true, ".xhtml": true, ".shtml": true, ".php": true, ".asp": true,
	".aspx": true, ".jsp": true, ".cgi": true, ".pl": true, ".do": true, ".action": true,
}

// htmlInsteadOfFile: the server answered with a web page, but the download is
// a file (a name from the browser, or a link that ends in a file extension).
// That is a login page, a captcha or a "just a moment" check page: writing it
// into the file would leave a broken download.
func htmlInsteadOfFile(contentType string, attachment bool, want, rawURL, finalURL string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if mt, _, err := mime.ParseMediaType(ct); err == nil {
		ct = mt
	}
	if ct != "text/html" && ct != "application/xhtml+xml" {
		return false
	}
	if attachment {
		return false // the site really sends an .html file
	}
	if want != "" {
		return !htmlExts[strings.ToLower(filepath.Ext(want))]
	}
	for _, raw := range []string{finalURL, rawURL} {
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		ext := strings.ToLower(path.Ext(u.Path))
		if ext != "" && len(ext) <= 6 && !htmlExts[ext] {
			return true
		}
	}
	return false
}

// MarkHandedBack: the extension gave a failed download to the browser. The
// row stays (with a plain message, see viewLocked) and the unfinished .part
// file is deleted. Finished or running downloads are never touched.
func (m *Manager) MarkHandedBack(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.findLocked(id)
	if j == nil {
		return errors.New("download not found")
	}
	if j.Status != StatusError || j.HandedBack {
		return nil
	}
	j.HandedBack = true
	if j.Kind == "" && j.Name != "" && j.Dir != "" {
		full := filepath.Join(j.Dir, j.Name)
		_ = os.Remove(full + ".part")
		_ = os.Remove(full + ".bddm.json")
	}
	m.saveHistoryLocked()
	return nil
}
