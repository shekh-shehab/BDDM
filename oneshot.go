package main

// oneshot.go: links that work only once (1.4.2), like Send Anywhere's.
//
// A Send Anywhere transfer ends when its download link has been read, so
// every extra request (a size check, a second connection) can use the file
// up before the real download starts. The browser extensions leave these
// downloads to the browser. When such a link still reaches BDDM (pasted or
// right-clicked), BDDM asks for it exactly once: no size or name check
// first, one connection, no Range header.

import (
	"net/url"
	"strings"
)

// oneShotHosts: sites whose download links work only once.
var oneShotHosts = []string{"send-anywhere.com", "sendanywhe.re", "send-anywhere.net"}

func onOneShotHost(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, s := range oneShotHosts {
		if h == s || strings.HasSuffix(h, "."+s) {
			return true
		}
	}
	return false
}

// isOneShot: the link (or the page it came from) is Send Anywhere's.
func isOneShot(link, referer string) bool {
	return onOneShotHost(link) || onOneShotHost(referer)
}

// oneShotPage: a Send Anywhere share or download page (not a file): only
// the browser can run it.
func oneShotPage(link string) bool {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || !onOneShotHost(link) {
		return false
	}
	h := strings.ToLower(u.Hostname())
	p := strings.ToLower(u.Path)
	return h == "sendanywhe.re" || h == "www.sendanywhe.re" ||
		((h == "send-anywhere.com" || h == "www.send-anywhere.com") && (p == "" || p == "/" || strings.HasPrefix(p, "/web/")))
}

const msgOneShotPage = "This is a Send Anywhere page. Its files can be received only in the browser: open the link there and press Download (BDDM leaves Send Anywhere downloads to the browser, because their links work only once)."
