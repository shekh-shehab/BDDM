package main

// robust.go: keeping downloads safe when things change under them.
//
//   - Expired links: a server that answers 401/403/404/410 to a link that
//     worked before. Google Drive links are worked out again; other links are
//     refreshed when the user clicks the download again in the browser (the
//     extension sends the new link and BDDM continues the old download).
//   - A changed file on the server (other size, ETag or Last-Modified): the
//     download starts again instead of mixing old and new data.
//   - Connections per server: a server that answers 429/503 or drops
//     connections gets fewer connections, remembered for a while.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	errFileChanged = errors.New("the file on the server changed")
	errLinkExpired = errors.New("the download link has expired")
)

// linkExpiredError carries the HTTP status that showed the link expired.
type linkExpiredError struct{ code int }

func (e *linkExpiredError) Error() string {
	return fmt.Sprintf("the download link has expired (the server answered HTTP %d)", e.code)
}
func (e *linkExpiredError) Unwrap() error { return errLinkExpired }

// msgLinkExpired is shown to the user (and recognised by the extension).
const msgLinkExpired = "The download link has expired. Click the download again in your browser: BDDM will continue where it stopped."

// isExpiredStatus: answers that mean "this link does not work any more".
func isExpiredStatus(code int) bool {
	return code == http.StatusUnauthorized || code == http.StatusForbidden ||
		code == http.StatusNotFound || code == http.StatusGone
}

// isBlockStatus: the proxy or the site refuses BDDM on purpose (407 wants a
// proxy sign-in, 451 is blocked for legal reasons). Asking again does not help.
func isBlockStatus(code int) bool {
	return code == http.StatusProxyAuthRequired || code == http.StatusUnavailableForLegalReasons
}

// normETag compares ETags without the weak marker W/ and the quotes.
func normETag(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "W/")
	return strings.Trim(s, `"`)
}

