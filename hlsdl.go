package main

// hlsdl.go: native HLS downloader. Parallel segments with retry and resume,
// AES-128, fMP4 and MPEG-TS (including pieces hidden behind a picture),
// speed limits and auto-yield, then ffmpeg -c copy into one MP4.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	hlsSegTries   = 4
	hlsSegMax     = 128 << 20 // one segment
	hlsKeyMax     = 64
	hlsWorkersMax = 16
)

// hlsState is written next to the work folder so Pause, a crash, or a
// dropped connection can continue without re-downloading finished pieces.
type hlsState struct {
	URL       string `json:"url"`
	MediaURL  string `json:"mediaUrl"`
	AudioURL  string `json:"audioUrl,omitempty"`
	FMP4      bool   `json:"fmp4,omitempty"`
	Hidden    bool   `json:"hidden,omitempty"`
	Out       string `json:"out"`
	N         int    `json:"n"`
	AudioN    int    `json:"audioN,omitempty"`
	Done      []bool `json:"done"`
	AudioDone []bool `json:"audioDone,omitempty"`
}

func (m *Manager) runNativeHLS(ctx context.Context, j *Job) error {
	j.setMessage("Reading the video playlist...")
	masterURL, masterText, mediaURL, mediaText, audioURL, err := m.loadHLSPlaylists(ctx, j)
	if err != nil {
		return err
	}
	_ = masterURL
	video, err := parseMediaPlaylist(mediaText)
	if err != nil {
		return err
	}
	var audio *hlsMediaPL
	if audioURL != "" {
		at, err2 := m.fetchHLSPlaylist(ctx, j, audioURL)
		if err2 != nil {
			return err2
		}
		audio, err = parseMediaPlaylist(at)
		if err != nil {
			return err
		}
	}

	out := hlsOutputPath(j)
	work := strings.TrimSuffix(out, filepath.Ext(out)) + ".bddm-hls"
	if err := os.MkdirAll(work, 0755); err != nil {
		return fmt.Errorf("cannot create folder: %w", err)
	}
	hidden := j.TSFix
	if !hidden && video.Map == nil {
		// Look at the first piece when probe did not already flag it.
		if len(video.Segs) > 0 {
			if b, _, err := m.hlsGet(ctx, j, video.Segs[0], 16<<10); err == nil && hiddenTS(b) {
				hidden = true
			}
		}
	}

	st := &hlsState{
		URL: j.URL, MediaURL: mediaURL, AudioURL: audioURL,
		FMP4: video.FMP4 || (audio != nil && audio.FMP4), Hidden: hidden, Out: out,
		N: len(video.Segs), Done: make([]bool, len(video.Segs)),
	}
	if audio != nil {
		st.AudioN = len(audio.Segs)
		st.AudioDone = make([]bool, len(audio.Segs))
	}
	if prev, err := loadHLSState(work); err == nil && prev.N == st.N && prev.AudioN == st.AudioN {
		st.Done = padBools(prev.Done, st.N)
		st.AudioDone = padBools(prev.AudioDone, st.AudioN)
		st.Hidden = st.Hidden || prev.Hidden
		hidden = st.Hidden
	}

	m.mu.Lock()
	j.VideoFiles = uniqueStrings(append(j.VideoFiles, out, work))
	j.TSFix = hidden
	if j.Size <= 0 && video.Dur > 0 {
		// A rough size from the master bandwidth, if we know it.
		if bw := hlsPickBandwidth(masterText, mediaURL); bw > 0 {
			j.Size = int64(float64(bw) / 8 * video.Dur)
		}
	}
	m.mu.Unlock()

	keys := &hlsKeyCache{m: m, j: j, got: map[string][]byte{}}

	// Init segments (fMP4) first: later pieces cannot be merged without them.
	vInit := filepath.Join(work, "v-init.mp4")
	if video.Map != nil {
		if err := m.downloadHLSOne(ctx, j, keys, *video.Map, vInit, hidden); err != nil {
			return m.hlsSegErr(0, err)
		}
	} else {
		vInit = ""
	}
	aInit := filepath.Join(work, "a-init.mp4")
	if audio != nil && audio.Map != nil {
		if err := m.downloadHLSOne(ctx, j, keys, *audio.Map, aInit, false); err != nil {
			return m.hlsSegErr(0, err)
		}
	} else {
		aInit = ""
	}

	// Count already-finished pieces for resume / progress.
	var already int64
	markExisting := func(done []bool, prefix string) int64 {
		var n int64
		for i := range done {
			p := hlsSegPath(work, prefix, i)
			if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
				done[i] = true
				n += fi.Size()
			} else {
				done[i] = false
			}
		}
		return n
	}
	already += markExisting(st.Done, "v")
	if audio != nil {
		already += markExisting(st.AudioDone, "a")
	}
	j.done.Store(already)
	j.liveMu.Lock()
	j.xferStart, j.xferFrom = time.Now(), already
	j.speed = 0
	j.liveMu.Unlock()
	_ = saveHLSState(work, st)

	stopSpeed := hlsSpeedLoop(ctx, j)
	defer stopSpeed()

	j.setMessage("Downloading...")
	if err := m.downloadHLSList(ctx, j, keys, video.Segs, st.Done, work, "v", hidden, st); err != nil {
		_ = saveHLSState(work, st)
		return err
	}
	if audio != nil {
		if err := m.downloadHLSList(ctx, j, keys, audio.Segs, st.AudioDone, work, "a", false, st); err != nil {
			_ = saveHLSState(work, st)
			return err
		}
	}

	j.setMessage("Processing (joining into one file)...")
	j.liveMu.Lock()
	j.speed = 0
	j.liveMu.Unlock()

	vfiles := hlsSegFiles(vInit, work, "v", len(video.Segs))
	var afiles []string
	if audio != nil {
		afiles = hlsSegFiles(aInit, work, "a", len(audio.Segs))
	}
	if err := m.mergeHLS(ctx, j, work, out, vfiles, afiles, st.FMP4); err != nil {
		return err
	}
	_ = os.RemoveAll(work)

	size := int64(0)
	if fi, err := os.Stat(out); err == nil {
		size = fi.Size()
	}
	m.mu.Lock()
	j.Name = filepath.Base(out)
	j.Dir = filepath.Dir(out)
	j.VideoFiles = []string{out}
	if size > 0 {
		j.Size = size
		j.done.Store(size)
	}
	m.mu.Unlock()

	if opt := j.Video; opt != nil && (opt.Thumb || (opt.Subs && opt.SubLang != "")) {
		j.setMessage("Getting subtitles / thumbnail...")
		m.fastExtras(ctx, j, opt, filepath.Dir(out))
	}
	return nil
}

