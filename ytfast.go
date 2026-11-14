package main

// ytfast.go: faster YouTube downloads.
//
// yt-dlp downloads YouTube's normal (not fragmented) video and audio streams
// over one connection, and YouTube slows down each connection. For a single
// YouTube video BDDM therefore asks yt-dlp only for the links of the streams
// that match the chosen quality, downloads each stream with its own
// multi-connection engine (in 10 MB pieces, like yt-dlp asks for them), and
// joins video and audio with ffmpeg (no re-encoding).
//
// If anything about this does not work (links refused, fragmented formats,
// live videos, ffmpeg errors, ...), BDDM switches to the normal yt-dlp
// download for that job, so it is never worse than before.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FastYT is the saved state of a fast YouTube download (in history.json,
// so pause/resume works after a restart).
type FastYT struct {
	Final   string       `json:"final,omitempty"` // full path of the finished file
	Streams []FastStream `json:"streams,omitempty"`
	Off     bool         `json:"off,omitempty"` // fast download failed: use yt-dlp
	Why     string       `json:"why,omitempty"` // why yt-dlp is used (shown in the list)
}

type FastStream struct {
	Format string `json:"format"` // yt-dlp format id, e.g. "137"
	Ext    string `json:"ext"`
	Size   int64  `json:"size"`
	File   string `json:"file"` // temp file (full path; ".part" while downloading)
	What   string `json:"what"` // "video", "audio" or "the video"
}

var errNotFast = errors.New("this video cannot use the fast download")

// notFast is errNotFast with a short reason for the Downloads list.
type notFastErr struct{ why string }

func (e *notFastErr) Error() string        { return e.why }
func (e *notFastErr) Is(target error) bool { return target == errNotFast }

func notFast(why string) error { return &notFastErr{why} }

// linksError: yt-dlp could not give the links (e.g. YouTube asks to sign
// in). yt-dlp is used for this run, but the next run tries the fast way again.
type linksError struct{ err error }

func (e *linksError) Error() string { return e.err.Error() }
func (e *linksError) Unwrap() error { return e.err }

const (
	ytChunk    = 10 << 20 // bytes per request (YouTube slows down bigger ranges)
	ytConnsMin = 16       // YouTube: at least this many connections (more if the setting is higher)
	ytRefresh  = 3        // times to ask for new links when YouTube refuses the old ones
)

type ytLinks struct {
	final   string
	streams []ytStream
}

type ytStream struct {
	id, ext, url, what string
	size, chunk        int64
	headers            map[string]string
}

// useFastYouTube decides whether a job tries the fast download. Call with m.mu held.
func (m *Manager) useFastYouTubeLocked(j *Job) bool {
	o := j.Video
	if o == nil || o.Playlist || o.Quality == "mp3" || o.Quality == "m4a" {
		return false
	}
	if j.Fast != nil && j.Fast.Off {
		return false
	}
	return isYouTube(j.URL) && !isStreamJob(j) && m.tools.Find("ffmpeg") != ""
}

