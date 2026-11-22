package main

// video.go: video downloads with yt-dlp (YouTube, Facebook, Instagram,
// TikTok, HLS .m3u8, DASH .mpd and the ~1800 other sites yt-dlp supports).
//
// Flow: a video link becomes a Pending job, BDDM reads the video's details
// with "yt-dlp -J" and shows the video options window (ui/video.html). When
// the user presses Start, the job runs yt-dlp with the chosen quality; its
// progress lines are shown in the Downloads list. Pause stops yt-dlp, and
// Resume runs it again (it continues its .part files).

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// VideoOptions are the choices from the video options window (saved).
type VideoOptions struct {
	Quality  string `json:"quality"` // "best", "1080", ..., "mp3", "m4a"
	Thumb    bool   `json:"thumb,omitempty"`
	Subs     bool   `json:"subs,omitempty"`
	SubLang  string `json:"subLang,omitempty"`
	Playlist bool   `json:"playlist,omitempty"`
	Title    string `json:"title,omitempty"`
	// Suffix is added to the file name (" (2)") when an earlier download of
	// the same video is already in the folder, so it is not skipped.
	Suffix string `json:"suffix,omitempty"`
}

type videoPending struct {
	state    string // "tools" (waiting for yt-dlp/ffmpeg), "loading", "ready", "error"
	err      string
	info     *VideoInfo
	playlist *PlaylistInfo
	dupName  string
	title    string // page title from the extension, until yt-dlp answers
}

type VideoInfo struct {
	VideoID   string         `json:"videoId,omitempty"`
	Title     string         `json:"title"`
	Uploader  string         `json:"uploader,omitempty"`
	Site      string         `json:"site,omitempty"`
	Duration  float64        `json:"duration,omitempty"`
	Thumbnail string         `json:"thumbnail,omitempty"`
	IsLive    bool           `json:"isLive,omitempty"`
	Qualities []QualityOpt   `json:"qualities"`
	AudioSize int64          `json:"audioSize,omitempty"`
	Subtitles []SubtitleLang `json:"subtitles"`
	NoVideo   bool           `json:"noVideo,omitempty"` // only a playlist, no single video
}

type QualityOpt struct {
	Height int    `json:"height"`
	Label  string `json:"label"`
	Size   int64  `json:"size,omitempty"` // estimate, 0 = unknown
}

type SubtitleLang struct {
	Code string `json:"code"`
	Name string `json:"name"`
	Auto bool   `json:"auto,omitempty"`
}

type PlaylistInfo struct {
	Title string `json:"title"`
	Count int    `json:"count"`
}

// VideoView is what the video options window shows.
type VideoView struct {
	ID        string        `json:"id"`
	URL       string        `json:"url"`
	State     string        `json:"state"`
	Error     string        `json:"error,omitempty"`
	Title     string        `json:"title,omitempty"`
	Info      *VideoInfo    `json:"info,omitempty"`
	Playlist  *PlaylistInfo `json:"playlist,omitempty"`
	Dir       string        `json:"dir"`
	Duplicate string        `json:"duplicate,omitempty"`
	Tools     ToolsStatus   `json:"tools"`
}

var standardHeights = []int{4320, 2160, 1440, 1080, 720, 480, 360, 240, 144}

// ---------------------------------------------------------------------------
// Which links are videos
// ---------------------------------------------------------------------------

var videoPathRules = map[string]*regexp.Regexp{
	"youtube.com":     regexp.MustCompile(`^/(watch|shorts/|live/|playlist|embed/|v/)`),
	"youtu.be":        regexp.MustCompile(`^/[\w-]{6,}`),
	"facebook.com":    regexp.MustCompile(`^/(watch|reel/|share/[vr]/|[^/]+/videos/|video\.php|videos/)`),
	"fb.watch":        regexp.MustCompile(`^/`),
	"instagram.com":   regexp.MustCompile(`^/(p|reel|reels|tv)/`),
	"tiktok.com":      regexp.MustCompile(`^/(@[^/]+/video/|t/|v/)`),
	"vm.tiktok.com":   regexp.MustCompile(`^/`),
	"vt.tiktok.com":   regexp.MustCompile(`^/`),
	"vimeo.com":       regexp.MustCompile(`^/(\d+|channels/|showcase/)`),
	"dailymotion.com": regexp.MustCompile(`^/video/`),
	"dai.ly":          regexp.MustCompile(`^/`),
	"x.com":           regexp.MustCompile(`^/[^/]+/status/\d+`),
	"twitter.com":     regexp.MustCompile(`^/[^/]+/status/\d+`),
	"twitch.tv":       regexp.MustCompile(`^/(videos/|[^/]+/clip/)`),
	"clips.twitch.tv": regexp.MustCompile(`^/`),
	"reddit.com":      regexp.MustCompile(`^/r/[^/]+/comments/`),
	"bilibili.com":    regexp.MustCompile(`^/video/`),
	"soundcloud.com":  regexp.MustCompile(`^/[^/]+/[^/]+`),
}

func siteHost(host string) string {
	host = strings.ToLower(strings.TrimPrefix(host, "www."))
	host = strings.TrimPrefix(host, "m.")
	host = strings.TrimPrefix(host, "music.")
	host = strings.TrimPrefix(host, "mobile.")
	host = strings.TrimPrefix(host, "web.")
	return host
}

// isStreamURL: a direct HLS (.m3u8) or DASH (.mpd) playlist.
func isStreamURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	p := strings.ToLower(u.Path)
	return strings.HasSuffix(p, ".m3u8") || strings.HasSuffix(p, ".mpd")
}

// isVideoPage reports whether a link is a video page of a known site.
func isVideoPage(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	re, ok := videoPathRules[siteHost(u.Hostname())]
	return ok && re.MatchString(u.Path)
}

func isYouTube(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := siteHost(u.Hostname())
	return h == "youtube.com" || h == "youtu.be" || strings.HasSuffix(h, ".youtube.com")
}

// hasPlaylist: a YouTube link with list=..., or a playlist page.
func hasPlaylist(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || !isYouTube(raw) {
		return false
	}
	return u.Query().Get("list") != "" || strings.HasPrefix(u.Path, "/playlist")
}

