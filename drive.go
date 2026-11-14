package main

// drive.go: Google Drive links.
//
//   - File links (drive.google.com/file/d/ID, open?id=, uc?id=,
//     drive.usercontent.google.com/download?id=...) are turned into the real
//     download link, also through the "can't scan this file for viruses"
//     page, and downloaded with the normal engine. The link is worked out
//     again on every start, so Resume works after a restart.
//   - Google Docs/Sheets/Slides are downloaded through their export links in
//     the format chosen in the "Download File Info" window.
//   - Folders are listed without an API key (the folder page and its
//     embedded view), checked so that only items whose parent is that folder
//     are taken, and shown in a window to choose from (drive.go + ui/drive.html).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	msgDrivePrivate  = "This file is private. Open it in your browser while signed in and send it from the BDDM extension."
	msgDriveMissing  = "Google Drive says this file does not exist or is private. Check the link, or open it in your browser while signed in and send it from the BDDM extension."
	msgDriveViewOnly = "The owner has turned off downloading for this file (view only). It can't be downloaded."
	msgDriveQuota    = "Google Drive says too many people viewed or downloaded this file recently (download limit reached). Try again later (often after 24 hours), or ask the owner to share a copy."
)

type driveLink struct {
	Kind        string // "file", "doc", "folder"
	ID          string
	ResourceKey string
	DocType     string // "document", "spreadsheets", "presentation" (Kind "doc")
	// AuthUser is the signed-in Google account the link was opened with
	// (/u/1/ in the path or ?authuser=1). With several accounts signed in,
	// Google uses account 0 unless told otherwise, so it has to be kept.
	AuthUser string
}