// resolveYouTube asks yt-dlp (without downloading) for the file name and the
// links of the streams. sel is an exact format ("137+140") when resuming.
func (m *Manager) resolveYouTube(ctx context.Context, j *Job, opt *VideoOptions, dir, sel string) (*ytLinks, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	args, done := m.baseArgs(j)
	defer done()
	args = append(args, "-P", dir)
	args = append(args, outputArgs(j, opt)...)
	if sel != "" {
		args = append(args, "-f", sel, "--merge-output-format", "mp4")
	} else {
		args = append(args, formatArgs(opt)...)
	}
	args = append(args, "--no-playlist", "--print", "BDDMN|%(filename)s", "--print", "%()j", "--", j.URL)
	cmd := m.ytdlpCommand(ctx, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		msg := lastErrorLine(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	return parseYouTubeLinks(out)
}

// parseYouTubeLinks reads yt-dlp's "--print filename --print %()j" answer.
func parseYouTubeLinks(out []byte) (*ytLinks, error) {
	r := &ytLinks{}
	var data map[string]any
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "BDDMN|"):
			r.final = strings.TrimPrefix(line, "BDDMN|")
		case strings.HasPrefix(line, "{") && data == nil:
			if err := json.Unmarshal([]byte(line), &data); err != nil {
				return nil, errors.New("yt-dlp gave an answer BDDM could not read")
			}
		}
	}
	if data == nil || r.final == "" || r.final == "NA" {
		return nil, notFast("yt-dlp did not give the file name and links")
	}
	if live, _ := data["is_live"].(bool); live {
		return nil, notFast("live video")
	}
	switch str(data, "live_status") {
	case "is_live", "is_upcoming", "post_live":
		return nil, notFast("live video")
	}
	var fmts []map[string]any
	if rf, ok := data["requested_formats"].([]any); ok && len(rf) > 0 {
		for _, x := range rf {
			f, ok := x.(map[string]any)
			if !ok {
				return nil, notFast("unexpected answer from yt-dlp")
			}
			fmts = append(fmts, f)
		}
	} else {
		fmts = []map[string]any{data}
	}
	if len(fmts) > 2 {
		return nil, notFast("more than two streams")
	}
	for _, f := range fmts {
		s := ytStream{id: str(f, "format_id"), ext: str(f, "ext"), url: str(f, "url"), size: int64(num(f, "filesize"))}
		p := str(f, "protocol")
		if s.id == "" || s.ext == "" || s.url == "" || (p != "https" && p != "http") || strings.ContainsAny(s.id, "+/") {
			return nil, notFast("this quality is only available as a stream in pieces (" + p + ")")
		}
		if do, ok := f["downloader_options"].(map[string]any); ok {
			s.chunk = int64(num(do, "http_chunk_size"))
		}
		s.headers = map[string]string{}
		if h, ok := f["http_headers"].(map[string]any); ok {
			for k, v := range h {
				if vs, ok := v.(string); ok {
					s.headers[k] = vs
				}
			}
		}
		switch {
		case len(fmts) == 1:
			s.what = "video"
		case str(f, "vcodec") != "none" && str(f, "vcodec") != "":
			s.what = "video"
		default:
			s.what = "audio"
		}
		r.streams = append(r.streams, s)
	}
	if len(r.streams) == 2 && r.streams[0].what == r.streams[1].what {
		r.streams[0].what, r.streams[1].what = "video", "audio"
	}
	return r, nil
}

// fastTempFile: "Title [id].bddm-v.mp4" next to the final "Title [id].mp4".
func fastTempFile(final, what, ext string) string {
	base := strings.TrimSuffix(final, filepath.Ext(final))
	tag := "v"
	if what == "audio" {
		tag = "a"
	}
	return base + ".bddm-" + tag + "." + ext
}

func fastSel(f *FastYT) string {
	ids := make([]string, len(f.Streams))
	for i, s := range f.Streams {
		ids[i] = s.Format
	}
	return strings.Join(ids, "+")
}