// ---------------------------------------------------------------------------
// Pending video jobs and the options window
// ---------------------------------------------------------------------------

// AddVideoPending adds a video that waits for the options window.
func (m *Manager) AddVideoPending(r AddRequest) (JobView, error) {
	r.URL = strings.TrimSpace(r.URL)
	u, err := url.Parse(r.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return JobView{}, errors.New("Please enter a valid http:// or https:// link.")
	}
	vp := &videoPending{state: "loading", title: strings.TrimSpace(r.Title)}
	if d := m.FindDone(r.URL); d != nil {
		// Only warn when the earlier file is still there.
		if _, err := os.Stat(filepath.Join(d.Dir, d.Name)); err == nil {
			vp.dupName = d.Name
		}
	}
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return JobView{}, errors.New("BDDM is shutting down")
	}
	j := &Job{
		ID: newID(), URL: r.URL, Referer: strings.TrimSpace(r.Referer), Cookie: strings.TrimSpace(r.Cookie),
		Stream: streamKind(r.Stream), Name: firstNonEmpty(vp.title, "Video"), Dir: m.settings.dirForCategory("Video"), Parts: m.settings.Parts,
		Status: StatusPending, Started: time.Now(), Kind: "video", vpend: vp,
	}
	j.applyBrowser(r)
	if isStreamJob(j) {
		j.refFrame = firstNonEmpty(r.Frame, r.Referer)
		j.refPage = r.Page
		if captured(r.Headers, "Referer") == "" {
			j.useRememberedReferer(j.refFrame, j.refPage)
		}
	}
	if !m.tools.Ready() {
		vp.state = "tools"
	}
	m.jobs = append(m.jobs, j)
	v := j.viewLocked()
	m.mu.Unlock()

	m.tools.OnReady(func() { m.probeVideo(j) })
	go func() {
		time.Sleep(pendingTimeout)
		_ = m.CancelPending(j.ID)
	}()
	return v, nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func (m *Manager) VideoPending(id string) (VideoView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.findLocked(id)
	if j == nil || j.Status != StatusPending || j.vpend == nil {
		return VideoView{}, errors.New("This video is no longer waiting (it was started or cancelled).")
	}
	vp := j.vpend
	v := VideoView{ID: j.ID, URL: j.URL, State: vp.state, Error: vp.err, Title: vp.title, Info: vp.info,
		Playlist: vp.playlist, Dir: j.Dir, Duplicate: vp.dupName}
	if vp.state == "tools" && m.tools.Ready() {
		v.State = "loading"
	}
	m.mu.Unlock()
	v.Tools = m.tools.Status()
	m.mu.Lock()
	return v, nil
}

// RetryVideoInfo asks yt-dlp again (after an error).
func (m *Manager) RetryVideoInfo(id string) error {
	m.mu.Lock()
	j := m.findLocked(id)
	if j == nil || j.vpend == nil {
		m.mu.Unlock()
		return errors.New("This video is no longer waiting.")
	}
	j.vpend.state, j.vpend.err = "loading", ""
	m.mu.Unlock()
	m.tools.OnReady(func() { m.probeVideo(j) })
	return nil
}

