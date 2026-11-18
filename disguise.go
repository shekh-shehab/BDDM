package main

// disguise.go: HLS streams whose pieces hide behind a fake picture (1.4.2),
// and the quality list of a master playlist.
//
// Some players (megaplay.buzz "HD-1") store each video piece on an image
// server: the piece starts with a tiny 1x1 PNG and the real MPEG-TS video
// data follows. Browsers' players skip the picture; ffmpeg sees a PNG and
// stops. BDDM looks at the first piece before the download. When it is
// such a piece, ffmpeg is told to read the pieces as MPEG-TS ("-f mpegts"),
// which skips the picture and finds the video.
//
// BDDM also reads the playlist itself: a master playlist lists the
// qualities (1080p, 720p, ...), and when the link is only one quality's
// playlist, BDDM looks for the master next to it so all qualities show.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// hlsLook: what BDDM learned from the playlist itself.
type hlsLook struct {
	Master    bool
	Qualities []hlsQuality
	FirstPart string // the first video piece's address
	Master2   string // for a single-quality playlist: its master playlist
	Hidden    bool   // the pieces are MPEG-TS behind a fake picture
}

type hlsQuality struct {
	Height    int
	Bandwidth int64
	URL       string
}

var (
	resolutionRe = regexp.MustCompile(`(?i)RESOLUTION=(\d+)x(\d+)`)
	bandwidthRe  = regexp.MustCompile(`(?i)[:,]BANDWIDTH=(\d+)`)
)

// parseMaster reads the qualities of a cleaned master playlist (links are
// absolute). The list is sorted from the highest quality down.
func parseMaster(text string) []hlsQuality {
	var out []hlsQuality
	var cur *hlsQuality
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "#EXT-X-STREAM-INF"):
			q := hlsQuality{}
			if m := resolutionRe.FindStringSubmatch(l); m != nil {
				q.Height, _ = strconv.Atoi(m[2])
			}
			if m := bandwidthRe.FindStringSubmatch(l); m != nil {
				q.Bandwidth, _ = strconv.ParseInt(m[1], 10, 64)
			}
			cur = &q
		case l == "" || strings.HasPrefix(l, "#"):
		case cur != nil:
			cur.URL = l
			out = append(out, *cur)
			cur = nil
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Height != out[b].Height {
			return out[a].Height > out[b].Height
		}
		return out[a].Bandwidth > out[b].Bandwidth
	})
	return out
}

// firstPiece: the first media piece of a cleaned media playlist.
func firstPiece(text string) string {
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			return l
		}
	}
	return ""
}

// siblingMaster: "master.m3u8" in the same folder as a single-quality
// playlist (with the same query), or "" when the link is the master.
func siblingMaster(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !strings.HasSuffix(strings.ToLower(u.Path), ".m3u8") {
		return ""
	}
	if strings.EqualFold(path.Base(u.Path), "master.m3u8") {
		return ""
	}
	u.Path = path.Join(path.Dir(u.Path), "master.m3u8")
	u.RawPath = ""
	return u.String()
}

// samePlaylist compares two playlist links without their queries (tokens).
func samePlaylist(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && strings.EqualFold(ua.Host, ub.Host) && ua.Path == ub.Path
}

// imageMagic: the data starts like a picture file.
func imageMagic(b []byte) bool {
	return bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) || bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}) ||
		bytes.HasPrefix(b, []byte("GIF8")) || bytes.HasPrefix(b, []byte("BM")) ||
		(len(b) >= 12 && bytes.HasPrefix(b, []byte("RIFF")) && string(b[8:12]) == "WEBP")
}

// hiddenTS: a picture header followed by MPEG-TS packets (0x47 every 188
// bytes), i.e. a video piece in disguise.
func hiddenTS(b []byte) bool {
	if !imageMagic(b) {
		return false
	}
	for i := 4; i+2*188 < len(b) && i < 64<<10; i++ {
		if b[i] == 0x47 && b[i+188] == 0x47 && b[i+2*188] == 0x47 {
			return true
		}
	}
	return false
}