// runFastYouTube downloads a single YouTube video with BDDM's engine.
// errNotFast (or any other error) means: use yt-dlp instead.
func (m *Manager) runFastYouTube(ctx context.Context, j *Job) error {
	m.mu.Lock()
	opt := *j.Video
	dir := j.Dir
	fast := j.Fast
	workers := j.Parts
	m.mu.Unlock()
	if workers < 1 {
		workers = defaultParts
	}
	workers = max(workers, ytConnsMin)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("cannot create folder: %w", err)
	}

	j.setMessage("Getting the video links...")
	sel := ""
	if fast != nil && len(fast.Streams) > 0 {
		sel = fastSel(fast)
	}
	links, err := m.resolveYouTube(ctx, j, &opt, dir, sel)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, errNotFast) {
			return err
		}
		return &linksError{err}
	}
	if fast == nil || len(fast.Streams) == 0 {
		if fileExists(links.final) {
			return notFast("the file is already in the folder") // yt-dlp knows what to do
		}
		fast = &FastYT{Final: links.final}
		for _, s := range links.streams {
			fast.Streams = append(fast.Streams, FastStream{Format: s.id, Ext: s.ext, Size: s.size,
				File: fastTempFile(links.final, s.what, s.ext), What: s.what})
		}
	} else if len(links.streams) != len(fast.Streams) {
		return notFast("YouTube changed the video's formats")
	}
	for i := range fast.Streams {
		if links.streams[i].id != fast.Streams[i].Format {
			return notFast("YouTube changed the video's formats")
		}
	}

	// Check every link (and get the exact sizes) before starting.
	var total int64
	for i := range fast.Streams {
		s := &links.streams[i]
		info, err := probe(ctx, m.client, s.url, reqHeaders{Extra: s.headers})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("YouTube refused the %s link: %w", s.what, err)
		}
		if !info.CanRange || info.Size <= 0 {
			return notFast("YouTube did not allow parts for the " + s.what)
		}
		fs := &fast.Streams[i]
		if fs.Size > 0 && fs.Size != info.Size {
			// Not the same file any more: start this stream again.
			removeFastFiles(fs.File)
		}
		fs.Size = info.Size
		total += info.Size
	}

	m.mu.Lock()
	j.Fast = fast
	j.Size = total
	j.Name = filepath.Base(fast.Final)
	for _, s := range fast.Streams {
		j.VideoFiles = appendUnique(j.VideoFiles, s.File)
	}
	m.saveHistoryLocked()
	m.mu.Unlock()
	j.liveMu.Lock()
	j.label = filepath.Base(fast.Final)
	j.liveMu.Unlock()

	// Bytes already on disk (after a pause or restart), so the progress
	// starts at the right place and the total stays the same.
	var have int64
	var todo []int
	for i, fs := range fast.Streams {
		if fileExists(fs.File) && !fileExists(fs.File+".part") {
			if st, err := os.Stat(fs.File); err == nil && st.Size() == fs.Size {
				have += fs.Size
				continue // finished before a pause
			}
		}
		if st, err := loadState(fs.File + ".bddm.json"); err == nil && st.Size == fs.Size && fileExists(fs.File+".part") {
			for _, p := range st.Parts {
				have += p.Written
			}
		}
		todo = append(todo, i)
	}
	j.done.Store(have)

	// Video and audio download at the same time; the connections are shared
	// by size (YouTube slows down each connection, so more connections
	// means more speed).
	conns := ytConns(workers, fast.Streams, todo)
	if len(todo) > 0 {
		if len(fast.Streams) == 2 && len(todo) == 2 {
			j.setMessage("Downloading video and audio with %d connections...", workers)
		} else {
			j.setMessage("Downloading %s with %d connections...", fast.Streams[todo[0]].What, workers)
		}
	}
	stopMsg := keepMessage(j)
	defer stopMsg()
	var linksMu sync.Mutex
	dctx, dcancel := context.WithCancel(ctx)
	defer dcancel()
	errs := make([]error, len(fast.Streams))
	var wg sync.WaitGroup
	for _, i := range todo {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			fs := fast.Streams[i]
			for refresh := 0; ; refresh++ {
				linksMu.Lock()
				s := links.streams[i]
				linksMu.Unlock()
				err := m.fastStream(dctx, j, fs, s, conns[i])
				if err == nil {
					return
				}
				if dctx.Err() != nil {
					return
				}
				if refresh >= ytRefresh || isNetworkErr(err) {
					errs[i] = err
					dcancel()
					return
				}
				// YouTube links expire after a few hours (and sometimes refuse
				// earlier): ask yt-dlp for new ones and continue.
				log.Printf("[%s] %s: %v; getting new links", filepath.Base(fs.File), fs.What, err)
				j.setMessage("Getting new video links...")
				nl, rerr := m.resolveYouTube(dctx, j, &opt, dir, fastSel(fast))
				if rerr == nil && (len(nl.streams) != len(fast.Streams) || nl.streams[i].id != fs.Format) {
					rerr = notFast("YouTube changed the video's formats")
				}
				if rerr != nil {
					if dctx.Err() == nil {
						errs[i] = rerr
						dcancel()
					}
					return
				}
				linksMu.Lock()
				links.streams[i].url = nl.streams[i].url
				links.streams[i].headers = nl.streams[i].headers
				linksMu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	if len(todo) > 0 && dctx.Err() != nil {
		return errors.New("download stopped")
	}

	j.setMessage("Joining video and audio...")
	if err := m.joinFast(ctx, fast); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	for _, s := range fast.Streams {
		removeFastFiles(s.File)
	}
	var size int64
	if st, err := os.Stat(fast.Final); err == nil {
		size = st.Size()
	}
	m.mu.Lock()
	j.Name = filepath.Base(fast.Final)
	j.Dir = filepath.Dir(fast.Final)
	j.VideoFiles = []string{fast.Final}
	if size > 0 {
		j.Size = size
		j.done.Store(size)
	}
	m.mu.Unlock()

	if opt.Thumb || (opt.Subs && opt.SubLang != "") {
		j.setMessage("Getting subtitles / thumbnail...")
		m.fastExtras(ctx, j, &opt, dir)
	}
	return nil
}

// fastStream downloads one stream into fs.File with the multi-part engine.
func (m *Manager) fastStream(ctx context.Context, j *Job, fs FastStream, s ytStream, workers int) error {
	full := fs.File
	chunk := s.chunk
	if chunk <= 0 || chunk > ytChunk {
		chunk = ytChunk
	}
	st, err := loadState(full + ".bddm.json")
	resuming := err == nil && st.Size == fs.Size && fileExists(full+".part")
	if resuming {
		st.URL = s.url // the old link may have expired
	} else {
		_ = os.Remove(full + ".part")
		st = &State{URL: s.url, Size: fs.Size, Name: filepath.Base(full), Parts: splitChunks(fs.Size, chunk)}
	}
	f, err := openPartFile(full+".part", st.Size, resuming)
	if err != nil {
		return fmt.Errorf("could not create file: %w", err)
	}
	d := &download{
		job: j, State: st, StatePath: full + ".bddm.json", File: f, client: m.client,
		hdr: reqHeaders{Extra: s.headers}, CanRange: true, limiter: connLimiter{limit: 1 << 30},
		Workers: workers, strict: true, shared: true,
	}
	if err := d.save(); err != nil {
		f.Close()
		return fmt.Errorf("could not save state: %w", err)
	}
	err = d.run(ctx)
	_ = d.save()
	f.Close()
	if err != nil {
		return err
	}
	if err := os.Rename(full+".part", full); err != nil {
		return fmt.Errorf("downloaded, but could not rename file: %w", err)
	}
	_ = os.Remove(full + ".bddm.json")
	return nil
}

// keepMessage keeps the job's current status text visible while a long
// download runs (texts older than 30 s are hidden), and brings it back
// after a short retry note.
func keepMessage(j *Job) (stop func()) {
	j.liveMu.Lock()
	main := j.message
	j.liveMu.Unlock()
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				j.liveMu.Lock()
				if j.message == main || time.Since(j.msgTime) > 15*time.Second {
					j.message = main
					j.msgTime = time.Now()
				}
				j.liveMu.Unlock()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// ytConns shares the connections between the streams still to download,
// by size (audio gets at least 2).
func ytConns(total int, streams []FastStream, todo []int) map[int]int {
	out := map[int]int{}
	if len(todo) == 1 {
		out[todo[0]] = total
		return out
	}
	var sum int64
	for _, i := range todo {
		sum += streams[i].Size
	}
	left := total
	for k, i := range todo {
		if k == len(todo)-1 {
			out[i] = max(left, 1)
			break
		}
		n := 1
		if sum > 0 {
			n = int((int64(total)*streams[i].Size + sum/2) / sum)
		}
		n = min(max(n, 2), total-2)
		out[i] = n
		left -= n
	}
	return out
}

// splitChunks cuts a file into pieces of at most chunk bytes.
func splitChunks(size, chunk int64) []Part {
	if size <= 0 {
		return []Part{{From: 0, To: -1}}
	}
	if chunk <= 0 {
		chunk = size
	}
	var parts []Part
	for from := int64(0); from < size; from += chunk {
		parts = append(parts, Part{From: from, To: min(from+chunk, size) - 1})
	}
	return parts
}

// joinFast puts video and audio together into the final file (no
// re-encoding), or just renames a single stream.
func (m *Manager) joinFast(ctx context.Context, f *FastYT) error {
	if len(f.Streams) == 1 {
		return os.Rename(f.Streams[0].File, f.Final)
	}
	ff := m.tools.Find("ffmpeg")
	if ff == "" {
		return errors.New("ffmpeg is missing")
	}
	ext := filepath.Ext(f.Final)
	tmp := strings.TrimSuffix(f.Final, ext) + ".bddm-join" + ext
	_ = os.Remove(tmp)
	var v, a string
	for _, s := range f.Streams {
		if s.What == "audio" {
			a = s.File
		} else {
			v = s.File
		}
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", v, "-i", a, "-map", "0:v:0", "-map", "1:a:0", "-c", "copy"}
	if strings.EqualFold(ext, ".mp4") || strings.EqualFold(ext, ".m4a") || strings.EqualFold(ext, ".mov") {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, tmp)
	cmd := exec.CommandContext(ctx, ff, args...)
	prepareProc(cmd)
	cmd.Cancel = func() error { killTree(cmd); return nil }
	cmd.WaitDelay = 10 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(tmp)
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		return fmt.Errorf("could not join video and audio: %v %s", err, msg)
	}
	if err := os.Rename(tmp, f.Final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("joined, but could not rename the file: %w", err)
	}
	return nil
}

// fastExtras gets the thumbnail / subtitles with yt-dlp (no video). A
// failure here only goes to the log: the video itself is done.
func (m *Manager) fastExtras(ctx context.Context, j *Job, opt *VideoOptions, dir string) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	args, done := m.baseArgs(j)
	defer done()
	args = append(args, "-P", dir)
	args = append(args, outputArgs(j, opt)...)
	args = append(args, "--skip-download", "--no-playlist", "--ignore-errors")
	if opt.Thumb {
		args = append(args, "--write-thumbnail", "--convert-thumbnails", "jpg")
	}
	if opt.Subs && opt.SubLang != "" {
		args = append(args, "--write-subs", "--write-auto-subs", "--sub-langs", opt.SubLang, "--convert-subs", "srt")
	}
	args = append(args, "--", j.URL)
	cmd := m.ytdlpCommand(ctx, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[%s] subtitles/thumbnail: %v %s", j.Name, err, lastErrorLine(string(out)))
	}
}

// dropFastFiles removes the pieces of a fast download that yt-dlp finished
// instead.
func (m *Manager) dropFastFiles(j *Job) {
	m.mu.Lock()
	var streams []FastStream
	if j.Fast != nil {
		streams = j.Fast.Streams
		j.Fast.Streams = nil
	}
	m.mu.Unlock()
	for _, s := range streams {
		removeFastFiles(s.File)
	}
}

func removeFastFiles(file string) {
	_ = os.Remove(file)
	_ = os.Remove(file + ".part")
	_ = os.Remove(file + ".bddm.json")
}

// isNetworkErr: the connection itself failed (no internet, timeouts). Then
// yt-dlp would fail too, so the job just stops and can be resumed later.
func isNetworkErr(err error) bool { return isNetErr(err) }

// fastFailed switches a job to the normal yt-dlp download and returns a
// short reason for the Downloads list.
func (m *Manager) fastFailed(j *Job, err error) string {
	why := shortReason(err)
	var le *linksError
	if errors.As(err, &le) {
		log.Printf("[%s] could not get the YouTube links (%v); using yt-dlp", j.Name, err)
		m.mu.Lock()
		if j.Fast == nil {
			j.Fast = &FastYT{}
		}
		j.Fast.Why = why
		m.mu.Unlock()
		return why // keep any downloaded pieces; the next run tries the fast way again
	}
	m.mu.Lock()
	if j.Fast == nil {
		j.Fast = &FastYT{}
	}
	j.Fast.Off = true
	j.Fast.Why = why
	streams := j.Fast.Streams
	j.Size = 0
	j.done.Store(0)
	m.saveHistoryLocked()
	m.mu.Unlock()
	for _, s := range streams {
		removeFastFiles(s.File)
	}
	log.Printf("[%s] fast YouTube download did not work (%v); using yt-dlp", j.Name, err)
	return why
}

func shortReason(err error) string {
	var le *linksError
	msg := err.Error()
	if errors.As(err, &le) {
		msg = "could not get the video links: " + friendlyVideoError(le.err.Error())
	}
	msg = strings.TrimSpace(msg)
	if len(msg) > 140 {
		msg = strings.ToValidUTF8(msg[:137], "") + "..."
	}
	return msg
}
