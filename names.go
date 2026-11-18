package main

// names.go: file names from the page title (1.4.2).
//
// Many links have no useful name of their own ("stream.mp4", "video.mp4",
// "download", "index.html", a long random id). Then BDDM names the file
// after the page it came from, cleaned up: no site name or "Watch online"
// around it, no characters Windows does not allow, not too long.

import (
	"html"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxTitleName: bytes of a name made from a title (without extension).
const maxTitleName = 150

var uselessBases = map[string]bool{
	"": true, "stream": true, "streams": true, "video": true, "videos": true, "download": true, "downloads": true,
	"file": true, "files": true, "index": true, "master": true, "playlist": true, "media": true, "play": true,
	"player": true, "get": true, "getfile": true, "watch": true, "view": true, "videoplayback": true, "output": true,
	"out": true, "content": true, "attachment": true, "untitled": true, "default": true, "blob": true, "data": true,
	"chunk": true, "movie": true, "audio": true, "clip": true, "embed": true, "source": true, "src": true,
	"raw": true, "dl": true, "temp": true, "tmp": true, "unknown": true, "noname": true, "document": true,
	"page": true, "main": true, "manifest": true, "hls": true, "mp4": true, "file.php": true, "download.php": true,
	"get.php": true, "video_file": true, "videofile": true, "mov": true, "track": true, "song": true, "music": true,
}

var (
	digitsOnlyRe = regexp.MustCompile(`^[0-9_-]+$`)
	hexIDRe      = regexp.MustCompile(`^[0-9a-f]{16,}$`)
	uuidRe       = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	randomIDRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{24,}$`)
	numberedRe   = regexp.MustCompile(`^(.*?)[ _-]*(\(\d+\)|\d+)$`)
)

// uselessName: a file name that says nothing about what is in the file.
func uselessName(name string) bool {
	name = strings.TrimSpace(name)
	ext := path.Ext(name)
	base := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(name, ext)))
	if len(ext) > 6 || strings.ContainsAny(ext, " ") {
		base, ext = strings.ToLower(name), ""
	}
	if uselessBases[base] || uselessBases[strings.ToLower(name)] {
		return true
	}
	// "video (1)", "stream_2", "download-3"
	if m := numberedRe.FindStringSubmatch(base); m != nil && m[1] != "" && uselessBases[strings.TrimSpace(m[1])] {
		return true
	}
	if digitsOnlyRe.MatchString(base) || hexIDRe.MatchString(base) || uuidRe.MatchString(base) {
		return true
	}
	if randomIDRe.MatchString(base) {
		digits := 0
		for _, r := range base {
			if unicode.IsDigit(r) {
				digits++
			}
		}
		return digits >= 4
	}
	return false
}

var (
	titleSepRe = regexp.MustCompile(`\s+[|\-–—·»:]{1,2}\s+`)
	siteWordRe = regexp.MustCompile(`(?i)(\b(watch|online|free|download|downloads|streaming|stream|subbed|dubbed|official site|full movie|hd|tv|anime|movies?)\b|tv$|\.(com|net|org|tv|to|bz|me|io|cc|co|top|xyz|site|online|live|ru|in|uk|de|fr|info|biz)\b)`)
	domainRe   = regexp.MustCompile(`(?i)^(www\.)?[a-z0-9-]+(\.[a-z0-9-]+)+$`)
	watchRe    = regexp.MustCompile(`(?i)^(watch|download|stream)\s+`)
	onlineRe   = regexp.MustCompile(`(?i)\s+(online(\s+(for\s+)?free)?(\s+in\s+hd)?|in\s+hd|for\s+free|free)$`)
	countRe    = regexp.MustCompile(`^\(\d+\+?\)\s*`) // "(3) Title" (new messages)
	spacesRe   = regexp.MustCompile(`\s+`)
)

// cleanTitle turns a page title into a file name (without extension), or
// "" when nothing useful is left.
func cleanTitle(title string) string {
	t := html.UnescapeString(title)
	t = strings.Map(func(r rune) rune {
		switch {
		case r == '\u200b' || r == '\u200c' || r == '\u200d' || r == '\ufeff':
			return -1
		case unicode.IsControl(r):
			return ' '
		}
		return r
	}, t)
	t = strings.TrimSpace(spacesRe.ReplaceAllString(t, " "))
	t = countRe.ReplaceAllString(t, "")
	// The site's name around the title: "Title - Site.com", "Site | Title".
	for {
		locs := titleSepRe.FindAllStringIndex(t, -1)
		if len(locs) == 0 {
			break
		}
		last := locs[len(locs)-1]
		tail := strings.TrimSpace(t[last[1]:])
		if tail != "" && (domainRe.MatchString(tail) || (siteWordRe.MatchString(tail) && len(strings.Fields(tail)) <= 6)) {
			t = strings.TrimSpace(t[:last[0]])
			continue
		}
		first := locs[0]
		head := strings.TrimSpace(t[:first[0]])
		if head != "" && domainRe.MatchString(head) {
			t = strings.TrimSpace(t[first[1]:])
			continue
		}
		break
	}
	t = watchRe.ReplaceAllString(t, "")
	t = onlineRe.ReplaceAllString(t, "")
	// Characters Windows does not allow in names.
	t = strings.NewReplacer(":", " -", "/", "-", "\\", "-", "|", "-", "?", "", "*", "", "\"", "'", "<", "", ">", "").Replace(t)
	t = strings.Map(func(r rune) rune {
		if r < 32 {
			return -1
		}
		return r
	}, t)
	t = strings.TrimSpace(spacesRe.ReplaceAllString(t, " "))
	t = strings.ReplaceAll(t, "- -", "-")
	if len(t) > maxTitleName {
		cut := maxTitleName
		for cut > 0 && !utf8.RuneStart(t[cut]) {
			cut--
		}
		t = t[:cut]
		if i := strings.LastIndex(t, " "); i > maxTitleName/2 {
			t = t[:i] // do not cut a word in half
		}
	}
	t = strings.Trim(t, " .-_")
	if isReservedName(t) {
		t += "_"
	}
	if strings.Trim(t, " .-_'") == "" {
		return ""
	}
	return t
}

// isReservedName: names Windows does not allow (CON, NUL, COM1, ...).
func isReservedName(s string) bool {
	u := strings.ToUpper(strings.TrimSpace(s))
	switch u {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(u) == 4 && (strings.HasPrefix(u, "COM") || strings.HasPrefix(u, "LPT")) && u[3] >= '1' && u[3] <= '9' {
		return true
	}
	return false
}

// nameWithTitle: name, or the page title with name's extension when name
// says nothing. Web page extensions (.html, .php) are not kept.
func nameWithTitle(name, title string) string {
	if !uselessName(name) {
		return name
	}
	t := cleanTitle(title)
	if t == "" {
		return name
	}
	ext := strings.ToLower(path.Ext(strings.TrimSpace(name)))
	switch ext {
	case ".php", ".asp", ".aspx", ".jsp", ".cgi", ".bin":
		ext = ""
	}
	if len(ext) > 6 || strings.ContainsAny(ext, " ") {
		ext = ""
	}
	return t + ext
}
