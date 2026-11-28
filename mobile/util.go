package mobile

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

func firstNonEmpty(vals ...string) string {
	for _, a := range vals {
		if strings.TrimSpace(a) != "" {
			return a
		}
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func filenameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "download"
	}
	base := filepath.Base(u.Path)
	base, _ = url.PathUnescape(base)
	base = sanitizeName(base)
	if base == "" || base == "." || base == "/" {
		return "download"
	}
	return base
}

func filenameFromDisposition(h string) string {
	if h == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(h)
	if err == nil {
		if n := params["filename*"]; n != "" {
			if i := strings.Index(n, "''"); i >= 0 {
				n = n[i+2:]
			}
			if dec, err := url.QueryUnescape(n); err == nil {
				n = dec
			}
			return sanitizeName(n)
		}
		if n := params["filename"]; n != "" {
			return sanitizeName(n)
		}
	}
	// fallback: filename="..."
	if i := strings.Index(strings.ToLower(h), "filename="); i >= 0 {
		n := strings.Trim(h[i+9:], ` ";`)
		return sanitizeName(n)
	}
	return ""
}

var unsafeName = regexp.MustCompile(`[/\\:*?"<>|\x00-\x1f]`)

func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	name = filepath.Base(name)
	name = unsafeName.ReplaceAllString(name, "_")
	name = strings.Trim(name, " .")
	if name == "" || name == "." || name == ".." {
		return ""
	}
	// keep it a reasonable length
	runes := []rune(name)
	if len(runes) > 180 {
		ext := filepath.Ext(name)
		stem := strings.TrimSuffix(string(runes), ext)
		rs := []rune(stem)
		if len(rs) > 180-len([]rune(ext)) {
			rs = rs[:180-len([]rune(ext))]
		}
		name = string(rs) + ext
	}
	for _, r := range name {
		if !unicode.IsSpace(r) && r != '.' {
			return name
		}
	}
	return ""
}

// limiter is a simple byte-per-second cap shared by all HTTP reads. 0 = off.
type limiter struct {
	mu   sync.Mutex
	bps  int64
	used int64
	win  time.Time
}

func newLimiter(bps int64) *limiter { return &limiter{bps: bps, win: time.Now()} }

func (l *limiter) set(bps int64) {
	l.mu.Lock()
	l.bps = bps
	l.used = 0
	l.win = time.Now()
	l.mu.Unlock()
}

func (l *limiter) wait(n int, stop <-chan struct{}) {
	if l == nil {
		return
	}
	for {
		l.mu.Lock()
		bps := l.bps
		if bps <= 0 {
			l.mu.Unlock()
			return
		}
		now := time.Now()
		if now.Sub(l.win) >= time.Second {
			l.win = now
			l.used = 0
		}
		if l.used+int64(n) <= bps {
			l.used += int64(n)
			l.mu.Unlock()
			return
		}
		wait := time.Second - now.Sub(l.win)
		l.mu.Unlock()
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		select {
		case <-stop:
			return
		case <-time.After(wait):
		}
	}
}

type limitedReader struct {
	r    io.Reader
	l    *limiter
	stop <-chan struct{}
}

func (r limitedReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 && r.l != nil {
		r.l.wait(n, r.stop)
	}
	return n, err
}

type httpClient struct {
	c *http.Client
}

func newHTTPClient() *httpClient {
	return &httpClient{c: &http.Client{
		Timeout: 0,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          32,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   20 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			ForceAttemptHTTP2:     true,
		},
	}}
}

func parseHeaders(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range m {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || v == "" || strings.ContainsAny(k+v, "\r\n") {
			continue
		}
		switch strings.ToLower(k) {
		case "range", "host", "content-length", "accept-encoding", "connection":
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// applyHeaders sets the default browser-like headers, then the job's own.
func applyHeaders(req *http.Request, h map[string]string) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")
	for k, v := range h {
		req.Header.Set(k, v)
	}
}