var (
	driveIDRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{10,}$`)
	driveFileRe = regexp.MustCompile(`^/file/(?:u/\d+/)?d/([A-Za-z0-9_-]{10,})`)
	driveFoldRe = regexp.MustCompile(`^/drive/(?:u/\d+/)?(?:mobile/)?folders/([A-Za-z0-9_-]{10,})`)
	driveDocRe  = regexp.MustCompile(`^/(document|spreadsheets|presentation)/(?:u/\d+/)?d/([A-Za-z0-9_-]{10,})(/[^?#]*)?`)
	driveUserRe = regexp.MustCompile(`/u/(\d+)(?:/|$)`)
	docsHomeRe  = regexp.MustCompile(`^/(?:(?:document|spreadsheets|presentation|forms|drawings)(?:/u/\d+)?)?/?$`)
)

// errDrivePage: a Google Drive web page (My Drive, Storage/quota, Recent,
// Shared with me, ...) is not a file.
var errDrivePage = errors.New("This is a Google Drive page, not a file, so there is nothing to download. In Drive, open the file (or folder) first, or right-click the file in the list and choose \"Download with BDDM\".")

// driveNonFilePage reports links to Google Drive / Docs pages that are not a
// file, folder or document (drive.google.com/drive/u/1/quota, /drive/my-drive,
// /drive/home, docs.google.com/document/u/0/ ...). They must not be
// downloaded: the result would be a useless web page.
func driveNonFilePage(raw string) bool {
	if _, ok := parseDriveLink(raw); ok {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "drive.google.com":
		return true
	case "docs.google.com":
		return docsHomeRe.MatchString(u.Path)
	}
	return false
}

// authQuery returns "&authuser=N" (or "?authuser=N" with first=true), or "".
func (l driveLink) authQuery(first bool) string {
	if l.AuthUser == "" {
		return ""
	}
	sep := "&"
	if first {
		sep = "?"
	}
	return sep + "authuser=" + url.QueryEscape(l.AuthUser)
}

// parseDriveLink recognises Google Drive / Docs links.
func parseDriveLink(raw string) (driveLink, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return driveLink{}, false
	}
	host := strings.ToLower(u.Hostname())
	q := u.Query()
	l := driveLink{ResourceKey: q.Get("resourcekey"), AuthUser: strings.TrimSpace(q.Get("authuser"))}
	if l.AuthUser == "" {
		if m := driveUserRe.FindStringSubmatch(u.Path); m != nil {
			l.AuthUser = m[1]
		}
	}
	qid := func() bool {
		l.ID = q.Get("id")
		return driveIDRe.MatchString(l.ID)
	}
	p := u.Path
	switch host {
	case "drive.google.com":
		if m := driveFileRe.FindStringSubmatch(p); m != nil {
			l.Kind, l.ID = "file", m[1]
			return l, true
		}
		if m := driveFoldRe.FindStringSubmatch(p); m != nil {
			l.Kind, l.ID = "folder", m[1]
			return l, true
		}
		switch strings.TrimSuffix(regexp.MustCompile(`^/u/\d+`).ReplaceAllString(p, ""), "/") {
		case "/open", "/uc":
			if qid() {
				l.Kind = "file"
				return l, true
			}
		case "/folderview", "/embeddedfolderview":
			if qid() {
				l.Kind = "folder"
				return l, true
			}
		}
	case "drive.usercontent.google.com":
		if (strings.HasSuffix(p, "/download") || strings.HasSuffix(p, "/uc")) && qid() {
			l.Kind = "file"
			return l, true
		}
	case "docs.google.com":
		if m := driveDocRe.FindStringSubmatch(p); m != nil {
			if strings.HasPrefix(m[3], "/export") {
				return driveLink{}, false // already an export link: a normal file
			}
			l.Kind, l.DocType, l.ID = "doc", m[1], m[2]
			return l, true
		}
		if m := driveFileRe.FindStringSubmatch(p); m != nil {
			l.Kind, l.ID = "file", m[1]
			return l, true
		}
		if strings.TrimSuffix(regexp.MustCompile(`^/u/\d+`).ReplaceAllString(p, ""), "/") == "/uc" && qid() {
			l.Kind = "file"
			return l, true
		}
	}
	return driveLink{}, false
}

// driveKey is the same for every link form of one Drive file (for the
// "already downloaded" check).
func driveKey(raw string) string {
	l, ok := parseDriveLink(raw)
	if !ok || l.Kind == "folder" {
		return ""
	}
	return l.Kind + ":" + l.ID
}

// ---------------------------------------------------------------------------
// Google Docs / Sheets / Slides
// ---------------------------------------------------------------------------

// docFormats lists the export formats; the first one is the default.
func docFormats(docType string) []string {
	switch docType {
	case "document":
		return []string{"pdf", "docx", "txt"}
	case "spreadsheets":
		return []string{"xlsx", "pdf", "csv"}
	case "presentation":
		return []string{"pptx", "pdf"}
	}
	return nil
}

func validDocFormat(docType, f string) bool {
	for _, x := range docFormats(docType) {
		if x == f {
			return true
		}
	}
	return false
}

func docExportURL(l driveLink, format string) string {
	if !validDocFormat(l.DocType, format) {
		format = docFormats(l.DocType)[0]
	}
	u := "https://docs.google.com/" + l.DocType + "/d/" + l.ID + "/export?format=" + format
	if l.ResourceKey != "" {
		u += "&resourcekey=" + url.QueryEscape(l.ResourceKey)
	}
	return u + l.authQuery(false)
}

// withExt sets the file extension of name to ext.
func withExt(name, ext string) string {
	if name == "" {
		return ""
	}
	if e := path.Ext(name); e != "" && len(e) <= 6 && !strings.Contains(e, " ") {
		name = strings.TrimSuffix(name, e)
	}
	return name + "." + ext
}

var titleRe = regexp.MustCompile(`(?is)<title>(.*?)</title>`)

// docTitle reads the document's name from its page.
func docTitle(ctx context.Context, client *http.Client, l driveLink, h reqHeaders) (string, error) {
	u := "https://docs.google.com/" + l.DocType + "/d/" + l.ID + "/edit"
	if l.ResourceKey != "" {
		u += "?resourcekey=" + url.QueryEscape(l.ResourceKey) + l.authQuery(false)
	} else {
		u += l.authQuery(true)
	}
	req, err := newRequest(ctx, http.MethodGet, u, h)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if isGoogleLogin(resp) {
		return "", errors.New(msgDrivePrivate)
	}
	if resp.StatusCode == 404 || resp.StatusCode == 401 || resp.StatusCode == 403 {
		return "", errors.New(msgDriveMissing)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	m := titleRe.FindSubmatch(body)
	if m == nil {
		return "", nil
	}
	t := strings.TrimSpace(html.UnescapeString(string(m[1])))
	for _, suf := range []string{" - Google Docs", " - Google Sheets", " - Google Slides", " - Google Drive"} {
		t = strings.TrimSuffix(t, suf)
	}
	return t, nil
}

// ---------------------------------------------------------------------------
// Files: the real download link
// ---------------------------------------------------------------------------

// isDriveFinal: an answer from Drive that retrying will not change.
func isDriveFinal(err error) bool {
	switch err.Error() {
	case msgDrivePrivate, msgDriveMissing, msgDriveQuota, msgDriveViewOnly:
		return true
	}
	return false
}

func driveDownloadURL(l driveLink) string {
	u := "https://drive.usercontent.google.com/download?id=" + url.QueryEscape(l.ID) + "&export=download"
	if l.ResourceKey != "" {
		u += "&resourcekey=" + url.QueryEscape(l.ResourceKey)
	}
	return u + l.authQuery(false)
}

func isGoogleLogin(resp *http.Response) bool {
	h := strings.ToLower(resp.Request.URL.Hostname())
	return h == "accounts.google.com" || strings.Contains(resp.Request.URL.Path, "ServiceLogin")
}

var (
	formRe   = regexp.MustCompile(`(?is)<form\b([^>]*)>(.*?)</form>`)
	formIDRe = regexp.MustCompile(`(?is)\bid="download-form"`)
	actionRe = regexp.MustCompile(`(?is)action="([^"]*)"`)
	inputRe  = regexp.MustCompile(`(?is)<input[^>]*>`)
	attrRe   = regexp.MustCompile(`(?is)\b(type|name|value)="([^"]*)"`)
	// Other page versions: a plain "Download anyway" link with confirm=...
	confirmHrefRe = regexp.MustCompile(`(?i)href="((?:https://(?:drive|docs)\.google\.com|https://drive\.usercontent\.google\.com)?/(?:u/\d+/)?(?:uc|download)\?[^"]*confirm=[^"]*)"`)
)

// parseDriveConfirm reads the "Download anyway" form of the virus-scan (or
// other confirm) page and returns the real download link ("" if none).
func parseDriveConfirm(page, base string) string {
	b, _ := url.Parse(base)
	for _, m := range formRe.FindAllStringSubmatch(page, -1) {
		formTag, body := m[1], m[2]
		action := ""
		if a := actionRe.FindStringSubmatch(formTag); a != nil {
			action = html.UnescapeString(a[1])
		}
		low := strings.ToLower(action)
		if !formIDRe.MatchString(formTag) && !strings.Contains(low, "/download") && !strings.Contains(low, "/uc") {
			continue // some other form on the page (search, sign out ...)
		}
		if action == "" {
			action = "https://drive.usercontent.google.com/download"
		}
		vals := url.Values{}
		for _, in := range inputRe.FindAllString(body, -1) {
			var typ, name, val string
			for _, a := range attrRe.FindAllStringSubmatch(in, -1) {
				switch strings.ToLower(a[1]) {
				case "type":
					typ = strings.ToLower(a[2])
				case "name":
					name = html.UnescapeString(a[2])
				case "value":
					val = html.UnescapeString(a[2])
				}
			}
			if name != "" && typ != "submit" {
				vals.Set(name, val)
			}
		}
		if vals.Get("id") == "" && b != nil {
			if id := b.Query().Get("id"); id != "" {
				vals.Set("id", id)
			}
		}
		if vals.Get("id") == "" {
			continue
		}
		// Keep the account and the resource key of the first link.
		if b != nil {
			for _, k := range []string{"authuser", "resourcekey"} {
				if vals.Get(k) == "" && b.Query().Get(k) != "" {
					vals.Set(k, b.Query().Get(k))
				}
			}
		}
		au, err := url.Parse(action)
		if err != nil {
			continue
		}
		if b != nil {
			au = b.ResolveReference(au)
		}
		au.RawQuery = vals.Encode()
		return au.String()
	}
	if m := confirmHrefRe.FindStringSubmatch(page); m != nil {
		root, _ := url.Parse("https://drive.google.com/")
		if b != nil && b.Host != "" {
			root = b
		}
		r, err := url.Parse(html.UnescapeString(m[1]))
		if err == nil {
			return root.ResolveReference(r).String()
		}
	}
	return ""
}

// driveProblem explains a Drive page that is not the file.
func driveProblem(page string) error {
	low := strings.ToLower(page)
	switch {
	case isViewOnlyPage(low):
		return errors.New(msgDriveViewOnly)
	case isRateLimitPage(low):
		return errDriveBusy
	case strings.Contains(low, "too many users have viewed or downloaded") || strings.Contains(low, "download quota") ||
		strings.Contains(low, "quota exceeded") || strings.Contains(low, "you can't view or download this file at this time"):
		return errors.New(msgDriveQuota)
	case strings.Contains(low, "you need access") || strings.Contains(low, "request access") ||
		strings.Contains(low, "accounts.google.com/servicelogin") || strings.Contains(low, "sign in"):
		return errors.New(msgDrivePrivate)
	}
	return nil
}

// errDriveBusy: Google is limiting requests ("429 Too Many Requests" or the
// "unusual traffic" page). Waiting helps, so it is not final.
var errDriveBusy = errors.New("Google Drive is limiting downloads right now (too many requests). BDDM waits and tries again; if it keeps happening, download fewer files at a time.")

// isViewOnlyPage: the owner turned off downloading for viewers ("Google
// Drive - Can't download file").
func isViewOnlyPage(low string) bool {
	return strings.Contains(low, "can't download file") || strings.Contains(low, "can&#39;t download file") ||
		strings.Contains(low, "cannot download file") || strings.Contains(low, "disabled downloading") ||
		strings.Contains(low, "has disabled download") || strings.Contains(low, "turned off downloading") ||
		strings.Contains(low, "download, print, and copy") || strings.Contains(low, "only the owner can download")
}

func isRateLimitPage(low string) bool {
	return strings.Contains(low, "unusual traffic") || strings.Contains(low, "automated queries") ||
		strings.Contains(low, "google.com/sorry") || strings.Contains(low, "rate limit exceeded") ||
		strings.Contains(low, "too many requests")
}

// driveSlots lets only four Drive files ask Google for their link at the same
// time (a folder with many files otherwise fires dozens of requests at once).
var driveSlots = make(chan struct{}, 4)

// driveSampleDir: where unknown Drive pages are saved for diagnosis (set by
// NewManager; "" = only the log).
var driveSampleDir string

var tagRe = regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>|<[^>]+>`)

// saveDrivePage writes a page Drive sent instead of the file to the log and
// to <data folder>/drive-pages, and returns its title.
func saveDrivePage(resp *http.Response, page string) string {
	title := ""
	if m := titleRe.FindStringSubmatch(page); m != nil {
		title = strings.TrimSpace(html.UnescapeString(m[1]))
	}
	where := ""
	status := 0
	if resp != nil {
		status = resp.StatusCode
		if resp.Request != nil && resp.Request.URL != nil {
			u := *resp.Request.URL
			u.RawQuery = "" // no tokens in the log
			where = u.String()
		}
	}
	text := strings.Join(strings.Fields(html.UnescapeString(tagRe.ReplaceAllString(page, " "))), " ")
	if len(text) > 600 {
		text = text[:600] + "..."
	}
	log.Printf("Google Drive sent an unknown page: HTTP %d %s title=%q text=%q", status, where, title, text)
	if driveSampleDir != "" {
		if err := os.MkdirAll(driveSampleDir, 0755); err == nil {
			name := filepath.Join(driveSampleDir, "page-"+time.Now().Format("20060102-150405.000")+".html")
			body := fmt.Sprintf("<!-- HTTP %d %s -->\n%s", status, where, page)
			if len(body) > 512<<10 {
				body = body[:512<<10]
			}
			_ = os.WriteFile(name, []byte(body), 0644)
			pruneDir(driveSampleDir, 30)
		}
	}
	return title
}

// pruneDir keeps only the newest keep files in dir.
func pruneDir(dir string, keep int) {
	es, err := os.ReadDir(dir)
	if err != nil || len(es) <= keep {
		return
	}
	sort.Slice(es, func(i, k int) bool { return es[i].Name() < es[k].Name() })
	for _, e := range es[:len(es)-keep] {
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}

// driveCandidates: the links to try for one file, best first. All keep the
// account (authuser) and the resource key.
func driveCandidates(l driveLink) []string {
	extra := ""
	if l.ResourceKey != "" {
		extra += "&resourcekey=" + url.QueryEscape(l.ResourceKey)
	}
	extra += l.authQuery(false)
	id := url.QueryEscape(l.ID)
	return []string{
		"https://drive.usercontent.google.com/download?id=" + id + "&export=download" + extra,
		"https://drive.usercontent.google.com/download?id=" + id + "&export=download&confirm=t" + extra,
		"https://drive.google.com/uc?export=download&id=" + id + extra,
	}
}

// resolveDrive finds the real download link of a Drive file and its name and
// size (without downloading it). It tries the other Drive download links when
// one answers with a page BDDM does not know. When Google refuses the
// signed-in request, the file is asked for once more without cookies: a
// public ("Anyone with the link") file then still works. info.NoCookie tells
// the caller to download without cookies; otherwise the cookies stay for
// every later request.
func resolveDrive(ctx context.Context, client *http.Client, l driveLink, h reqHeaders) (string, probeInfo, error) {
	select {
	case driveSlots <- struct{}{}:
		defer func() { <-driveSlots }()
	case <-ctx.Done():
		return "", probeInfo{}, ctx.Err()
	}
	u, info, err := resolveDriveAll(ctx, client, l, h)
	if err != nil && (err.Error() == msgDrivePrivate || err.Error() == msgDriveMissing) &&
		(h.Cookie != "" || l.AuthUser != "") && ctx.Err() == nil {
		anon, al := h, l
		anon.Cookie, al.AuthUser = "", ""
		if u2, info2, err2 := resolveDriveAll(ctx, client, al, anon); err2 == nil {
			info2.NoCookie = true
			return u2, info2, nil
		}
	}
	return u, info, err
}

func resolveDriveAll(ctx context.Context, client *http.Client, l driveLink, h reqHeaders) (string, probeInfo, error) {
	var firstErr error
	for i, c := range driveCandidates(l) {
		u, info, err := resolveDriveFrom(ctx, client, c, h)
		if err == nil {
			if i > 0 {
				log.Printf("Google Drive file %s: worked with link form %d", l.ID, i+1)
			}
			return u, info, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		if ctx.Err() != nil || isDriveFinal(err) || errors.Is(err, errDriveBusy) {
			return "", info, err
		}
	}
	return "", probeInfo{}, firstErr
}

// resolveDriveOnce is resolveDrive with the first link form only (tests).
func resolveDriveOnce(ctx context.Context, client *http.Client, l driveLink, h reqHeaders) (string, probeInfo, error) {
	return resolveDriveFrom(ctx, client, driveCandidates(l)[0], h)
}

func resolveDriveFrom(ctx context.Context, client *http.Client, first string, h reqHeaders) (string, probeInfo, error) {
	try := func(u string, ranged bool) (probeInfo, string, *http.Response, error) {
		var info probeInfo
		req, err := newRequest(ctx, http.MethodGet, u, h)
		if err != nil {
			return info, "", nil, err
		}
		if ranged {
			req.Header.Set("Range", "bytes=0-0")
		}
		resp, err := client.Do(req)
		if err != nil {
			return info, "", nil, err
		}
		defer resp.Body.Close()
		if isGoogleLogin(resp) {
			return info, "", resp, errors.New(msgDrivePrivate)
		}
		if strings.HasPrefix(resp.Request.URL.Path, "/sorry") {
			return info, "", resp, errDriveBusy
		}
		ct := strings.ToLower(resp.Header.Get("Content-Type"))
		if strings.HasPrefix(ct, "text/html") {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			page := string(body)
			if resp.StatusCode == 429 {
				if p := driveProblem(page); p != nil && p.Error() == msgDriveQuota {
					return info, "", resp, p
				}
				return info, "", resp, errDriveBusy
			}
			if p := driveProblem(page); p != nil && !strings.Contains(page, "download-form") {
				return info, "", resp, p
			}
			if resp.StatusCode == 404 {
				return info, "", resp, errors.New(msgDriveMissing)
			}
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				return info, "", resp, errors.New(msgDrivePrivate)
			}
			if resp.StatusCode >= 500 {
				return info, "", resp, fmt.Errorf("Google Drive answered HTTP %d", resp.StatusCode)
			}
			return info, page, resp, nil
		}
		switch {
		case resp.StatusCode == 429:
			return info, "", resp, errDriveBusy
		case resp.StatusCode == 404:
			return info, "", resp, errors.New(msgDriveMissing)
		case resp.StatusCode == 401 || resp.StatusCode == 403:
			return info, "", resp, errors.New(msgDrivePrivate)
		case resp.StatusCode >= 400:
			return info, "", resp, fmt.Errorf("Google Drive answered HTTP %d", resp.StatusCode)
		}
		info.Filename = filenameFromDisposition(resp.Header.Get("Content-Disposition"))
		if resp.StatusCode == http.StatusPartialContent {
			if size, ok := parseContentRange(resp.Header.Get("Content-Range")); ok {
				info.Size = size
				info.CanRange = true
			}
		} else if resp.ContentLength > 0 {
			info.Size = resp.ContentLength
		}
		return info, "", resp, nil
	}

	unknown := func(resp *http.Response, page string) error {
		title := saveDrivePage(resp, page)
		if title != "" {
			return fmt.Errorf("Google Drive sent a web page (%q) instead of the file. BDDM saved it in its data folder (drive-pages) and tried the other Drive download links too. Open the file in your browser and download it from there with the BDDM extension on.", title)
		}
		return errors.New("Google Drive did not send the file (it sent a web page BDDM does not know). Open the file in your browser and download it from there with the BDDM extension on.")
	}

	u := first
	info, page, resp, err := try(u, true)
	if err != nil {
		return "", info, err
	}
	if page != "" {
		// Some Drive answers differ for ranged requests: ask once without Range.
		if info2, page2, resp2, err2 := try(u, false); err2 == nil && page2 == "" {
			return u, info2, nil
		} else if err2 == nil {
			page, resp = page2, resp2
		}
	}
	if page == "" {
		return u, info, nil
	}
	// The "can't scan this file for viruses" page: take its form.
	next := parseDriveConfirm(page, resp.Request.URL.String())
	if next == "" {
		if p := driveProblem(page); p != nil {
			return "", info, p
		}
		return "", info, unknown(resp, page)
	}
	info, page, resp, err = try(next, true)
	if err != nil {
		return "", info, err
	}
	if page != "" {
		if p := driveProblem(page); p != nil {
			return "", info, p
		}
		return "", info, unknown(resp, page)
	}
	return next, info, nil
}

// ---------------------------------------------------------------------------
// Folders
// ---------------------------------------------------------------------------

// DriveNode is one item of a Drive folder tree.
type DriveNode struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Mime     string       `json:"mime,omitempty"`
	Size     int64        `json:"size,omitempty"` // 0 = unknown
	Folder   bool         `json:"folder,omitempty"`
	Shortcut bool         `json:"shortcut,omitempty"`
	DocType  string       `json:"docType,omitempty"` // Google Docs/Sheets/Slides
	Export   string       `json:"export,omitempty"`  // export format used for DocType
	Skip     string       `json:"skip,omitempty"`    // why it cannot be downloaded
	Children []*DriveNode `json:"children,omitempty"`
}

const (
	mimeFolder   = "application/vnd.google-apps.folder"
	mimeShortcut = "application/vnd.google-apps.shortcut"
)

var googleDocMimes = map[string]string{
	"application/vnd.google-apps.document":     "document",
	"application/vnd.google-apps.spreadsheet":  "spreadsheets",
	"application/vnd.google-apps.presentation": "presentation",
}

// folderDefaultExport: a folder download saves Docs as DOCX, Sheets as XLSX
// and Slides as PPTX.
var folderDefaultExport = map[string]string{"document": "docx", "spreadsheets": "xlsx", "presentation": "pptx"}

var ivdRe = regexp.MustCompile(`(?s)window\['_DRIVE_ivd'\]\s*=\s*'((?:[^'\\]|\\.)*)'`)

