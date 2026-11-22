package main

// playlist.go: help for HLS playlists that yt-dlp cannot read as they come.
//
// Some sites put extra bytes before the "#EXTM3U" header of their playlist.
// yt-dlp then stops with "Response data has no m3u header". BDDM fetches the
// playlist itself (with the same headers as the player), cuts everything
// before "#EXTM3U", makes all links in it absolute, saves it in a temp
// folder and gives yt-dlp that local copy.
//
// When what comes back is not a playlist at all (an encrypted blob, a web
// page or an error), BDDM explains that in plain words instead.
// AES-128 segment encryption is handled by the native HLS downloader
// (hls.go); SAMPLE-AES / DRM playlists are not decrypted.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxPlaylistSize = 8 << 20

var errNotPlaylist = errors.New("not an HLS playlist")

const (
	msgEncryptedStream = "This site encrypts its video stream, so BDDM can't download it. " +
		"If the site has its own Download button, use it and give BDDM the link it creates."
	msgExpiredStream = "The video link has probably expired (links from streaming sites only work for a short time). " +
		"Play the video again in your browser, then send the newest link from the BDDM extension."
	msgStreamRefused = "The video site refused to give this stream to BDDM (HTTP 403 Forbidden)."
	msgNotPlaylist   = "The site did not send a video playlist for this link (it may protect its stream). " +
		"Play the video again and send the newest link from the BDDM extension, or try another server on the site."
)

var uriAttrRe = regexp.MustCompile(`URI="([^"]*)"`)

// cleanPlaylist cuts anything before "#EXTM3U" and makes every link in
// the playlist (segments, keys, maps, variant and audio playlists)
// absolute, using base (the playlist's own address).
func cleanPlaylist(body []byte, base string) (string, error) {
	i := bytes.Index(body, []byte("#EXTM3U"))
	if i < 0 {
		return "", errNotPlaylist
	}
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	text := strings.ReplaceAll(string(body[i:]), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for k, l := range lines {
		l = strings.TrimSpace(l)
		switch {
		case l == "":
		case strings.HasPrefix(l, "#"):
			l = uriAttrRe.ReplaceAllStringFunc(l, func(m string) string {
				ref := uriAttrRe.FindStringSubmatch(m)[1]
				return `URI="` + resolveRef(b, ref) + `"`
			})
		default:
			l = resolveRef(b, l)
		}
		lines[k] = l
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n", nil
}

func resolveRef(base *url.URL, ref string) string {
	if strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "skd:") {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return base.ResolveReference(r).String()
}

// explainNotPlaylist turns an answer that is not a playlist into a plain
// English reason.
func explainNotPlaylist(status int, body []byte) error {
	if status == 401 || status == 403 {
		return errors.New(msgStreamRefused)
	}
	if status == 404 || status == 410 {
		return errors.New(msgExpiredStream)
	}
	t := strings.TrimSpace(string(body))
	low := strings.ToLower(t)
	switch {
	case strings.HasPrefix(low, "<!doctype") || strings.HasPrefix(low, "<html") || strings.Contains(low[:min(len(low), 512)], "<html"):
		return errors.New(msgNotPlaylist)
	case strings.HasPrefix(t, "{") || strings.HasPrefix(t, "["):
		if strings.Contains(low, "token") || strings.Contains(low, "expire") || strings.Contains(low, "denied") {
			return errors.New(msgExpiredStream)
		}
		return errors.New(msgNotPlaylist)
	case looksEncrypted(body):
		return errors.New(msgEncryptedStream)
	}
	return errors.New(msgNotPlaylist)
}

// looksEncrypted: a Base64 text or binary data instead of a playlist.
func looksEncrypted(body []byte) bool {
	t := bytes.TrimSpace(body)
	if len(t) < 32 {
		return false
	}
	b64, bin := 0, 0
	for _, c := range t {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '+', c == '/', c == '=', c == '-', c == '_', c == '\n', c == '\r':
			b64++
		case c < 9 || c > 126:
			bin++
		}
	}
	return b64 == len(t) || bin*10 > len(t)
}

// fetchPlaylist gets one playlist with the player's headers.
func (m *Manager) fetchPlaylist(ctx context.Context, j *Job, raw string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	req, err := newRequest(ctx, http.MethodGet, raw, j.browserHeaders())
	if err != nil {
		return 0, nil, err
	}
	applyStreamOrigin(j, req)
	resp, err := m.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPlaylistSize))
	return resp.StatusCode, body, err
}

// localizePlaylist saves a cleaned copy of the job's playlist (and of the
// quality/audio playlists of a master playlist) and returns its path.
func (m *Manager) localizePlaylist(ctx context.Context, j *Job) (string, error) {
	status, body, err := m.fetchPlaylist(ctx, j, j.URL)
	if err != nil {
		return "", fmt.Errorf("Could not read the video playlist: %v", err)
	}
	if status >= 400 {
		return "", explainNotPlaylist(status, body)
	}
	master, err := cleanPlaylist(body, j.URL)
	if err != nil {
		return "", explainNotPlaylist(status, body)
	}
	dir := filepath.Join(os.TempDir(), "BDDM-playlists", j.ID)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	if strings.Contains(master, "#EXT-X-STREAM-INF") {
		// A master playlist: clean the playlists it points to as well.
		n := 0
		local := map[string]string{}
		get := func(ref string) string {
			if p, ok := local[ref]; ok {
				return p
			}
			if n >= 40 {
				return ref
			}
			st, b, err := m.fetchPlaylist(ctx, j, ref)
			if err != nil || st >= 400 {
				return ref
			}
			txt, err := cleanPlaylist(b, ref)
			if err != nil {
				return ref
			}
			n++
			p := filepath.Join(dir, fmt.Sprintf("v%d.m3u8", n))
			if os.WriteFile(p, []byte(txt), 0644) != nil {
				return ref
			}
			local[ref] = fileURL(p)
			return local[ref]
		}
		lines := strings.Split(master, "\n")
		afterInf := false
		for k, l := range lines {
			switch {
			case strings.HasPrefix(l, "#EXT-X-STREAM-INF"):
				afterInf = true
			case strings.HasPrefix(l, "#EXT-X-MEDIA:"):
				lines[k] = uriAttrRe.ReplaceAllStringFunc(l, func(s string) string {
					return `URI="` + get(uriAttrRe.FindStringSubmatch(s)[1]) + `"`
				})
			case l != "" && !strings.HasPrefix(l, "#") && afterInf:
				lines[k] = get(l)
				afterInf = false
			}
		}
		master = strings.Join(lines, "\n")
	}
	p := filepath.Join(dir, "playlist.m3u8")
	if err := os.WriteFile(p, []byte(master), 0644); err != nil {
		return "", err
	}
	return p, nil
}

func removeLocalPlaylist(j *Job) {
	if j.localPL != "" {
		_ = os.RemoveAll(filepath.Dir(j.localPL))
	}
}

// fileURL makes a file:/// link (also for Windows paths like C:\x).
func fileURL(p string) string {
	s := filepath.ToSlash(p)
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	return (&url.URL{Scheme: "file", Path: s}).String()
}

// wantsPlaylistFix: yt-dlp could not read a stream link's playlist.
func wantsPlaylistFix(msg string) bool {
	low := strings.ToLower(msg)
	return strings.Contains(low, "no m3u header") || strings.Contains(low, "unsupported url") ||
		strings.Contains(low, "m3u8") && strings.Contains(low, "invalid")
}
