package main

// headers.go: requests that look like the browser's own (1.4.1).
//
// The browser extension sends, with every link:
//   - the browser's real User-Agent and Accept-Language;
//   - the headers the browser itself sent for that link (Origin, custom
//     player headers such as X-Player-Token, Authorization ...);
//   - the cookies of every host involved (the page, the link and the CDN
//     it redirects to), each with its own domain.
// Captured headers and per-host cookies are kept in memory only (never in
// history.json), and are never sent to mirror links.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// defaultAccept / defaultLang: what a browser sends when nothing better is
// known (IDM sends "*/*" for downloads as well).
// probedFresh: how long the "Download File Info" window's look at a link
// is trusted when Start is pressed (instead of asking the server again).
const probedFresh = 2 * time.Minute

const (
	defaultAccept = "*/*"
	defaultLang   = "en-US,en;q=0.9"
)

// hostCookie is one browser cookie with its scope (from the extension).
type hostCookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain"`
	Path     string `json:"path,omitempty"`
	HostOnly bool   `json:"hostOnly,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
}

type cookieSet []hostCookie

const (
	maxCookies       = 400
	maxCapturedHdrs  = 32
	maxHeaderValue   = 8 << 10
	maxCookieValue   = 8 << 10
	maxCookieHdrSize = 32 << 10
)

// cleanCookies drops broken and oversized entries.
func cleanCookies(in []hostCookie) cookieSet {
	var out cookieSet
	for _, c := range in {
		c.Name = strings.TrimSpace(c.Name)
		c.Domain = strings.ToLower(strings.TrimSpace(c.Domain))
		if c.Name == "" || c.Domain == "" || len(c.Value) > maxCookieValue ||
			strings.ContainsAny(c.Name, "=;, \t\r\n") || strings.ContainsAny(c.Value, ";\r\n") ||
			strings.ContainsAny(c.Domain, " \t\r\n/") {
			continue
		}
		if c.Path == "" || !strings.HasPrefix(c.Path, "/") {
			c.Path = "/"
		}
		out = append(out, c)
		if len(out) >= maxCookies {
			break
		}
	}
	return out
}

// matches reports whether the browser would send c to u.
func (c hostCookie) matches(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	dom := strings.TrimPrefix(c.Domain, ".")
	if c.HostOnly {
		if host != dom {
			return false
		}
	} else if host != dom && !strings.HasSuffix(host, "."+dom) {
		return false
	}
	if c.Secure && u.Scheme != "https" && host != "localhost" && host != "127.0.0.1" {
		return false
	}
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if c.Path != "/" && p != c.Path && !strings.HasPrefix(p, strings.TrimSuffix(c.Path, "/")+"/") {
		return false
	}
	return true
}

// forURL: the cookies for u, longest path first (like browsers).
func (s cookieSet) forURL(u *url.URL) []hostCookie {
	var out []hostCookie
	seen := map[string]bool{}
	// Longer paths first; then more specific (host-only) domains.
	for pass := 0; pass < 2; pass++ {
		for _, c := range s {
			if (pass == 0) != (c.Path != "/") || seen[c.Name] || !c.matches(u) {
				continue
			}
			seen[c.Name] = true
			out = append(out, c)
		}
	}
	return out
}

// mergeCookieHeader adds cookies to a Cookie header; the header's own
// values win (they are what the browser really sent).
func mergeCookieHeader(header string, add []hostCookie) string {
	have := map[string]bool{}
	var parts []string
	for _, p := range strings.Split(header, ";") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		name, _, _ := strings.Cut(p, "=")
		have[name] = true
		parts = append(parts, p)
	}
	size := len(header)
	for _, c := range add {
		if have[c.Name] {
			continue
		}
		if size += len(c.Name) + len(c.Value) + 3; size > maxCookieHdrSize {
			break
		}
		have[c.Name] = true
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// skipCaptured: headers BDDM sets itself (or that would break ranges).
func skipCaptured(name string) bool {
	n := strings.ToLower(name)
	switch n {
	case "host", "connection", "keep-alive", "content-length", "content-type", "transfer-encoding", "te", "trailer",
		"upgrade", "range", "accept-encoding", "cookie", "user-agent", "referer", "proxy-authorization",
		"proxy-connection", "upgrade-insecure-requests", "priority", "cache-control", "pragma", "dnt":
		return true
	}
	return strings.HasPrefix(n, "if-") || strings.HasPrefix(n, "sec-") || strings.HasPrefix(n, ":")
}

func validHeaderName(n string) bool {
	if n == "" || len(n) > 100 {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
			return false
		}
	}
	return true
}

// cleanCaptured keeps the browser's headers that are worth copying.
func cleanCaptured(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !validHeaderName(k) || skipCaptured(k) || v == "" || len(v) > maxHeaderValue || strings.ContainsAny(v, "\r\n\x00") {
			continue
		}
		out[http.CanonicalHeaderKey(k)] = v
		if len(out) >= maxCapturedHdrs {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// cleanUA / cleanLang: one header value from the extension.
func cleanValue(v string, max int) string {
	v = strings.TrimSpace(v)
	if len(v) > max || strings.ContainsAny(v, "\r\n\x00") {
		return ""
	}
	return v
}

// captured returns the header value the browser sent (any spelling).
func captured(h map[string]string, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// betterReferer picks the Referer for target (1.4.2):
//  1. the Referer the browser really sent for this link, when the
//     extension saw it (it is what the site's own player sends);
//  2. for a stream (stream=true), what the browser sends by default
//     ("strict-origin-when-cross-origin"): only the site, e.g.
//     "https://megaplay.buzz/", when the player frame is on another site
//     than the stream; the full address on the same site;
//  3. otherwise the extension's referer as it is.
func betterReferer(ref, target string, hdrs map[string]string, stream bool) string {
	if real := strings.TrimSpace(captured(hdrs, "Referer")); isWebLink(real) {
		return real
	}
	if stream && ref != "" && target != "" {
		return policyReferer(ref, target)
	}
	return ref
}

// isWebLink: an http(s) link with a host.
func isWebLink(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// policyReferer: the Referer a browser sends by default when the page
// "from" loads "target": the full address (without #fragment) on the same
// origin, only "scheme://host/" on another origin, nothing from https to
// http.
func policyReferer(from, target string) string {
	f, err := url.Parse(strings.TrimSpace(from))
	if err != nil || (f.Scheme != "http" && f.Scheme != "https") || f.Host == "" {
		return from
	}
	t, err := url.Parse(strings.TrimSpace(target))
	if err != nil || t.Host == "" {
		return from
	}
	f.Fragment, f.RawFragment, f.User = "", "", nil
	if strings.EqualFold(f.Scheme, t.Scheme) && strings.EqualFold(f.Host, t.Host) {
		return f.String()
	}
	if f.Scheme == "https" && t.Scheme == "http" {
		return ""
	}
	return f.Scheme + "://" + f.Host + "/"
}

// streamOrigin is the Origin the player used: first the header the
// extension captured, then the origin of the Referer, then of the
// player frame, then of the tab page. Needed when the iframe is
// no-referrer so Referer is empty but the CDN still checks Origin.
func streamOrigin(j *Job) string {
	if j == nil {
		return ""
	}
	if o := captured(j.extra, "Origin"); o != "" {
		return o
	}
	for _, raw := range []string{j.Referer, j.refFrame, j.refPage} {
		if o := originOf(raw); o != "" {
			return o
		}
	}
	return ""
}

func applyStreamOrigin(j *Job, req *http.Request) {
	if req == nil || req.Header.Get("Origin") != "" {
		return
	}
	if o := streamOrigin(j); o != "" {
		req.Header.Set("Origin", o)
	}
}

// applyBrowser copies the browser details of r into j (j is new or locked).
func (j *Job) applyBrowser(r AddRequest) {
	hdrs := map[string]string{}
	for k, v := range r.Headers {
		hdrs[k] = v
	}
	if ua := cleanValue(firstNonEmpty(captured(hdrs, "User-Agent"), r.UserAgent), 1024); ua != "" {
		j.UA = ua
	}
	if lang := cleanValue(firstNonEmpty(captured(hdrs, "Accept-Language"), r.AcceptLanguage), 512); lang != "" {
		j.Lang = lang
	}
	stream := streamKind(r.Stream) != "" || isStreamURL(r.URL)
	if ref := betterReferer(j.Referer, r.URL, hdrs, stream); ref != j.Referer && cleanValue(ref, 8<<10) != "" {
		j.Referer = ref
	}
	if stream {
		j.RefAlts = refererAlternatives(j.Referer, r.URL, firstNonEmpty(r.Frame, r.Referer), r.Page)
	}
	if c := cleanValue(captured(hdrs, "Cookie"), maxCookieHdrSize); c != "" && j.Cookie == "" {
		j.Cookie = c
	}
	extra := cleanCaptured(hdrs)
	delete(extra, "Accept-Language") // kept in j.Lang
	j.extra = extra
	j.jar = cleanCookies(r.Cookies)
}

// browserHeaders: the request headers of job j (the caller holds m.mu or
// owns j). Mirrors must not get these: see forMirror.
func (j *Job) browserHeaders() reqHeaders {
	h := reqHeaders{Referer: j.Referer, Cookie: j.Cookie, UA: j.UA, Lang: j.Lang, Jar: j.jar}
	if len(j.extra) > 0 {
		h.Extra = map[string]string{}
		for k, v := range j.extra {
			h.Extra[k] = v
		}
	}
	if _, isDrive := parseDriveLink(j.URL); isDrive {
		// Google Drive has its own cookie and header rules (drive.go).
		h.Jar, h.Extra = nil, nil
	}
	return h
}

// requestHeaders: the headers for a link that is not a job yet.
func requestHeaders(r AddRequest) reqHeaders {
	j := &Job{URL: r.URL, Referer: strings.TrimSpace(r.Referer), Cookie: strings.TrimSpace(r.Cookie)}
	j.applyBrowser(r)
	return j.browserHeaders()
}

// forMirror: what a mirror link gets: no cookies, no captured headers.
func (h reqHeaders) forMirror() reqHeaders {
	return reqHeaders{Referer: h.Referer, UA: h.UA, Lang: h.Lang}
}

// ---------------------------------------------------------------- redirects

type jarKey struct{}

// withJar lets checkRedirect find the cookies for the next host.
func withJar(ctx context.Context, jar cookieSet) context.Context {
	if len(jar) == 0 {
		return ctx
	}
	return context.WithValue(ctx, jarKey{}, jar)
}

// checkRedirect follows up to 10 redirects like Go does, and gives each
// new host its own browser cookies (Go drops the Cookie header when a
// redirect goes to another site).
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	jar, _ := req.Context().Value(jarKey{}).(cookieSet)
	if len(jar) == 0 {
		return nil
	}
	if c := mergeCookieHeader(req.Header.Get("Cookie"), jar.forURL(req.URL)); c != "" {
		req.Header.Set("Cookie", c)
	}
	return nil
}

// ---------------------------------------------------------------- yt-dlp

// cookieFile writes the cookies of job j as a Netscape cookies.txt file
// for yt-dlp (so the CDN hosts of a video get their cookies too). The
// caller deletes the file (remove) when yt-dlp is done.
func writeCookieFile(dir string, rawURL, header string, jar cookieSet) (path string, remove func(), err error) {
	remove = func() {}
	var lines []string
	exp := time.Now().Add(48 * time.Hour).Unix()
	seen := map[string]bool{}
	add := func(c hostCookie) {
		dom := strings.TrimPrefix(c.Domain, ".")
		key := dom + "\x00" + c.Path + "\x00" + c.Name
		if seen[key] {
			return
		}
		seen[key] = true
		sub := "TRUE"
		if c.HostOnly {
			sub = "FALSE"
		} else {
			dom = "." + dom
		}
		sec := "FALSE"
		if c.Secure {
			sec = "TRUE"
		}
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s\t%d\t%s\t%s", dom, sub, c.Path, sec, exp, c.Name, c.Value))
	}
	// The Cookie header the browser sent for the link: cookies of its host.
	if u, perr := url.Parse(rawURL); perr == nil && u.Hostname() != "" && header != "" {
		for _, p := range strings.Split(header, ";") {
			name, value, ok := strings.Cut(strings.TrimSpace(p), "=")
			if ok && name != "" && !strings.ContainsAny(name+value, "\t\r\n") {
				add(hostCookie{Name: name, Value: value, Domain: strings.ToLower(u.Hostname()), Path: "/", HostOnly: true})
			}
		}
	}
	for _, c := range jar {
		if !strings.ContainsAny(c.Name+c.Value+c.Domain+c.Path, "\t\r\n") {
			add(c)
		}
	}
	if len(lines) == 0 {
		return "", remove, nil
	}
	f, err := os.CreateTemp(dir, "bddm-cookies-*.txt")
	if err != nil {
		return "", remove, err
	}
	path = f.Name()
	remove = func() { os.Remove(path) }
	_, err = f.WriteString("# Netscape HTTP Cookie File\n" + strings.Join(lines, "\n") + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		remove()
		return "", func() {}, err
	}
	return path, remove, nil
}

// removeOldCookieFiles deletes cookie files left behind when BDDM was
// closed while yt-dlp was running.
func removeOldCookieFiles() {
	files, _ := filepath.Glob(filepath.Join(os.TempDir(), "bddm-cookies-*.txt"))
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && time.Since(st.ModTime()) > time.Hour {
			os.Remove(f)
		}
	}
}