// lookAtHLS fetches the job's playlist (and, for a master, its best
// quality's playlist) and the first bytes of the first piece.
// A 401/403 is answered with the job's other Referers first (referer.go).
func (m *Manager) lookAtHLS(ctx context.Context, j *Job) (*hlsLook, error) {
	m.mu.Lock()
	raw := j.URL
	m.mu.Unlock()
	status, body, err := m.fetchPlaylist(ctx, j, raw)
	if err == nil && (status == 401 || status == 403) {
		status, body, err = m.playlistWithOtherReferers(ctx, j, raw, status, body)
	}
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, explainNotPlaylist(status, body)
	}
	text, err := cleanPlaylist(body, raw)
	if err != nil {
		return nil, explainNotPlaylist(status, body)
	}
	look := &hlsLook{}
	media := text
	mediaURL := raw
	if strings.Contains(text, "#EXT-X-STREAM-INF") {
		look.Master = true
		look.Qualities = parseMaster(text)
		if len(look.Qualities) == 0 {
			return look, nil
		}
		mediaURL = look.Qualities[len(look.Qualities)-1].URL // the smallest is enough to look
		st, b, err := m.fetchPlaylist(ctx, j, mediaURL)
		if err != nil || st >= 400 {
			return look, nil
		}
		if media, err = cleanPlaylist(b, mediaURL); err != nil {
			return look, nil
		}
	} else if sib := siblingMaster(raw); sib != "" {
		// One quality only: is there a master playlist next to it that
		// lists this one? Then offer all qualities.
		if st, b, err := m.fetchPlaylist(ctx, j, sib); err == nil && st < 400 {
			if mt, err := cleanPlaylist(b, sib); err == nil && strings.Contains(mt, "#EXT-X-STREAM-INF") {
				for _, q := range parseMaster(mt) {
					if samePlaylist(q.URL, raw) {
						look.Master2 = sib
						look.Qualities = parseMaster(mt)
						break
					}
				}
			}
		}
	}
	look.FirstPart = firstPiece(media)
	if look.FirstPart != "" && strings.HasPrefix(look.FirstPart, "http") {
		look.Hidden = m.pieceHidden(ctx, j, look.FirstPart)
	}
	return look, nil
}

// pieceHidden reads the first 16 KB of a piece.
func (m *Manager) pieceHidden(ctx context.Context, j *Job, raw string) bool {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	m.mu.Lock()
	h := j.browserHeaders()
	m.mu.Unlock()
	req, err := newRequest(ctx, http.MethodGet, raw, h)
	if err != nil {
		return false
	}
	req.Header.Set("Range", "bytes=0-16383")
	resp, err := m.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return false
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	return hiddenTS(b)
}

// playlistWithOtherReferers asks for the playlist with each other Referer
// after a 401/403; the first that works is kept (and remembered).
func (m *Manager) playlistWithOtherReferers(ctx context.Context, j *Job, raw string, status int, body []byte) (int, []byte, error) {
	m.mu.Lock()
	orig := j.Referer
	alts := append([]string(nil), j.RefAlts...)
	m.mu.Unlock()
	for _, ref := range alts {
		m.mu.Lock()
		j.Referer = ref
		m.mu.Unlock()
		st, b, err := m.fetchPlaylist(ctx, j, raw)
		if err == nil && st < 400 {
			m.mu.Lock()
			j.RefAlts = otherReferers(alts, ref, orig)
			m.mu.Unlock()
			return st, b, nil
		}
	}
	m.mu.Lock()
	j.Referer = orig
	m.mu.Unlock()
	return status, body, nil
}

// otherReferers: alts without picked, with the old Referer first.
func otherReferers(alts []string, picked, old string) []string {
	rest := []string{}
	if old != "" && old != picked {
		rest = append(rest, old)
	}
	for _, a := range alts {
		if a != picked && a != old {
			rest = append(rest, a)
		}
	}
	return rest
}