// StartVideo starts (or adds paused) a pending video with the chosen options.
func (m *Manager) StartVideo(id string, opt VideoOptions, dir string, later bool) (JobView, error) {
	dir = strings.TrimSpace(dir)
	if dir != "" {
		dir = filepath.Clean(dir)
		if !filepath.IsAbs(dir) {
			return JobView{}, errors.New("Please choose a full folder path, for example D:\\Videos.")
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return JobView{}, errors.New("Cannot use this folder: " + err.Error())
		}
	}
	if !m.tools.Ready() {
		return JobView{}, errors.New("The video tools are not installed yet.")
	}
	q := opt.Quality
	if q != "best" && q != "mp3" && q != "m4a" {
		if _, err := strconv.Atoi(q); err != nil {
			opt.Quality = "best"
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.findLocked(id)
	if j == nil || j.Status != StatusPending || j.vpend == nil {
		return JobView{}, errors.New("This video is no longer waiting (it was started or cancelled).")
	}
	vp := j.vpend
	title := vp.title
	if vp.info != nil && vp.info.Title != "" {
		title = vp.info.Title
	}
	if opt.Playlist && vp.playlist != nil && vp.playlist.Title != "" {
		title = vp.playlist.Title
	}
	opt.Title = title
	// Videos and music have their own folders (Settings, file types).
	cat := "Video"
	if q == "mp3" || q == "m4a" {
		cat = "Audio"
	}
	videoDir := m.settings.dirForCategory("Video")
	switch {
	case dir == "" || (cat == "Audio" && filepath.Clean(dir) == filepath.Clean(videoDir)):
		j.Dir = m.settings.dirForCategory(cat) // not changed in the window
	default:
		j.Dir = dir
		m.rememberDirLocked(cat, dir)
	}
	j.Name = firstNonEmpty(cleanTitle(title), sanitizeName(title), "Video")
	if !opt.Playlist {
		switch opt.Quality {
		case "mp3":
			j.Name += ".mp3"
		case "m4a":
			j.Name += ".m4a"
		default:
			j.Name += ".mp4"
		}
	}
	if !opt.Playlist && isStreamJob(j) && strings.TrimSpace(opt.Title) != "" {
		opt.Suffix = freeVideoSuffix(j.Dir, globEscape(streamOutName(opt.Title)), opt.Quality)
	} else if !opt.Playlist && vp.info != nil && vp.info.VideoID != "" {
		opt.Suffix = freeVideoSuffix(j.Dir, "*"+globEscape("["+vp.info.VideoID+"]"), opt.Quality)
	}
	j.Video = &opt
	j.vpend = nil
	j.Size = 0
	j.Started = time.Now()
	if later {
		j.Status = StatusPaused
	} else {
		j.Status = StatusQueued
		m.scheduleLocked()
	}
	m.saveHistoryLocked()
	return j.viewLocked(), nil
}

// ---------------------------------------------------------------------------
// Reading video details (yt-dlp -J)
// ---------------------------------------------------------------------------

// baseArgs: the yt-dlp options every run of job j gets. done deletes the
// temporary cookie file (call it when yt-dlp has finished).
func (m *Manager) baseArgs(j *Job) (args []string, done func()) {
	done = func() {}
	args = []string{"--no-warnings", "--no-colors", "--ignore-config", "--no-cache-dir"}
	if deno := m.tools.Find("deno"); deno != "" {
		args = append(args, "--js-runtimes", "deno:"+deno)
	}
	if ff := m.tools.Find("ffmpeg"); ff != "" {
		args = append(args, "--ffmpeg-location", filepath.Dir(ff))
	}
	if addr := m.yieldProxyAddr(); addr != "" {
		args = append(args, "--proxy", "http://"+addr)
	}
	if j.localPL != "" {
		args = append(args, "--enable-file-urls")
	}
	browser, _ := cookiesBrowser.Load().(string)
	if isYouTube(j.URL) {
		// YouTube: let yt-dlp pick its clients and headers itself (passing
		// browser cookies to YouTube makes it use clients that need extra
		// tokens), unless the user chose to share a browser's cookies.
		if browser != "" {
			args = append(args, "--cookies-from-browser", browser)
		}
		return args, done
	}
	stream := isStreamJob(j)
	// A stream (or a link the extension sent with the browser's details) is
	// fetched like the site's own player does: the browser's user agent,
	// the player's page as Referer, its Origin and the player's headers.
	direct := stream || !isVideoPage(j.URL)
	if j.UA != "" || stream {
		args = append(args, "--user-agent", firstNonEmpty(j.UA, userAgent))
	}
	if j.Referer != "" {
		args = append(args, "--referer", j.Referer)
	}
	origin := captured(j.extra, "Origin")
	if origin == "" && stream {
		origin = originOf(j.Referer)
	}
	if origin != "" {
		args = append(args, "--add-header", "Origin:"+origin)
	}
	if direct {
		keys := make([]string, 0, len(j.extra))
		for k := range j.extra {
			if !strings.EqualFold(k, "Origin") && !strings.EqualFold(k, "Accept") {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, "--add-header", k+":"+j.extra[k])
		}
	}
	if browser != "" {
		args = append(args, "--cookies-from-browser", browser)
	}
	// The browser's cookies of every host (page, playlist, CDN) in a
	// temporary cookies.txt: yt-dlp gives each host its own cookies.
	path, remove, err := writeCookieFile("", j.URL, j.Cookie, j.jar)
	if err != nil {
		log.Printf("[%s] could not write the cookie file for yt-dlp: %v", j.Name, err)
	} else if path != "" {
		args = append(args, "--cookies", path)
		done = remove
	}
	return args, done
}

// isStreamJob: the job is a direct HLS/DASH playlist link.
func isStreamJob(j *Job) bool {
	return j.Stream != "" || isStreamURL(j.URL)
}

func streamKind(s string) string {
	switch s = strings.ToLower(strings.TrimSpace(s)); s {
	case "hls", "dash":
		return s
	}
	return ""
}

// originOf returns "https://host[:port]" of a web link, or "".
func originOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// inputURL is what yt-dlp is given: the link, or the cleaned local copy of
// the playlist when the site's own copy needed fixing.
func inputURL(j *Job) string {
	if j.localPL != "" {
		return fileURL(j.localPL)
	}
	return j.URL
}

func (m *Manager) ytdlpCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, m.tools.Find("yt-dlp"), args...)
	prepareProc(cmd)
	// Loopback is not proxied: test servers and local playlists stay
	// direct, and a dropped local connection still looks like a network
	// error to yt-dlp. Real sites go through the yield proxy.
	cmd.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8",
		"NO_PROXY=127.0.0.1,localhost,::1", "no_proxy=127.0.0.1,localhost,::1")
	cmd.Cancel = func() error { killTree(cmd); return nil }
	cmd.WaitDelay = 10 * time.Second
	return cmd
}

// runJSON runs yt-dlp -J and decodes its answer.
func (m *Manager) runJSON(j *Job, extra ...string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args, done := m.baseArgs(j)
	defer done()
	args = append(args, "-J")
	args = append(args, extra...)
	args = append(args, "--", inputURL(j))
	cmd := m.ytdlpCommand(ctx, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := lastErrorLine(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	var data map[string]any
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, errors.New("yt-dlp gave an answer BDDM could not read")
	}
	return data, nil
}

func lastErrorLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.HasPrefix(l, "ERROR:") {
			l = strings.TrimSpace(strings.TrimPrefix(l, "ERROR:"))
			l = regexp.MustCompile(`^\[[^\]]+\] [\w-]+: `).ReplaceAllString(l, "")
			return l
		}
	}
	if len(lines) > 0 {
		return strings.TrimSpace(lines[len(lines)-1])
	}
	return ""
}

