package main

// explain.go: turns error messages into a plain reason and a suggested fix
// for the Downloads list (A12). The original text stays available as
// "Details".

import (
	"regexp"
	"strings"
)

type errorHelp struct {
	match  func(low string) bool
	reason string // "" = keep the message (it is already plain)
	fix    string
}

func has(words ...string) func(string) bool {
	return func(low string) bool {
		for _, w := range words {
			if strings.Contains(low, w) {
				return true
			}
		}
		return false
	}
}

var httpCodeRe = regexp.MustCompile(`http (\d{3})\b|\(http (\d{3})\)|status (\d{3})\b|returned http (\d{3})`)

func httpCode(low string) string {
	m := httpCodeRe.FindStringSubmatch(low)
	if m == nil {
		return ""
	}
	for _, g := range m[1:] {
		if g != "" {
			return g
		}
	}
	return ""
}

func code(c ...string) func(string) bool {
	return func(low string) bool {
		got := httpCode(low)
		for _, x := range c {
			if got != "" && (got == x || (len(x) == 2 && strings.HasPrefix(got, x))) {
				return true
			}
		}
		return false
	}
}

const fixRetry = "Click Retry. BDDM continues where it stopped."

var errorHelps = []errorHelp{
	{has("checksum mismatch"), "The downloaded file does not match its checksum.",
		"Make sure the checksum is for this exact file and version. The file was kept so you can decide; delete it and download again if you do not trust it."},
	{has("not enough free space", "no space left", "disk full", "there is not enough space"), "There is not enough free space on the disk.",
		"Free up some space, or choose another folder in Settings, then click Retry."},
	{has("access is denied", "permission denied", "operation not permitted", "read-only file system"), "BDDM is not allowed to save files in this folder.",
		"Choose another download folder in Settings (for example one inside your Downloads folder), then click Retry."},
	{has("file name too long", "filename too long", "name too long", "the filename, directory name"), "The file name or folder path is too long for Windows.",
		"Choose a shorter download folder in Settings (for example D:\\Downloads), then click Retry."},
	{has("being used by another process", "could not rename file"), "The finished file is being used by another program.",
		"Close programs that may have the file open (a video player, antivirus scan), then click Retry."},
	{has("link has expired"), "", "Click the download again on the website. BDDM continues where it stopped."},
	{has("x509", "certificate", "tls: "), "The website's security certificate could not be checked.",
		"Check that the date and time on your computer are right. If they are, the website's certificate is broken; only download from it if you trust it."},
	{has("no such host", "name resolution", "getaddrinfo failed", "failed to resolve", "nodename nor servname", "lookup "), "The website's address could not be found.",
		"Check that the link is typed correctly and that you are online. If other websites work, this website may be down."},
	{has("connection refused", "actively refused", "errno 111", "errno 10061"), "The server refused the connection.",
		"The website may be down or busy. Try again later."},
	{has("network is unreachable", "no route to host", "unable to connect"), "Your computer could not reach the server.",
		"Check your internet connection (Wi-Fi, cable, VPN). Downloads continue by themselves when the connection is back."},
	{has("timeout", "timed out", "stalled"), "The server stopped answering.",
		"Check your internet connection and click Retry. BDDM continues where it stopped."},
	{has("connection reset", "forcibly closed", "broken pipe", "unexpected eof", "connection closed early", "incompleteread", "remote end closed"), "The connection was cut off.",
		"Click Retry; BDDM continues where it stopped. If it keeps happening, lower \"Parts per download\" in Settings."},
	{has("too many requests"), "The server is limiting how much you can download right now.",
		"Wait a few minutes and click Retry. Fewer \"Parts per download\" or \"Downloads at the same time\" in Settings can help."},
	{has("web page instead of the file"), msgSitePage,
		"If BDDM came from your browser's download, the browser downloads the file itself. Otherwise sign in on the website and start the download again from your browser."},
	{code("407"), "A proxy on your network wants you to sign in.",
		"If the download came from your browser, the browser downloads it itself. Otherwise check your proxy settings."},
	{code("401"), "The server wants you to sign in.",
		"Sign in on the website, then start the download again from your browser so BDDM gets your sign-in."},
	{code("403"), "The website refused to give this file to BDDM (HTTP 403 Forbidden).",
		fix403},
	{code("404"), "The file was not found on the server.",
		"The link may be wrong, or the file was moved or deleted. Get a fresh link from the website."},
	{code("410"), "The file is no longer on the server.",
		"Get a fresh link from the website."},
	{code("416"), "The server could not send the missing piece of the file.",
		"Delete the download and start it again."},
	{code("429"), "The server is limiting how much you can download right now.",
		"Wait a few minutes and click Retry."},
	{code("50"), "The website's server had a problem.",
		"This is on the website's side. Try again later."},
	{has("incomplete download", "did not finish", "failed after"), "The download stopped before the whole file arrived.", fixRetry},
	{has("server does not support range"), "The server cannot continue an interrupted download.",
		"Click Retry to download the file again from the start."},
	{has("video tools", "yt-dlp", "ffmpeg"), "", "Open Settings and check the video tools (install or update them)."},
	{has("sign in to confirm", "login required", "private video", "members-only", "confirm your age", "age-restricted"), "",
		"Choose your browser under \"Use cookies from\" in Settings so BDDM can use your sign-in, then click Retry."},
}

// fix403: what to do when a website answers 403 Forbidden.
const fix403 = "Start the download again from the website in your browser: BDDM then gets a fresh link together with " +
	"your browser's sign-in, cookies and settings. If it is refused again, the site lets only the browser download " +
	"this file: hold Alt while you click the download link, and the browser downloads it itself."

// explainError returns a plain reason (the message itself when it is
// already plain) and a suggested fix ("" when there is nothing to add).
func explainError(msg string) (reason, fix string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "", ""
	}
	low := strings.ToLower(msg)
	for _, h := range errorHelps {
		if h.match(low) {
			// BDDM's general "refused (HTTP n)" text: the table knows the
			// code better, so give its reason and fix instead of two advices.
			if h.reason != "" && strings.HasPrefix(msg, "The server refused the download (HTTP ") {
				if c := httpCode(low); c != "" {
					if strings.Contains(h.reason, "HTTP ") {
						return h.reason, h.fix
					}
					r := strings.TrimSuffix(h.reason, ".")
					if strings.HasSuffix(r, ")") {
						return strings.TrimSuffix(r, ")") + ", HTTP " + c + ").", h.fix
					}
					return r + " (HTTP " + c + ").", h.fix
				}
			}
			if h.reason == "" || looksPlain(msg) {
				return msg, h.fix
			}
			return h.reason, h.fix
		}
	}
	if looksPlain(msg) {
		return msg, ""
	}
	return "The download failed: " + msg, "Click Retry. If it keeps failing, start the download again from your browser."
}

// looksPlain: a message BDDM wrote for people (a sentence), not a
// technical error from Go or a server.
func looksPlain(msg string) bool {
	if msg == "" {
		return false
	}
	low := strings.ToLower(msg)
	for _, tech := range []string{"dial tcp", "read tcp", "write tcp", "get \"", "lookup ", "x509", "errno", "eof", "tls:", ": i/o", "context deadline", "wsarecv", "wsasend"} {
		if strings.Contains(low, tech) {
			return false
		}
	}
	c := msg[0]
	return c >= 'A' && c <= 'Z' && strings.ContainsAny(msg, ".!?")
}