// jsUnescape decodes a JavaScript single-quoted string body.
func jsUnescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 'x':
			if i+2 < len(s) {
				if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
					b.WriteRune(rune(v))
					i += 2
					continue
				}
			}
			b.WriteByte('x')
		case 'u':
			if i+4 < len(s) {
				if v, err := strconv.ParseUint(s[i+1:i+5], 16, 16); err == nil {
					b.WriteRune(rune(v))
					i += 4
					continue
				}
			}
			b.WriteByte('u')
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// parseFolderPage reads the items of a Drive folder page (_DRIVE_ivd data).
// Only items whose parent is folderID are returned; foreign counts the rest.
func parseFolderPage(page, folderID string) (items []*DriveNode, foreign int, ok bool) {
	m := ivdRe.FindStringSubmatch(page)
	if m == nil {
		return nil, 0, false
	}
	var data []any
	if err := json.Unmarshal([]byte(jsUnescape(m[1])), &data); err != nil || len(data) == 0 {
		return nil, 0, false
	}
	list, _ := data[0].([]any)
	for _, e := range list {
		row, _ := e.([]any)
		if len(row) < 4 {
			continue
		}
		id, _ := row[0].(string)
		name, _ := row[2].(string)
		mime, _ := row[3].(string)
		if id == "" {
			continue
		}
		parentOK := false
		if ps, ok := row[1].([]any); ok {
			for _, p := range ps {
				if s, _ := p.(string); s == folderID {
					parentOK = true
				}
			}
		}
		if !parentOK {
			foreign++
			continue
		}
		n := &DriveNode{ID: id, Name: name, Mime: mime}
		if len(row) > 13 {
			if f, ok := row[13].(float64); ok && f > 0 {
				n.Size = int64(f)
			}
		}
		classifyNode(n)
		items = append(items, n)
	}
	return items, foreign, true
}