// fileChanged reports why a saved download does not fit the file the
// server has now ("" = it fits). Unknown values never count as a change.
func fileChanged(st *State, size int64, etag, lastMod string) string {
	switch {
	case size > 0 && st.Size > 0 && size != st.Size:
		return fmt.Sprintf("its size changed from %s to %s", humanBytes(st.Size), humanBytes(size))
	case etag != "" && st.ETag != "" && normETag(etag) != normETag(st.ETag):
		return "its version (ETag) changed"
	case lastMod != "" && st.LastModified != "" && lastMod != st.LastModified:
		return "its date changed"
	}
	return ""
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// discardPartial removes an unfinished download (its .part and progress
// file) so it can start again from the beginning.
func discardPartial(full string) {
	_ = os.Remove(full + ".part")
	_ = os.Remove(full + ".bddm.json")
}

// linkWorks asks the server for the first byte. It returns the HTTP status
// (0 when the server could not be reached).
func linkWorks(ctx context.Context, client *http.Client, rawURL string, h reqHeaders) int {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := newRequest(ctx, http.MethodGet, rawURL, h)
	if err != nil {
		return 0
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	resp.Body.Close()
	return resp.StatusCode
}

// ---------------------------------------------------------------------------
// Refreshing an expired link from the browser
// ---------------------------------------------------------------------------

// RefreshLink looks for an unfinished download (failed or paused) of the
// same file as req and gives it the new link, so it continues where it
// stopped instead of starting a second download. It returns the job, or nil.
func (m *Manager) RefreshLink(req AddRequest, name string, size int64) *JobView {
	name = sanitizeName(name)
	if name == "" || name == "download.bin" {
		return nil
	}
	m.mu.Lock()
	var j *Job
	for i := len(m.jobs) - 1; i >= 0; i-- {
		o := m.jobs[i]
		if o.Kind != "" || o.Group != "" || (o.Status != StatusError && o.Status != StatusPaused) || o.URL == req.URL {
			continue
		}
		if !strings.EqualFold(o.Name, name) || (size > 0 && o.Size > 0 && size != o.Size) {
			continue
		}
		full := filepath.Join(o.Dir, o.Name)
		if !fileExists(full + ".part") {
			continue
		}
		// Only links that stopped working (or downloads the user paused).
		if o.Status == StatusError && !errorsLikeExpired(o.Error) {
			continue
		}
		j = o
		break
	}
	if j == nil {
		m.mu.Unlock()
		return nil
	}
	wantSize := j.Size
	m.mu.Unlock()
	if size <= 0 && wantSize > 0 && !isOneShot(req.URL, req.Referer) {
		// Quick look at the new link: a file of another size is another file.
		ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
		info, err := probe(ctx, m.client, req.URL, requestHeaders(req))
		cancel()
		if err == nil {
			size = info.Size
		}
	}
	if size > 0 && wantSize > 0 && size != wantSize {
		return nil
	}
	m.mu.Lock()
	if j.Status != StatusError && j.Status != StatusPaused {
		m.mu.Unlock()
		return nil // it was started meanwhile
	}
	old := j.URL
	full := filepath.Join(j.Dir, j.Name)
	if st, err := loadState(full + ".bddm.json"); err == nil {
		st.URL = req.URL
		if err := saveStateFile(full+".bddm.json", st); err != nil {
			log.Printf("[%s] could not update the saved progress: %v", j.Name, err)
		}
	}
	j.URL = req.URL
	if c := strings.TrimSpace(req.Cookie); c != "" {
		j.Cookie = c
	}
	if r := strings.TrimSpace(req.Referer); r != "" {
		j.Referer = r
	}
	j.applyBrowser(req) // the browser's headers and cookies for the new link
	id := j.ID
	m.saveHistoryLocked()
	m.mu.Unlock()
	log.Printf("[%s] new link from the browser (was %s); continuing", name, hostOf(old))
	_ = m.Resume(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	v := j.viewLocked()
	return &v
}

func errorsLikeExpired(msg string) bool {
	low := strings.ToLower(msg)
	return strings.Contains(low, "expired") || strings.Contains(low, "http 403") || strings.Contains(low, "http 410") ||
		strings.Contains(low, "http 401") || strings.Contains(low, "http 404") || strings.Contains(low, "rejected")
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return strings.ToLower(u.Host) // with the port: another port can be another server
	}
	return ""
}

// ---------------------------------------------------------------------------
// Connections per server
// ---------------------------------------------------------------------------

// hostConns remembers servers that do not like many connections: the
// number that worked, lowered on 429/503 or dropped connections, and raised
// again by one every few minutes without trouble.
type hostConnInfo struct {
	limit   int
	changed time.Time
}

var hostConns = struct {
	sync.Mutex
	m map[string]*hostConnInfo
}{m: map[string]*hostConnInfo{}}

const hostConnRecover = 3 * time.Minute

// hostLimit returns how many connections to open to host (at most want).
func hostLimit(host string, want int) int {
	hostConns.Lock()
	defer hostConns.Unlock()
	h := hostConns.m[strings.ToLower(host)]
	if h == nil {
		return want
	}
	// Slowly trust the server again.
	for h.limit < want && time.Since(h.changed) > hostConnRecover {
		h.limit++
		h.changed = h.changed.Add(hostConnRecover)
	}
	if h.limit >= want {
		delete(hostConns.m, strings.ToLower(host))
		return want
	}
	return max(1, h.limit)
}

// hostLowered records that host refused when n connections were open.
func hostLowered(host string, n int) {
	if host == "" {
		return
	}
	hostConns.Lock()
	defer hostConns.Unlock()
	key := strings.ToLower(host)
	h := hostConns.m[key]
	if h == nil || n < h.limit {
		hostConns.m[key] = &hostConnInfo{limit: max(1, n), changed: time.Now()}
	} else {
		h.changed = time.Now()
	}
}

// ---------------------------------------------------------------------------
// Files that survive a power cut
// ---------------------------------------------------------------------------

// writeFileSync writes p through a temporary file that is flushed to the
// disk before it replaces p, so p is always either the old or the new
// version, never half written.
func writeFileSync(p string, data []byte) error {
	tmp := p + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

// ---------------------------------------------------------------------------
// Free disk space
// ---------------------------------------------------------------------------

// spareSpace is kept free on the disk besides the file itself.
const spareSpace = 64 << 20

// checkFreeSpace fails when the disk of dir has less than need bytes free.
func checkFreeSpace(dir string, need int64) error {
	if need <= 0 {
		return nil
	}
	free, err := diskFree(dir)
	if err != nil || free < 0 {
		return nil // unknown: do not stop the download
	}
	if free < need+spareSpace {
		return &diskFullError{dir: dir, need: need, free: free}
	}
	return nil
}

type diskFullError struct {
	dir        string
	need, free int64
}

func (e *diskFullError) Error() string {
	where := filepath.VolumeName(e.dir)
	if where == "" {
		where = e.dir
	}
	return fmt.Sprintf("Not enough free space on %s: this download needs %s, but only %s is free. Free up some space or choose another folder in Settings, then click Retry.",
		where, humanBytes(e.need), humanBytes(e.free))
}