func (m *Manager) probeVideo(j *Job) {
	m.mu.Lock()
	if j.vpend == nil {
		m.mu.Unlock()
		return
	}
	j.vpend.state, j.vpend.err = "loading", ""
	playlistToo := hasPlaylist(j.URL)
	m.mu.Unlock()

	// HLS: read the playlist ourselves first (qualities, a master next to a
	// single-quality link, pieces hidden behind a picture; disguise.go).
	var look *hlsLook
	if isHLSJob(j) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		l, err := m.lookAtHLS(ctx, j)
		cancel()
		if err == nil {
			look = l
			m.mu.Lock()
			if l.Master2 != "" && !l.Master {
				log.Printf("[%s] using the master playlist %s (all qualities)", j.Name, l.Master2)
				j.URL = l.Master2
			}
			j.TSFix = l.Hidden
			kind := refererKind(j.Referer, j.refFrame, j.refPage)
			m.mu.Unlock()
			refMem.remember(hostOfLink(j.URL), kind)
			if l.Hidden {
				log.Printf("[%s] the stream's pieces are video data behind a picture; ffmpeg will read them as MPEG-TS", j.Name)
			}
		} else {
			log.Printf("[%s] reading the playlist: %v", j.Name, err)
		}
	}

	var wg sync.WaitGroup
	var info *VideoInfo
	var pl, pl2 *PlaylistInfo
	var infoErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		data, err := m.runJSON(j, "--no-playlist")
		if err != nil && isStreamJob(j) && isRefused(err.Error()) {
			// 403: try the other Referers a player could send (referer.go).
			data, err = m.retryReferers(j, err)
		}
		if err != nil {
			infoErr = err
			return
		}
		if isStreamJob(j) {
			m.mu.Lock()
			kind := refererKind(j.Referer, j.refFrame, j.refPage)
			m.mu.Unlock()
			refMem.remember(hostOfLink(j.URL), kind)
		}
		info = parseVideoInfo(data)
		if t, _ := data["_type"].(string); t == "playlist" {
			// The link is only a playlist (no single video in it).
			info.NoVideo = true
			pl = &PlaylistInfo{Title: str(data, "title"), Count: playlistCount(data)}
		}
	}()
	if playlistToo {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, err := m.runJSON(j, "--yes-playlist", "--flat-playlist")
			if err == nil {
				if t, _ := data["_type"].(string); t == "playlist" {
					pl2 = &PlaylistInfo{Title: str(data, "title"), Count: playlistCount(data)}
				}
			}
		}()
	}
	wg.Wait()
	if pl == nil {
		pl = pl2
	}
	if infoErr != nil && pl == nil && isHLSJob(j) && wantsPlaylistFix(infoErr.Error()) {
		// yt-dlp could not read the playlist as the site sends it: clean a
		// copy ourselves and try again with that (see playlist.go).
		if data, err := m.retryWithCleanPlaylist(j); err != nil {
			infoErr = err
		} else {
			infoErr = nil
			info = parseVideoInfo(data)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if j.vpend == nil {
		return
	}
	if infoErr != nil && pl == nil {
		j.vpend.state = "error"
		j.vpend.err = friendlyJobError(j, infoErr.Error())
		return
	}
	if info == nil {
		info = &VideoInfo{Title: pl.Title, NoVideo: true}
	}
	if look != nil && len(look.Qualities) > 0 && !info.NoVideo {
		if len(info.Qualities) == 0 {
			info.Qualities = qualitiesFromMaster(look.Qualities, info.Duration)
		} else {
			addMasterSizes(info, look.Qualities)
		}
	}
	if info.NoVideo && len(info.Qualities) == 0 {
		for _, h := range []int{2160, 1440, 1080, 720, 480, 360} {
			info.Qualities = append(info.Qualities, QualityOpt{Height: h, Label: fmt.Sprintf("%dp", h)})
		}
	}
	if isStreamJob(j) && j.vpend.title != "" {
		// A stream link's own title is just "master" or "index".
		info.Title = j.vpend.title
	}
	j.vpend.info = info
	j.vpend.playlist = pl
	j.vpend.state = "ready"
	if info.Title != "" && !isStreamJob(j) {
		j.Name = info.Title
	}
}

// retryReferers asks yt-dlp again with each other Referer (after a 403).
// The first one the site does not refuse is kept.
func (m *Manager) retryReferers(j *Job, first error) (map[string]any, error) {
	m.mu.Lock()
	orig := j.Referer
	alts := append([]string(nil), j.RefAlts...)
	m.mu.Unlock()
	for _, ref := range alts {
		m.mu.Lock()
		j.Referer = ref
		m.mu.Unlock()
		data, err := m.runJSON(j, "--no-playlist")
		if err == nil || !isRefused(err.Error()) {
			// Not refused any more (even if something else is wrong):
			// keep this Referer, the others become the alternatives.
			m.mu.Lock()
			j.RefAlts = otherReferers(alts, ref, orig)
			m.mu.Unlock()
			log.Printf("[%s] the stream works with Referer %s", j.Name, ref)
			return data, err
		}
	}
	m.mu.Lock()
	j.Referer = orig
	m.mu.Unlock()
	return nil, first
}

// nextReferer switches a stream download to its next other Referer after
// a 403 (each is tried once per run). Reports whether there was one.
func (m *Manager) nextReferer(j *Job) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !isStreamJob(j) || len(j.RefAlts) == 0 {
		return false
	}
	if j.refTried == nil {
		j.refTried = map[string]bool{j.Referer: true}
	}
	for _, a := range j.RefAlts {
		if !j.refTried[a] {
			j.refTried[a] = true
			old := j.Referer
			j.Referer = a
			var rest []string
			for _, b := range j.RefAlts {
				if b != a {
					rest = append(rest, b)
				}
			}
			if old != "" {
				rest = append([]string{old}, rest...)
			}
			j.RefAlts = rest
			return true
		}
	}
	return false
}

// isHLSJob: a stream job whose playlist is HLS (.m3u8).
func isHLSJob(j *Job) bool {
	if j.Stream == "hls" {
		return true
	}
	u, err := url.Parse(j.URL)
	return err == nil && strings.HasSuffix(strings.ToLower(u.Path), ".m3u8")
}

// retryWithCleanPlaylist makes a cleaned local copy of the playlist and asks
// yt-dlp again. Its errors are already plain English.
func (m *Manager) retryWithCleanPlaylist(j *Job) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p, err := m.localizePlaylist(ctx, j)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	j.localPL = p
	j.FixPlaylist = true
	m.mu.Unlock()
	data, err := m.runJSON(j, "--no-playlist")
	if err != nil {
		return nil, errors.New(friendlyJobError(j, err.Error()))
	}
	return data, nil
}

var httpDeniedRe = regexp.MustCompile(`(?i)http error (401|403|404|410)`)

// friendlyJobError: like friendlyVideoError, and for stream links a refused
// link most likely means it expired.
func friendlyJobError(j *Job, msg string) string {
	if m := httpDeniedRe.FindStringSubmatch(msg); m != nil && (m[1] == "401" || m[1] == "403") {
		return msgStreamRefused // explainError adds what to do
	}
	if isStreamJob(j) && httpDeniedRe.MatchString(msg) {
		return msgExpiredStream
	}
	return friendlyVideoError(msg)
}

var reportTailRe = regexp.MustCompile(`(?is)[;.]?\s*please report this issue on.*$`)