func dropHLSWork(out string) {
	if out == "" {
		return
	}
	ext := filepath.Ext(out)
	base := strings.TrimSuffix(out, ext)
	_ = os.RemoveAll(base + ".bddm-hls")
	_ = os.Remove(base + ".bddm-join" + ext)
	_ = os.Remove(out + ".bddm-part")
}

func hlsOutputPath(j *Job) string {
	name := strings.TrimSpace(j.Name)
	if name == "" {
		name = "Video"
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".mp4" && ext != ".mkv" && ext != ".mov" {
		name += ".mp4"
	}
	return filepath.Join(j.Dir, name)
}

func hlsSegPath(work, prefix string, i int) string {
	return filepath.Join(work, fmt.Sprintf("%s-%05d.bin", prefix, i))
}

func hlsSegFiles(init, work, prefix string, n int) []string {
	var out []string
	if init != "" {
		out = append(out, init)
	}
	for i := 0; i < n; i++ {
		out = append(out, hlsSegPath(work, prefix, i))
	}
	return out
}

func padBools(s []bool, n int) []bool {
	if n <= 0 {
		return nil
	}
	out := make([]bool, n)
	copy(out, s)
	return out
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func loadHLSState(work string) (*hlsState, error) {
	b, err := os.ReadFile(filepath.Join(work, "state.json"))
	if err != nil {
		return nil, err
	}
	var st hlsState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func saveHLSState(work string, st *hlsState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(work, "state.json"), b, 0644)
}

func hlsSpeedLoop(ctx context.Context, j *Job) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		var last int64
		var lastT time.Time
		first := true
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case now := <-t.C:
				cur := j.done.Load()
				j.liveMu.Lock()
				if !first {
					if dt := now.Sub(lastT).Seconds(); dt > 0 {
						j.speed = float64(cur-last) / dt
					}
				}
				j.liveMu.Unlock()
				last, lastT, first = cur, now, false
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// loadHLSPlaylists fetches the job's playlist (retrying other Referers on
// 403) and, for a master, the chosen quality and optional audio playlist.
func (m *Manager) loadHLSPlaylists(ctx context.Context, j *Job) (masterURL, masterText, mediaURL, mediaText, audioURL string, err error) {
	m.mu.Lock()
	raw := j.URL
	want := "best"
	if j.Video != nil {
		want = j.Video.Quality
	}
	m.mu.Unlock()

	status, body, err := m.fetchPlaylist(ctx, j, raw)
	status = playlistStatus(status, body)
	if err == nil && (status == 401 || status == 403) {
		status, body, err = m.playlistWithOtherReferers(ctx, j, raw, status, body)
	}
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("Could not read the video playlist: %v", err)
	}
	if status >= 400 {
		return "", "", "", "", "", explainNotPlaylist(status, body)
	}
	text, err := cleanPlaylist(body, raw)
	if err != nil {
		return "", "", "", "", "", explainNotPlaylist(status, body)
	}

	if !strings.Contains(text, "#EXT-X-STREAM-INF") {
		return "", "", raw, text, "", nil
	}
	qs := parseMaster(text)
	q, ok := pickHLSQuality(qs, want)
	if !ok || q.URL == "" {
		return "", "", "", "", "", errors.New("This playlist lists no video qualities.")
	}
	audio := audioURIFromMaster(text, q.Audio)
	mt, err := m.fetchHLSPlaylist(ctx, j, q.URL)
	if err != nil {
		return "", "", "", "", "", err
	}
	return raw, text, q.URL, mt, audio, nil
}

func (m *Manager) fetchHLSPlaylist(ctx context.Context, j *Job, raw string) (string, error) {
	status, body, err := m.fetchPlaylist(ctx, j, raw)
	status = playlistStatus(status, body)
	if err != nil {
		return "", fmt.Errorf("Could not read the video playlist: %v", err)
	}
	if status >= 400 {
		return "", explainNotPlaylist(status, body)
	}
	text, err := cleanPlaylist(body, raw)
	if err != nil {
		return "", explainNotPlaylist(status, body)
	}
	return text, nil
}

func hlsPickBandwidth(master, mediaURL string) int64 {
	if master == "" || mediaURL == "" {
		return 0
	}
	for _, q := range parseMaster(master) {
		if samePlaylist(q.URL, mediaURL) || q.URL == mediaURL {
			return q.Bandwidth
		}
	}
	qs := parseMaster(master)
	if len(qs) > 0 {
		return qs[0].Bandwidth
	}
	return 0
}

type hlsKeyCache struct {
	m   *Manager
	j   *Job
	mu  sync.Mutex
	got map[string][]byte
}

func (c *hlsKeyCache) get(ctx context.Context, uri string) ([]byte, error) {
	c.mu.Lock()
	if k, ok := c.got[uri]; ok {
		c.mu.Unlock()
		return k, nil
	}
	c.mu.Unlock()
	status, body, err := c.m.fetchPlaylist(ctx, c.j, uri)
	if err != nil {
		return nil, fmt.Errorf("could not fetch the encryption key: %w", err)
	}
	if status >= 400 {
		return nil, fmt.Errorf("encryption key: HTTP %d", status)
	}
	if len(body) > hlsKeyMax {
		body = body[:hlsKeyMax]
	}
	// Some servers wrap the 16-byte key in whitespace or a longer file.
	key := body
	if len(key) != 16 {
		key = bytesTrim16(body)
	}
	if len(key) != 16 {
		return nil, fmt.Errorf("encryption key is %d bytes, want 16", len(body))
	}
	c.mu.Lock()
	c.got[uri] = key
	c.mu.Unlock()
	return key, nil
}

func bytesTrim16(b []byte) []byte {
	s := strings.TrimSpace(string(b))
	if len(s) == 16 {
		return []byte(s)
	}
	// hex-encoded key
	if len(s) == 32 {
		if d, err := parseHLSIV(s); err == nil {
			return d
		}
	}
	if len(b) >= 16 {
		return b[:16]
	}
	return b
}

func (m *Manager) downloadHLSList(ctx context.Context, j *Job, keys *hlsKeyCache, segs []hlsSeg, done []bool, work, prefix string, hidden bool, st *hlsState) error {
	n := 0
	for _, ok := range done {
		if !ok {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	workers := j.Parts
	if workers < 1 {
		workers = 4
	}
	if workers > hlsWorkersMax {
		workers = hlsWorkersMax
	}
	if workers > n {
		workers = n
	}

	label := "video"
	if prefix == "a" {
		label = "audio"
	}
	hp := newHLSProgress(j, label, done)
	stopProg := hp.start()
	defer stopProg()
	sem := make(chan struct{}, workers)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var finished int
	total := len(segs)
	for _, ok := range done {
		if ok {
			finished++
		}
	}

	for i, seg := range segs {
		if done[i] {
			continue
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case err := <-errCh:
			wg.Wait()
			return err
		case sem <- struct{}{}:
		}
		i, seg := i, seg
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			path := hlsSegPath(work, prefix, i)
			hp.set(i, fragBusy)
			err := m.downloadHLSSeg(ctx, j, keys, seg, path, hidden, func() { hp.set(i, fragRetry) })
			if err != nil {
				if j.skipFrags && ctx.Err() == nil && !isNetErr(err) {
					log.Printf("[%s] skipping fragment %d: %v", j.Name, i+1, err)
					// leave a tiny placeholder so concat stays aligned
					_ = os.WriteFile(path, nil, 0644)
					mu.Lock()
					done[i] = true
					mu.Unlock()
					hp.set(i, fragDone)
					return
				}
				select {
				case errCh <- m.hlsSegErr(i+1, err):
				default:
				}
				return
			}
			mu.Lock()
			done[i] = true
			finished++
			got := finished
			mu.Unlock()
			hp.set(i, fragDone)
			_ = saveHLSState(work, st)
			j.setMessage("Downloading (%d / %d)...", got, total)
		}()
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
	}
	for i, ok := range done {
		if ok {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("unable to continue: fragment %d was not downloaded", i+1)
	}
	return nil
}

func (m *Manager) hlsSegErr(n int, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || ctxErr(err) {
		return err
	}
	return fmt.Errorf("unable to continue: fragment %d: %w", n, err)
}

func ctxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (m *Manager) downloadHLSSeg(ctx context.Context, j *Job, keys *hlsKeyCache, seg hlsSeg, path string, hidden bool, onRetry func()) error {
	var last error
	for try := 1; try <= hlsSegTries; try++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := m.downloadHLSOne(ctx, j, keys, seg, path, hidden)
		if err == nil {
			return nil
		}
		last = err
		if ctxErr(err) || isRefused(err.Error()) {
			return err
		}
		if !isNetErr(err) && !isRetryableHTTP(err) && try >= 2 {
			return err
		}
		wait := backoff(try)
		if onRetry != nil {
			onRetry()
		}
		j.setMessage("Segment retry (%s); continuing in %s...", shortReason(err), wait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return last
}

func isRetryableHTTP(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for _, c := range []string{"HTTP 408", "HTTP 429", "HTTP 500", "HTTP 502", "HTTP 503", "HTTP 504"} {
		if strings.Contains(s, c) {
			return true
		}
	}
	return false
}

func (m *Manager) downloadHLSOne(ctx context.Context, j *Job, keys *hlsKeyCache, seg hlsSeg, path string, hidden bool) error {
	if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
		return nil
	}
	raw, status, err := m.hlsGet(ctx, j, seg, hlsSegMax)
	if err != nil {
		return err
	}
	if status == 401 || status == 403 {
		return fmt.Errorf("HTTP %d Forbidden", status)
	}
	if status == 404 || status == 410 {
		return fmt.Errorf("fragment not found (HTTP %d)", status)
	}
	if status >= 400 {
		return fmt.Errorf("HTTP %d", status)
	}
	data := raw
	if seg.Key != nil && strings.EqualFold(seg.Key.Method, "AES-128") {
		key, err := keys.get(ctx, seg.Key.URI)
		if err != nil {
			return err
		}
		data, err = decryptAES128(data, key, segmentIV(seg.Key, seg.Seq))
		if err != nil {
			return err
		}
	}
	if hidden || hiddenTS(data) {
		data = stripImagePrefix(data)
	}
	part := path + ".part"
	if err := os.WriteFile(part, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(part, path); err != nil {
		_ = os.Remove(part)
		return err
	}
	j.done.Add(int64(len(data)))
	m.mu.Lock()
	if j.Size < j.done.Load() {
		j.Size = j.done.Load()
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) hlsGet(ctx context.Context, j *Job, seg hlsSeg, limit int64) ([]byte, int, error) {
	if limit <= 0 {
		limit = hlsSegMax
	}
	m.mu.Lock()
	h := j.browserHeaders()
	m.mu.Unlock()
	req, err := newRequest(ctx, http.MethodGet, seg.URL, h)
	if err != nil {
		return nil, 0, err
	}
	applyStreamOrigin(j, req)
	if seg.Len > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", seg.Off, seg.Off+seg.Len-1))
	} else if seg.Off > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", seg.Off))
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	r := m.limitHTTPReader(ctx, j, resp.Body)
	body, err := io.ReadAll(io.LimitReader(r, limit))
	return body, resp.StatusCode, err
}

func (m *Manager) mergeHLS(ctx context.Context, j *Job, work, out string, video, audio []string, fmp4 bool) error {
	ff := m.tools.Find("ffmpeg")
	if ff == "" {
		return errors.New("ffmpeg is missing")
	}
	vcat := filepath.Join(work, "all-v.bin")
	if err := concatFiles(vcat, video); err != nil {
		return fmt.Errorf("could not join segments: %w", err)
	}
	var acat string
	if len(audio) > 0 {
		acat = filepath.Join(work, "all-a.bin")
		if err := concatFiles(acat, audio); err != nil {
			return fmt.Errorf("could not join audio segments: %w", err)
		}
	}
	ext := filepath.Ext(out)
	tmp := strings.TrimSuffix(out, ext) + ".bddm-join" + ext
	_ = os.Remove(tmp)
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return err
	}

	run := func(extra ...string) error {
		args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}
		if !fmp4 {
			args = append(args, "-f", "mpegts")
		}
		args = append(args, "-i", vcat)
		if acat != "" {
			if !fmp4 {
				args = append(args, "-f", "mpegts")
			}
			args = append(args, "-i", acat, "-map", "0:v:0", "-map", "1:a:0")
		}
		args = append(args, "-c", "copy")
		if !fmp4 {
			// AAC in MPEG-TS is ADTS; MP4 needs the raw bitstream.
			args = append(args, "-bsf:a", "aac_adtstoasc")
		}
		args = append(args, extra...)
		args = append(args, "-movflags", "+faststart", tmp)
		return m.runFFmpegCopy(ctx, ff, args)
	}

	err := run()
	if err != nil && acat == "" && !fmp4 {
		// Some streams have no audio, or aac_adtstoasc is not needed.
		err = run("-an")
		if err != nil {
			// last try: no bitstream filter, copy whatever is there
			args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
				"-f", "mpegts", "-i", vcat, "-c", "copy", "-movflags", "+faststart", tmp}
			err = m.runFFmpegCopy(ctx, ff, args)
		}
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, out); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("joined, but could not rename the file: %w", err)
	}
	return nil
}

func (m *Manager) runFFmpegCopy(ctx context.Context, ff string, args []string) error {
	cmd := exec.CommandContext(ctx, ff, args...)
	prepareProc(cmd)
	cmd.Cancel = func() error { killTree(cmd); return nil }
	cmd.WaitDelay = 10 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		if msg == "" {
			return fmt.Errorf("could not join the video: %v", err)
		}
		return fmt.Errorf("could not join the video: %s", msg)
	}
	return nil
}

func concatFiles(dst string, srcs []string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, p := range srcs {
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, in)
		in.Close()
		if err != nil {
			return err
		}
	}
	return f.Close()
}