// qualitiesFromMaster: the window's quality list from the master playlist,
// for when yt-dlp gave no heights. The size is a guess from the bandwidth.
func qualitiesFromMaster(qs []hlsQuality, duration float64) []QualityOpt {
	var out []QualityOpt
	seen := map[int]bool{}
	for _, q := range qs {
		if q.Height <= 0 {
			continue
		}
		h := roundHeight(q.Height)
		if seen[h] {
			continue
		}
		seen[h] = true
		o := QualityOpt{Height: h, Label: fmt.Sprintf("%dp", h)}
		if duration > 0 && q.Bandwidth > 0 {
			o.Size = int64(float64(q.Bandwidth) / 8 * duration)
		}
		out = append(out, o)
	}
	return out
}

// addMasterSizes fills unknown sizes of yt-dlp's qualities from the
// master's bandwidths.
func addMasterSizes(vi *VideoInfo, qs []hlsQuality) {
	if vi == nil || vi.Duration <= 0 {
		return
	}
	est := map[int]int64{}
	for _, o := range qualitiesFromMaster(qs, vi.Duration) {
		est[o.Height] = o.Size
	}
	for k := range vi.Qualities {
		if vi.Qualities[k].Size == 0 {
			vi.Qualities[k].Size = est[vi.Qualities[k].Height]
		}
	}
}

// hiddenArgs: yt-dlp options for disguised pieces. ffmpeg reads the joined
// pieces of each part as MPEG-TS when it puts video and audio together.
func hiddenArgs() []string {
	return []string{
		"--ppa", "Merger+ffmpeg_i1:-f mpegts",
		"--ppa", "Merger+ffmpeg_i2:-f mpegts",
		// The fixes and conversions that would misread the picture are left
		// to BDDM (repairHidden).
		"--fixup", "never",
	}
}

// hiddenAudioMark: the name part of the file yt-dlp saves for an audio
// choice of such a stream (BDDM converts it to MP3/M4A, then deletes it).
const hiddenAudioMark = ".bddm-audio"

// fileHidden: the file starts with a picture (pieces still in disguise).
func fileHidden(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 16)
	n, _ := io.ReadFull(f, b)
	return imageMagic(b[:n])
}

// repairHidden turns a downloaded file whose pieces are still in disguise
// into a normal file: ffmpeg reads it as MPEG-TS and writes an MP4, or for
// audio choices an MP3/M4A. Returns the new path.
func repairHidden(ctx context.Context, ffmpeg, p, quality string) (string, error) {
	if ffmpeg == "" {
		return p, errors.New("ffmpeg is missing")
	}
	base := strings.TrimSuffix(strings.TrimSuffix(p, filepath.Ext(p)), hiddenAudioMark)
	var out string
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	if fileHidden(p) {
		args = append(args, "-f", "mpegts")
	}
	args = append(args, "-i", p)
	switch quality {
	case "mp3":
		out = base + ".mp3"
		args = append(args, "-vn", "-c:a", "libmp3lame", "-q:a", "0")
	case "m4a":
		out = base + ".m4a"
		args = append(args, "-vn", "-c:a", "copy", "-movflags", "+faststart")
	default:
		out = base + ".mp4"
		args = append(args, "-map", "0:v?", "-map", "0:a?", "-c", "copy", "-movflags", "+faststart")
	}
	tmp := base + ".bddm-fix" + filepath.Ext(out)
	args = append(args, tmp)
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	prepareProc(cmd)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		return p, fmt.Errorf("ffmpeg could not fix the video: %s", firstNonEmpty(strings.TrimSpace(stderr.String()), err.Error()))
	}
	if out == p {
		if err := os.Remove(p); err != nil {
			_ = os.Remove(tmp)
			return p, err
		}
	}
	if err := os.Rename(tmp, out); err != nil {
		return p, err
	}
	if out != p {
		_ = os.Remove(p)
	}
	return out, nil
}