func friendlyVideoError(msg string) string {
	reported := reportTailRe.MatchString(msg)
	msg = strings.TrimSpace(reportTailRe.ReplaceAllString(msg, ""))
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "no m3u header"):
		return msgNotPlaylist
	case strings.Contains(low, "unsupported url"):
		return "No video was found at this link."
	case strings.Contains(low, "sign in to confirm"):
		if b, _ := cookiesBrowser.Load().(string); b != "" {
			return "YouTube still asks to confirm you are not a bot. Open YouTube in " + browserName(b) + ", make sure you are signed in, close that browser fully, then press Try again."
		}
		return "YouTube asks to confirm you are not a bot. Fix: in BDDM Settings > Video tools, set \"Use cookies from\" to Firefox (be signed in to YouTube there), then press Try again."
	case strings.Contains(low, "private video"):
		return "This video is private."
	case strings.Contains(low, "login") || strings.Contains(low, "log in") || strings.Contains(low, "cookies"):
		return "This site needs you to be logged in. Sign in there in your browser, then in BDDM Settings > Video tools set \"Use cookies from\" to that browser and try again."
	}
	if reported {
		msg = strings.TrimRight(msg, ". ") + ". yt-dlp could not handle this video. It updates itself once a day, so trying again later may help."
	}
	return msg
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func num(m map[string]any, k string) float64 {
	f, _ := m[k].(float64)
	return f
}

func playlistCount(data map[string]any) int {
	if n := int(num(data, "playlist_count")); n > 0 {
		return n
	}
	if e, ok := data["entries"].([]any); ok {
		return len(e)
	}
	return 0
}

// parseVideoInfo turns yt-dlp's JSON into the choices for the window.
func parseVideoInfo(d map[string]any) *VideoInfo {
	vi := &VideoInfo{
		VideoID: str(d, "id"), Title: str(d, "title"), Uploader: firstNonEmpty(str(d, "uploader"), str(d, "channel")),
		Site: str(d, "extractor_key"), Duration: num(d, "duration"), Thumbnail: str(d, "thumbnail"),
	}
	if live, _ := d["is_live"].(bool); live {
		vi.IsLive = true
	}
	fmts, _ := d["formats"].([]any)
	bestVideo := map[int]int64{} // height -> size of the biggest video stream
	var bestAudio int64
	haveHeight := map[int]bool{}
	for _, fi := range fmts {
		f, ok := fi.(map[string]any)
		if !ok {
			continue
		}
		size := int64(num(f, "filesize"))
		if size <= 0 {
			size = int64(num(f, "filesize_approx"))
		}
		vc, ac := str(f, "vcodec"), str(f, "acodec")
		h := int(num(f, "height"))
		if vc != "none" && h > 0 {
			std := roundHeight(h)
			haveHeight[std] = true
			if size > bestVideo[std] {
				bestVideo[std] = size
			}
		} else if vc == "none" && ac != "none" && ac != "" && size > bestAudio {
			bestAudio = size
		}
	}
	vi.AudioSize = bestAudio
	var heights []int
	for h := range haveHeight {
		heights = append(heights, h)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(heights)))
	for _, h := range heights {
		q := QualityOpt{Height: h, Label: fmt.Sprintf("%dp", h)}
		if bestVideo[h] > 0 {
			q.Size = bestVideo[h] + bestAudio
		}
		vi.Qualities = append(vi.Qualities, q)
	}
	vi.Subtitles = parseSubs(d)
	return vi
}

// roundHeight maps odd heights (e.g. 1036, 718) to the usual names.
func roundHeight(h int) int {
	for _, s := range standardHeights {
		if h >= s*9/10 {
			return s
		}
	}
	return h
}

func parseSubs(d map[string]any) []SubtitleLang {
	out := []SubtitleLang{}
	seen := map[string]bool{}
	add := func(key string, auto bool) {
		subs, _ := d[key].(map[string]any)
		codes := make([]string, 0, len(subs))
		for c := range subs {
			codes = append(codes, c)
		}
		sort.Strings(codes)
		for _, code := range codes {
			if code == "live_chat" || seen[code] {
				continue
			}
			// Automatic captions come translated into 100+ languages; keep
			// only the original ones.
			if auto && !strings.HasSuffix(code, "-orig") {
				continue
			}
			name := code
			if list, ok := subs[code].([]any); ok && len(list) > 0 {
				if e, ok := list[0].(map[string]any); ok && str(e, "name") != "" {
					name = str(e, "name")
				}
			}
			seen[code] = true
			out = append(out, SubtitleLang{Code: code, Name: name, Auto: auto})
		}
	}
	add("subtitles", false)
	add("automatic_captions", true)
	return out
}

// ---------------------------------------------------------------------------
// Running yt-dlp
// ---------------------------------------------------------------------------

const progressPrefix = "BDDMP|"
const filePrefix = "BDDMF|"
const sizePrefix = "BDDMS|"