func classifyNode(n *DriveNode) {
	switch {
	case n.Mime == mimeFolder:
		n.Folder = true
	case n.Mime == mimeShortcut:
		n.Shortcut = true
	case googleDocMimes[n.Mime] != "":
		n.DocType = googleDocMimes[n.Mime]
		n.Export = folderDefaultExport[n.DocType]
	case strings.HasPrefix(n.Mime, "application/vnd.google-apps."):
		n.Skip = "Google " + strings.TrimPrefix(n.Mime, "application/vnd.google-apps.") + " files can't be downloaded"
	}
}

var (
	embEntryRe = regexp.MustCompile(`(?s)<div class="flip-entry" id="entry-([A-Za-z0-9_-]+)".*?<a href="([^"]*)".*?<div class="flip-entry-title">(.*?)</div>`)
)

// parseEmbeddedFolder reads drive.google.com/embeddedfolderview (it lists
// only the items of that one folder).
func parseEmbeddedFolder(page string) (title string, items []*DriveNode) {
	if m := titleRe.FindStringSubmatch(page); m != nil {
		title = strings.TrimSpace(html.UnescapeString(m[1]))
	}
	for _, m := range embEntryRe.FindAllStringSubmatch(page, -1) {
		n := &DriveNode{ID: m[1], Name: strings.TrimSpace(html.UnescapeString(m[3]))}
		link := html.UnescapeString(m[2])
		if l, ok := parseDriveLink(link); ok {
			switch l.Kind {
			case "folder":
				n.Folder, n.Mime = true, mimeFolder
			case "doc":
				n.DocType = l.DocType
				n.Export = folderDefaultExport[l.DocType]
			}
		}
		items = append(items, n)
	}
	return title, items
}