// videoArgs: the yt-dlp options of a download run; done deletes the
// temporary cookie file once yt-dlp has finished.
func (m *Manager) videoArgs(j *Job) (args []string, done func()) {
	opt := j.Video
	if opt == nil {
		opt = &VideoOptions{Quality: "best"}
	}
	parts := j.Parts
	if parts < 1 {
		parts = defaultParts
	}
	args, done = m.baseArgs(j)
	args = append(args,
		"--newline", "--progress",
		"--progress-template", "download:"+progressPrefix+"%(progress.status)s|%(progress.downloaded_bytes)s|%(progress.total_bytes)s|%(progress.total_bytes_estimate)s|%(progress.speed)s|%(info.playlist_index)s|%(info.n_entries)s|%(progress.filename)s",
		"--print", "after_move:"+filePrefix+"%(filepath)s",
		// The size of everything this video needs (video + audio), so the
		// total does not jump when yt-dlp moves from the video to the audio.
		"--print", "before_dl:"+sizePrefix+"%(filesize,filesize_approx)s|%(requested_formats.0.filesize,requested_formats.0.filesize_approx)s|%(requested_formats.1.filesize,requested_formats.1.filesize_approx)s",
		"--continue", "--no-mtime", "--no-overwrites",
		// A failed extra (subtitles, thumbnail) or one broken playlist video
		// must not stop the rest; real failures still give no file.
		"--ignore-errors",
		// Speed: several fragments of DASH/HLS streams at once; re-ask the
		// site for fresh links if a connection gets throttled.
		"-N", strconv.Itoa(parts),
		"--throttled-rate", "100K",
		"--retries", "10", "--fragment-retries", "10", "--extractor-retries", "3",
		"--file-access-retries", "10",
		"-P", j.Dir,
	)
	if !j.skipFrags {
		// A stream piece that fails (no internet) stops yt-dlp instead of
		// leaving a gap in the video; BDDM then continues the download.
		args = append(args, "--abort-on-unavailable-fragments")
	}
	args = append(args, outputArgs(j, opt)...)
	audioOnly := opt.Quality == "mp3" || opt.Quality == "m4a"
	if j.TSFix {
		// Pieces behind a picture (disguise.go): ffmpeg must read them as
		// MPEG-TS; audio is converted by BDDM after the download, from a
		// file of its own name (never an earlier video of the same title).
		args = append(args, hiddenArgs()...)
		if audioOnly {
			for k := len(args) - 1; k > 0; k-- {
				if args[k-1] == "-o" {
					args[k] = strings.TrimSuffix(args[k], ".%(ext)s") + hiddenAudioMark + ".%(ext)s"
					break
				}
			}
			args = append(args, "-f", "ba/b")
		} else {
			args = append(args, formatArgs(opt)...)
		}
	} else {
		args = append(args, formatArgs(opt)...)
	}
	if !audioOnly && isStreamJob(j) && !j.TSFix {
		args = append(args, "--remux-video", "mp4")
	}
	if opt.Thumb {
		args = append(args, "--write-thumbnail", "--convert-thumbnails", "jpg")
		if opt.Quality == "mp3" || opt.Quality == "m4a" {
			args = append(args, "--embed-thumbnail")
		}
	}
	if opt.Subs && opt.SubLang != "" {
		args = append(args, "--write-subs", "--write-auto-subs", "--sub-langs", opt.SubLang, "--convert-subs", "srt")
	}
	return append(args, "--", inputURL(j)), done
}

// outputArgs: where yt-dlp saves the file and how it names it.
func outputArgs(j *Job, opt *VideoOptions) []string {
	var args []string
	if runtime.GOOS == "windows" {
		args = append(args, "--windows-filenames")
	}
	if opt.Playlist {
		args = append(args, "--yes-playlist", "-o", "%(playlist_title,playlist_id|Playlist).100B/%(playlist_index)03d - %(title).120B.%(ext)s")
	} else if isStreamJob(j) && strings.TrimSpace(opt.Title) != "" {
		// A bare stream link has no title of its own ("master", "index"),
		// so use the page title.
		name := strings.ReplaceAll(streamOutName(opt.Title), "%", "%%")
		args = append(args, "--no-playlist", "-o", name+outSuffix(opt)+".%(ext)s")
	} else {
		args = append(args, "--no-playlist", "-o", "%(title).150B [%(id)s]"+outSuffix(opt)+".%(ext)s")
	}
	return args
}

// formatArgs: which streams yt-dlp picks for the chosen quality.
func formatArgs(opt *VideoOptions) []string {
	switch opt.Quality {
	case "mp3":
		return []string{"-f", "ba/b", "-x", "--audio-format", "mp3", "--audio-quality", "0"}
	case "m4a":
		return []string{"-f", "ba[ext=m4a]/ba/b", "-x", "--audio-format", "m4a"}
	case "best", "":
		return []string{"-f", "bv*+ba/b", "-S", "res,fps,vcodec:h264,acodec:m4a", "--merge-output-format", "mp4"}
	}
	h := opt.Quality
	return []string{"-f", "bv*[height<=" + h + "]+ba/b[height<=" + h + "]/bv*+ba/b",
		"-S", "res:" + h + ",fps,vcodec:h264,acodec:m4a", "--merge-output-format", "mp4"}
}

// expectedSize reads "size|video size|audio size" (NA = unknown) and
// returns the best known total.
func expectedSize(s string) int64 {
	f := strings.Split(s, "|")
	var n, parts int64
	if len(f) > 0 {
		n = atoi64(f[0])
	}
	ok := len(f) >= 3
	for _, x := range f[1:] {
		v := atoi64(x)
		if v <= 0 {
			ok = false
		}
		parts += v
	}
	if ok && parts > n {
		n = parts
	}
	return n
}

type streamProg struct {
	done, total int64
	nth         int
	counted     bool
}

// runVideo downloads a video. yt-dlp keeps what it downloaded (.part files,
// and for HLS/DASH streams the finished pieces and a .ytdl progress file),
// so running it again continues where it stopped: after Pause, a crash, or
// a lost connection. When the connection drops, BDDM waits until it is back
// and continues by itself (A13).
func (m *Manager) runVideo(ctx context.Context, j *Job) error {
	defer func() {
		m.mu.Lock()
		j.skipFrags = false
		m.mu.Unlock()
	}()
	for attempt := 1; ; attempt++ {
		err := m.runVideoOnce(ctx, j)
		if err == nil || ctx.Err() != nil {
			return err
		}
		if isRefused(err.Error()) && m.nextReferer(j) {
			j.setMessage("The site refused the stream (403); trying another Referer...")
			continue
		}
		frag := isFragmentFail(err)
		if !isNetErr(err) && !frag {
			return err
		}
		waited, werr := waitOnline(ctx, j, serverAddr(j.URL))
		if werr != nil {
			return werr
		}
		if waited {
			continue // the connection is back: continue the download
		}
		if attempt >= videoNetTries {
			if frag && !j.skipFrags {
				// Some pieces of the stream really are missing on the
				// site: finish without them (as most players would).
				m.mu.Lock()
				j.skipFrags = true
				m.mu.Unlock()
				log.Printf("[%s] some stream pieces keep failing; finishing without them", j.Name)
				continue
			}
			return err
		}
		wait := backoff(attempt)
		j.setMessage("Connection problem (%s); continuing in %s...", shortReason(err), wait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// dropPartialFragments removes stream pieces that were only half downloaded
// when yt-dlp was stopped ("video.mp4.part-Frag7.part"). yt-dlp would ask
// the server for the rest of such a piece, and servers that answer that
// with an error (HTTP 416) make it give up the piece. Whole pieces
// ("video.mp4.part-Frag6") and the .ytdl progress file are kept, so the
// download continues where it stopped.
func dropPartialFragments(files []string) {
	for _, f := range files {
		matches, _ := filepath.Glob(globEscape(f) + ".part-Frag*.part")
		for _, p := range matches {
			_ = os.Remove(p)
		}
	}
}

// videoNetTries: runs of yt-dlp after connection problems while online.
var videoNetTries = 3

// isFragmentFail: yt-dlp stopped because a piece of an HLS/DASH stream
// could not be downloaded.
func isFragmentFail(err error) bool {
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "fragment") && (strings.Contains(low, "unable to continue") ||
		strings.Contains(low, "giving up") || strings.Contains(low, "not found"))
}

func (m *Manager) runVideoOnce(ctx context.Context, j *Job) (rerr error) {
	if !m.tools.Ready() {
		return errors.New("the video tools (yt-dlp and ffmpeg) are missing; open Settings to install them")
	}
	m.mu.Lock()
	fix := j.FixPlaylist
	fast := m.useFastYouTubeLocked(j)
	m.mu.Unlock()
	if fast {
		err := m.runFastYouTube(ctx, j)
		if err == nil || ctx.Err() != nil {
			return err
		}
		if isNetworkErr(err) {
			return err // no internet: yt-dlp would fail too; Resume continues
		}
		why := m.fastFailed(j, err)
		j.setMessage("Using yt-dlp: %s", why)
		defer func() {
			if rerr == nil {
				m.dropFastFiles(j)
			}
		}()
	}
	if fix {
		// Clean a fresh copy of the site's playlist (the temp copy may be
		// gone after a restart, and the site may have changed it).
		j.setMessage("Reading the video playlist...")
		p, err := m.localizePlaylist(ctx, j)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		m.mu.Lock()
		j.localPL = p
		m.mu.Unlock()
		defer removeLocalPlaylist(j)
	}
	m.mu.Lock()
	args, doneArgs := m.videoArgs(j)
	defer doneArgs()
	dir := j.Dir
	files := append([]string(nil), j.VideoFiles...)
	m.mu.Unlock()
	dropPartialFragments(files)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("cannot create folder: %w", err)
	}
	j.liveMu.Lock()
	j.label = j.Name
	j.liveMu.Unlock()
	j.setMessage("Starting yt-dlp...")

	cmd := m.ytdlpCommand(ctx, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start yt-dlp: %w", err)
	}

	var errMu sync.Mutex
	var errLines []string
	errDone := make(chan struct{})
	go func() {
		defer close(errDone)
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			l := sc.Text()
			errMu.Lock()
			errLines = append(errLines, l)
			if len(errLines) > 50 {
				errLines = errLines[1:]
			}
			errMu.Unlock()
		}
	}()

	streams := map[string]*streamProg{} // per file yt-dlp writes
	var expected, shown int64           // total from yt-dlp's format choice; total shown so far
	m.mu.Lock()
	single := j.Video == nil || !j.Video.Playlist
	note := ""
	if j.Fast != nil {
		note = j.Fast.Why
	}
	m.mu.Unlock()
	var order []string
	finalFiles := []string{}
	curItem, nItems := 0, 0
	update := func() {
		var done, total int64
		for _, k := range order {
			s := streams[k]
			done += s.done
			t := s.total
			if t < s.done {
				t = s.done
			}
			total += t
		}
		j.done.Store(done)
		if single {
			// One video: show a total that does not jump around. Use the
			// size of the chosen formats when known, and never go down.
			total = max(total, expected, shown, done)
			shown = total
		}
		m.mu.Lock()
		j.Size = total
		m.mu.Unlock()
	}
	itemStreams := map[int]int{}
	rd := bufio.NewReader(stdout)
	for {
		line, rerr := rd.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, progressPrefix) {
			f := strings.SplitN(strings.TrimPrefix(line, progressPrefix), "|", 8)
			if len(f) == 8 {
				status := f[0]
				done := atoi64(f[1])
				total := atoi64(f[2])
				if total <= 0 {
					total = atoi64(f[3])
				}
				speed, _ := strconv.ParseFloat(f[4], 64)
				if i := atoi64(f[5]); i > 0 {
					curItem = int(i)
				}
				if n := atoi64(f[6]); n > 0 {
					nItems = int(n)
				}
				file := f[7]
				s := streams[file]
				if s == nil {
					s = &streamProg{}
					streams[file] = s
					order = append(order, file)
					m.mu.Lock()
					j.VideoFiles = appendUnique(j.VideoFiles, file)
					m.mu.Unlock()
				}
				s.done = done
				if total > 0 {
					s.total = total
				}
				if status == "finished" && s.total < s.done {
					s.total = s.done
				}
				update()
				j.liveMu.Lock()
				j.speed = speed
				j.liveMu.Unlock()
				what := "Downloading"
				if !s.counted {
					s.counted = true
					itemStreams[curItem]++
					s.nth = itemStreams[curItem]
				}
				if itemStreams[curItem] > 1 || isFormatPart(file) {
					if s.nth == 1 {
						what = "Downloading video"
					} else {
						what = "Downloading audio"
					}
				}
				if nItems > 1 {
					what = fmt.Sprintf("Video %d of %d: %s", curItem, nItems, strings.ToLower(what[:1])+what[1:])
				}
				if status == "finished" {
					what = "Processing (joining video and audio / converting)..."
					j.liveMu.Lock()
					j.speed = 0
					j.liveMu.Unlock()
				}
				if note != "" {
					what += " (using yt-dlp: " + note + ")"
				}
				j.liveMu.Lock()
				j.message = what
				j.msgTime = time.Now()
				j.liveMu.Unlock()
			}
		} else if strings.HasPrefix(line, sizePrefix) {
			if n := expectedSize(strings.TrimPrefix(line, sizePrefix)); n > 0 && single {
				expected = n
				update()
			}
		} else if strings.HasPrefix(line, filePrefix) {
			p := strings.TrimPrefix(line, filePrefix)
			finalFiles = append(finalFiles, p)
			m.mu.Lock()
			j.VideoFiles = appendUnique(j.VideoFiles, p)
			m.mu.Unlock()
		} else if line != "" {
			log.Printf("[%s] yt-dlp: %s", j.Name, line)
		}
		if rerr != nil {
			if rerr != io.EOF {
				log.Printf("[%s] reading yt-dlp output: %v", j.Name, rerr)
			}
			break
		}
	}
	<-errDone
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if werr != nil && len(finalFiles) > 0 {
		errMu.Lock()
		log.Printf("[%s] finished with problems: %s", j.Name, lastErrorLine(strings.Join(errLines, "\n")))
		errMu.Unlock()
		werr = nil
	}
	if werr != nil {
		errMu.Lock()
		msg := lastErrorLine(strings.Join(errLines, "\n"))
		errMu.Unlock()
		if msg == "" {
			msg = werr.Error()
		}
		return errors.New(friendlyJobError(j, msg))
	}
	m.mu.Lock()
	tsfix := j.TSFix
	quality := "best"
	if j.Video != nil {
		quality = j.Video.Quality
	}
	m.mu.Unlock()
	if tsfix {
		// Still a picture in front (no joining was needed, or audio only):
		// BDDM makes a normal file of it (disguise.go).
		for k, p := range finalFiles {
			audioLeft := (quality == "mp3" || quality == "m4a") && strings.Contains(filepath.Base(p), hiddenAudioMark+".")
			if !fileHidden(p) && !audioLeft {
				continue
			}
			j.setMessage("Fixing the video (its pieces were hidden behind a picture)...")
			np, err := repairHidden(ctx, m.tools.Find("ffmpeg"), p, quality)
			if err != nil {
				return err
			}
			finalFiles[k] = np
			m.mu.Lock()
			j.VideoFiles = appendUnique(j.VideoFiles, np)
			m.mu.Unlock()
		}
	}
	// Real sizes of the finished files.
	var total int64
	for _, p := range finalFiles {
		if fi, err := os.Stat(p); err == nil {
			total += fi.Size()
		}
	}
	m.mu.Lock()
	if j.Video != nil && j.Video.Playlist {
		if len(finalFiles) > 0 {
			j.Name = filepath.Base(filepath.Dir(finalFiles[0]))
		}
	} else if len(finalFiles) > 0 {
		j.Name = filepath.Base(finalFiles[len(finalFiles)-1])
		j.Dir = filepath.Dir(finalFiles[len(finalFiles)-1])
	}
	if total > 0 {
		j.Size = total
		j.done.Store(total)
	}
	m.mu.Unlock()
	return nil
}