func folderTitle(page string) string {
	m := titleRe.FindStringSubmatch(page)
	if m == nil {
		return ""
	}
	t := strings.TrimSpace(html.UnescapeString(m[1]))
	return strings.TrimSpace(strings.TrimSuffix(t, "- Google Drive"))
}

func getPage(ctx context.Context, client *http.Client, u string, h reqHeaders) (string, error) {
	req, err := newRequest(ctx, http.MethodGet, u, h)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if isGoogleLogin(resp) {
		return "", errors.New("This folder is private. Open it in your browser while signed in and send it from the BDDM extension.")
	}
	if resp.StatusCode == 404 || resp.StatusCode == 401 || resp.StatusCode == 403 {
		return "", errors.New("Google Drive says this folder does not exist or is private. Open it in your browser while signed in and send it from the BDDM extension.")
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("Google Drive answered HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return string(b), err
}

const (
	maxFolderDepth = 20
	maxFolderItems = 5000
)

type folderLister struct {
	client  *http.Client
	h       reqHeaders
	rk      string
	au      string // "&authuser=N" or ""
	count   int
	foreign int
	seen    map[string]bool
}

// listDriveFolder lists a folder and its subfolders.
func listDriveFolder(ctx context.Context, client *http.Client, l driveLink, h reqHeaders) (*DriveNode, int, error) {
	fl := &folderLister{client: client, h: h, rk: l.ResourceKey, au: l.authQuery(false), seen: map[string]bool{}}
	root := &DriveNode{ID: l.ID, Folder: true, Mime: mimeFolder}
	if err := fl.list(ctx, root, 0); err != nil {
		return nil, 0, err
	}
	if root.Name == "" {
		root.Name = "Google Drive folder"
	}
	return root, fl.foreign, nil
}

func (fl *folderLister) list(ctx context.Context, f *DriveNode, depth int) error {
	if fl.seen[f.ID] {
		return nil // a loop
	}
	fl.seen[f.ID] = true
	rk := ""
	if depth == 0 && fl.rk != "" {
		rk = "?resourcekey=" + url.QueryEscape(fl.rk)
	}
	q := rk
	if fl.au != "" {
		if q == "" {
			q = "?" + fl.au[1:]
		} else {
			q += fl.au
		}
	}
	page, err := getPage(ctx, fl.client, "https://drive.google.com/drive/folders/"+f.ID+q, fl.h)
	if err != nil {
		return err
	}
	items, foreign, ok := parseFolderPage(page, f.ID)
	fl.foreign += foreign
	if f.Name == "" {
		f.Name = folderTitle(page)
	}
	// The folder page lists at most about 50 items; the embedded view lists
	// all items of this folder (but no sizes): add the missing ones.
	ek := ""
	if rk != "" {
		ek = "&resourcekey=" + url.QueryEscape(fl.rk)
	}
	emb, eerr := getPage(ctx, fl.client, "https://drive.google.com/embeddedfolderview?id="+f.ID+ek+fl.au, fl.h)
	if eerr == nil {
		title, eitems := parseEmbeddedFolder(emb)
		if f.Name == "" {
			f.Name = title
		}
		have := map[string]bool{}
		for _, it := range items {
			have[it.ID] = true
		}
		for _, it := range eitems {
			if !have[it.ID] {
				items = append(items, it)
			}
		}
	} else if !ok {
		return eerr
	}
	for _, it := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fl.count++
		if fl.count > maxFolderItems {
			return fmt.Errorf("This folder has more than %d items; BDDM stops listing there.", maxFolderItems)
		}
		if it.Folder && depth+1 < maxFolderDepth {
			if err := fl.list(ctx, it, depth+1); err != nil {
				it.Skip = "could not list this folder: " + err.Error()
			}
		}
		f.Children = append(f.Children, it)
	}
	return nil
}

// cleanDriveName makes a Drive name safe as a file/folder name.
func cleanDriveName(name string) string {
	name = sanitizeName(strings.TrimSpace(name))
	if !utf8.ValidString(name) {
		name = strings.ToValidUTF8(name, "_")
	}
	name = strings.TrimRight(name, ". ")
	if name == "" {
		name = "unnamed"
	}
	return name
}

// probeDoc checks a Docs/Sheets/Slides export link before downloading it.
// Export answers have no size and no ranges; a private document sends the
// Google sign-in page instead.
func probeDoc(ctx context.Context, client *http.Client, exportURL string, h reqHeaders) (probeInfo, error) {
	var info probeInfo
	req, err := newRequest(ctx, http.MethodGet, exportURL, h)
	if err != nil {
		return info, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return info, err
	}
	defer resp.Body.Close()
	switch {
	case isGoogleLogin(resp), resp.StatusCode == 401, resp.StatusCode == 403:
		return info, errors.New(msgDrivePrivate)
	case resp.StatusCode == 404:
		return info, errors.New(msgDriveMissing)
	case resp.StatusCode == 429:
		return info, errors.New(msgDriveQuota)
	case resp.StatusCode >= 400:
		return info, fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		page, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err := driveProblem(string(page)); err != nil {
			return info, err
		}
		return info, errors.New(msgDrivePrivate)
	}
	info.Filename = filenameFromDisposition(resp.Header.Get("Content-Disposition"))
	if resp.ContentLength > 0 && resp.Header.Get("Content-Encoding") == "" {
		info.Size = resp.ContentLength
	}
	return info, nil
}