func atoi64(s string) int64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return int64(f)
}

func appendUnique(list []string, s string) []string {
	for _, v := range list {
		if v == s {
			return list
		}
	}
	return append(list, s)
}

// removeVideoFiles deletes what yt-dlp wrote for a job (and its leftovers).
func removeVideoFiles(j *Job) {
	for _, f := range j.VideoFiles {
		if !filepath.IsAbs(f) {
			f = filepath.Join(j.Dir, f)
		}
		_ = os.Remove(f)
		_ = os.Remove(f + ".part")
		_ = os.Remove(f + ".ytdl")
		_ = os.Remove(f + ".bddm.json")
		if frags, _ := filepath.Glob(globEscape(f) + ".part-Frag*"); len(frags) > 0 {
			for _, fr := range frags {
				_ = os.Remove(fr)
			}
		}
	}
	if j.Video != nil && j.Video.Playlist && j.Name != "" {
		_ = os.Remove(filepath.Join(j.Dir, j.Name)) // only if it is empty now
	}
}

func globEscape(s string) string {
	r := strings.NewReplacer("[", "[[]", "*", "[*]", "?", "[?]")
	return r.Replace(s)
}

// isFormatPart reports whether a file name looks like one stream of a
// download that will be joined later ("name.f137.mp4").
func isFormatPart(file string) bool {
	return formatPartRe.MatchString(filepath.Base(file))
}

var formatPartRe = regexp.MustCompile(`\.f[0-9a-zA-Z-]+\.[a-z0-9]+$`)

// cookiesBrowser mirrors Settings.CookiesBrowser so yt-dlp commands can read
// it without holding the manager lock.
var cookiesBrowser atomic.Value

// streamOutName is the file name (without extension) of a stream download:
// the page title.
func streamOutName(title string) string {
	name := firstNonEmpty(cleanTitle(title), sanitizeName(title))
	if len(name) > 150 {
		name = strings.TrimSpace(strings.ToValidUTF8(name[:150], ""))
	}
	return name
}

// freeVideoSuffix returns "" when no finished file of this video is in dir,
// otherwise " (2)", " (3)", ... so a new download does not reuse it.
// globPrefix is the (glob-escaped) start of the file name.
func freeVideoSuffix(dir, globPrefix, quality string) string {
	exts := []string{".mp4", ".mkv", ".webm"}
	switch quality {
	case "mp3":
		exts = []string{".mp3"}
	case "m4a":
		exts = []string{".m4a"}
	}
	exists := func(suffix string) bool {
		matches, _ := filepath.Glob(filepath.Join(globEscape(dir), globPrefix+globEscape(suffix)+".*"))
		for _, p := range matches {
			b := strings.ToLower(p)
			for _, e := range exts {
				if strings.HasSuffix(b, e) {
					return true
				}
			}
		}
		return false
	}
	if !exists("") {
		return ""
	}
	for n := 2; n < 1000; n++ {
		s := fmt.Sprintf(" (%d)", n)
		if !exists(s) {
			return s
		}
	}
	return ""
}

func outSuffix(o *VideoOptions) string {
	// keep only safe characters
	s := o.Suffix
	if s == "" || len(s) > 8 || strings.ContainsAny(s, "%/\\") {
		return ""
	}
	return s
}

func browserName(b string) string {
	switch b {
	case "firefox":
		return "Firefox"
	case "chrome":
		return "Chrome"
	case "edge":
		return "Edge"
	case "brave":
		return "Brave"
	}
	return b
}
