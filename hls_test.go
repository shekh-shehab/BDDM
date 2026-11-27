package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseHLSAttrsAndMedia(t *testing.T) {
	a := parseHLSAttrs(`METHOD=AES-128,URI="https://ex/key.bin",IV=0x0123456789ABCDEF0123456789ABCDEF`)
	if a["METHOD"] != "AES-128" || a["URI"] != "https://ex/key.bin" {
		t.Fatalf("attrs: %+v", a)
	}
	if a["IV"] != "0x0123456789ABCDEF0123456789ABCDEF" {
		t.Fatalf("IV: %q", a["IV"])
	}

	text := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:7
#EXT-X-KEY:METHOD=AES-128,URI="https://ex/key.bin",IV=0x00000000000000000000000000000007
#EXT-X-MAP:URI="init.mp4"
#EXTINF:2.0,
a1.m4s
#EXTINF:2.0,
#EXT-X-BYTERANGE:1000@0
a2.m4s
#EXT-X-KEY:METHOD=NONE
#EXTINF:1.5,
a3.m4s
#EXT-X-ENDLIST
`
	pl, err := parseMediaPlaylist(text)
	if err != nil {
		t.Fatal(err)
	}
	if !pl.End || !pl.FMP4 || pl.Seq0 != 7 || len(pl.Segs) != 3 || pl.Map == nil || pl.Map.URL != "init.mp4" {
		t.Fatalf("media: %+v", pl)
	}
	if pl.Segs[0].Seq != 7 || pl.Segs[0].Key == nil || pl.Segs[2].Key != nil {
		t.Fatalf("keys/seq: %+v", pl.Segs)
	}
	if pl.Segs[1].Off != 0 || pl.Segs[1].Len != 1000 {
		t.Fatalf("range: %+v", pl.Segs[1])
	}
	if pl.Dur < 5.4 || pl.Dur > 5.6 {
		t.Fatalf("dur %v", pl.Dur)
	}

	_, err = parseMediaPlaylist(`#EXTM3U
#EXT-X-KEY:METHOD=SAMPLE-AES,URI="skd://x"
#EXTINF:1,
a.ts
#EXT-X-ENDLIST
`)
	if !errors.Is(err, errSampleAES) {
		t.Fatalf("SAMPLE-AES: %v", err)
	}
}

func TestPickHLSQualityHighest(t *testing.T) {
	qs := parseMaster(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=200000,RESOLUTION=640x360,AUDIO="aac"
/lo.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=1280x720,AUDIO="aac"
/hi.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=400000,RESOLUTION=854x480,AUDIO="aac"
/mid.m3u8
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aac",NAME="English",DEFAULT=YES,URI="/en.m3u8"
`)
	if len(qs) != 3 || qs[0].Height != 720 || qs[0].Audio != "aac" {
		t.Fatalf("sorted: %+v", qs)
	}
	q, ok := pickHLSQuality(qs, "best")
	if !ok || q.Height != 720 {
		t.Fatalf("best: %+v", q)
	}
	q, _ = pickHLSQuality(qs, "480")
	if q.Height != 480 {
		t.Fatalf("480: %+v", q)
	}
	q, _ = pickHLSQuality(qs, "240")
	if q.Height != 360 {
		t.Fatalf("below all: %+v", q)
	}
	if got := audioURIFromMaster(`#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aac",NAME="English",DEFAULT=YES,URI="/en.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aac",NAME="Off"
`, "aac"); got != "/en.m3u8" {
		t.Fatalf("audio URI %q", got)
	}
}

func TestDecryptAES128AndIV(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, 16)
	plain := []byte("hello HLS segment")
	iv := defaultSegIV(7)
	wantIV, _ := hex.DecodeString("00000000000000000000000000000007")
	if !bytes.Equal(iv, wantIV) {
		t.Fatalf("IV=%x want %x", iv, wantIV)
	}
	enc := encryptAES128ForTest(plain, key, iv)
	got, err := decryptAES128(enc, key, iv)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("got %q", got)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 20)...)
	var ts []byte
	for i := 0; i < 3; i++ {
		pkt := make([]byte, 188)
		pkt[0] = 0x47
		ts = append(ts, pkt...)
	}
	got = stripImagePrefix(append(png, ts...))
	if !bytes.Equal(got, ts) {
		t.Fatalf("strip: %d bytes", len(got))
	}
	if !bytes.Equal(stripImagePrefix(ts), ts) {
		t.Fatal("plain TS changed")
	}
}

func encryptAES128ForTest(data, key, iv []byte) []byte {
	pad := aes.BlockSize - len(data)%aes.BlockSize
	if pad == 0 {
		pad = aes.BlockSize
	}
	src := append(append([]byte{}, data...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(src))
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, src)
	return out
}

func TestVideoWindowDefaultsHighestAndFollowsTheme(t *testing.T) {
	b, err := os.ReadFile("ui/video.html")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `opt("q", "best", "Best available"`) || !strings.Contains(s, `, true, false)`) {
		t.Fatal("quality default is not Best available / highest")
	}
	if strings.Contains(s, "h.height <= 1080") {
		t.Fatal("old default (1080p cap) is still in video.html")
	}
	if !strings.Contains(s, "prefers-color-scheme: dark") || !strings.Contains(s, `html[data-scheme="dark"]`) {
		t.Fatal("video window theme does not follow Windows / color scheme")
	}
}

func TestHLSSegmentRetry(t *testing.T) {
	var hits atomic.Int64
	payload := bytes.Repeat([]byte("SEG"), 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, "nope")
			return
		}
		w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	m, _ := newTestManager(t, 2)
	j := &Job{URL: srv.URL + "/index.m3u8", Name: "Retry", Parts: 2}
	path := filepath.Join(t.TempDir(), "s.bin")
	keys := &hlsKeyCache{m: m, j: j, got: map[string][]byte{}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := m.downloadHLSSeg(ctx, j, keys, hlsSeg{URL: srv.URL + "/seg.ts", Seq: 1}, path, false, nil); err != nil {
		t.Fatal(err)
	}
	if hits.Load() < 3 {
		t.Fatalf("retries: %d hits, want at least 3", hits.Load())
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, payload) {
		t.Fatalf("file %q", got)
	}
}

func serveHLSDir(t *testing.T, dir string, failFirst map[string]int) (*httptest.Server, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	var hits, fails atomic.Int64
	left := map[string]*atomic.Int64{}
	for name, n := range failFirst {
		a := &atomic.Int64{}
		a.Store(int64(n))
		left[name] = a
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		name := filepath.Base(r.URL.Path)
		if a := left[name]; a != nil && a.Add(-1) >= 0 {
			fails.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, name))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits, &fails
}

func addHLSJob(m *Manager, u, dir, title, quality string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := &Job{ID: newID(), URL: u, Kind: "video", Stream: "hls", Name: title, Dir: dir, Parts: 4,
		Status: StatusQueued, Started: time.Now(), Video: &VideoOptions{Quality: quality, Title: title}}
	m.jobs = append(m.jobs, j)
	m.scheduleLocked()
	return j.ID
}

func TestNativeHLSMasterAndMedia(t *testing.T) {
	needFFmpeg(t)
	hi := makeHLS(t, 4)
	lo := makeHLS(t, 4)
	root := t.TempDir()
	mustCopyDir(t, hi, filepath.Join(root, "hi"))
	mustCopyDir(t, lo, filepath.Join(root, "lo"))
	master := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=1280x720\nhi/index.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=200000,RESOLUTION=640x360\nlo/index.m3u8\n"
	if err := os.WriteFile(filepath.Join(root, "master.m3u8"), []byte(master), 0644); err != nil {
		t.Fatal(err)
	}
	var hiHits, loHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.Contains(p, "/hi/") {
			hiHits.Add(1)
		}
		if strings.Contains(p, "/lo/") {
			loHits.Add(1)
		}
		http.ServeFile(w, r, filepath.Join(root, strings.TrimPrefix(p, "/")))
	}))
	t.Cleanup(srv.Close)

	m, dl := newTestManager(t, 4)
	if m.tools.Find("ffmpeg") == "" {
		t.Skip("ffmpeg not found by BDDM")
	}
	id := addHLSJob(m, srv.URL+"/master.m3u8", dl, "Master Clip", "best")
	v := waitDone(t, m, id, 2*time.Minute)
	if v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	if hiHits.Load() == 0 {
		t.Fatal("highest quality playlist was not fetched")
	}
	if loHits.Load() != 0 {
		t.Fatalf("lowest quality was also fetched (%d)", loHits.Load())
	}
	if d := videoSeconds(t, filepath.Join(dl, "Master Clip.mp4")); d < 3 {
		t.Fatalf("duration %.1f", d)
	}
	// temp segments must be gone
	if matches, _ := filepath.Glob(filepath.Join(dl, "*.bddm-hls*")); len(matches) > 0 {
		t.Fatalf("temp files left: %v", matches)
	}
	if v.Size <= 0 || v.Done < v.Size && v.Status == StatusDone {
		t.Fatalf("progress size=%d done=%d", v.Size, v.Done)
	}
}

func TestNativeHLSEncryptedAndRetry(t *testing.T) {
	needFFmpeg(t)
	dir := makeEncryptedHLS(t, 4)
	var segFails atomic.Int64
	var failedOnce atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".ts") && !failedOnce.Load() {
			failedOnce.Store(true)
			segFails.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, filepath.Base(r.URL.Path)))
	}))
	t.Cleanup(srv.Close)

	m, dl := newTestManager(t, 4)
	if m.tools.Find("ffmpeg") == "" {
		t.Skip("ffmpeg not found by BDDM")
	}
	id := addHLSJob(m, srv.URL+"/index.m3u8", dl, "Secret Clip", "best")
	v := waitDone(t, m, id, 2*time.Minute)
	if v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	if segFails.Load() < 1 {
		t.Fatal("the failing segment was never hit")
	}
	if d := videoSeconds(t, filepath.Join(dl, "Secret Clip.mp4")); d < 3 {
		t.Fatalf("duration %.1f (decrypt/merge failed?)", d)
	}
}

func TestNativeHLSFMP4(t *testing.T) {
	needFFmpeg(t)
	dir := makeFMP4HLS(t, 4)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fs := http.FileServer(http.Dir(dir))
	srv := &http.Server{Handler: fs}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	m, dl := newTestManager(t, 4)
	if m.tools.Find("ffmpeg") == "" {
		t.Skip("ffmpeg not found by BDDM")
	}
	id := addHLSJob(m, "http://"+ln.Addr().String()+"/index.m3u8", dl, "FMP4 Clip", "best")
	v := waitDone(t, m, id, 2*time.Minute)
	if v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	if d := videoSeconds(t, filepath.Join(dl, "FMP4 Clip.mp4")); d < 3 {
		t.Fatalf("duration %.1f", d)
	}
}

func TestPublicHLSStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skip("network: ", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode >= 400 {
		t.Skipf("public stream HTTP %d %v", resp.StatusCode, err)
	}
	text, err := cleanPlaylist(body, "https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8")
	if err != nil {
		t.Fatalf("public playlist was not HLS: %v", err)
	}
	qs := parseMaster(text)
	q, ok := pickHLSQuality(qs, "best")
	if !ok || q.Height != 1080 {
		t.Fatalf("public qualities: %+v (best=%+v)", qs, q)
	}
	t.Logf("public mux HLS: %d qualities, highest %dp bandwidth %d url %s", len(qs), q.Height, q.Bandwidth, q.URL)
	// Fetch the media playlist and the first segment header, not the whole film.
	mt, err := fetchURL(ctx, q.URL)
	if err != nil {
		t.Logf("media playlist: %v (qualities were read)", err)
		return
	}
	pl, err := parseMediaPlaylist(mt)
	if err != nil {
		t.Fatalf("media: %v", err)
	}
	t.Logf("public media: %d segments, %.1f s, end=%v fmp4=%v", len(pl.Segs), pl.Dur, pl.End, pl.FMP4)
	if len(pl.Segs) == 0 {
		t.Fatal("media playlist has no segments")
	}
	segReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, pl.Segs[0].URL, nil)
	segReq.Header.Set("Range", "bytes=0-1023")
	sr, err := http.DefaultClient.Do(segReq)
	if err != nil {
		t.Logf("first segment: %v", err)
		return
	}
	defer sr.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(sr.Body, 1024))
	t.Logf("first segment HTTP %d, %d bytes, TS sync=%v", sr.StatusCode, len(b), len(b) > 0 && b[0] == 0x47)
}

func TestMovielinkbdReachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pages := []string{
		"https://movielinkbd.tv/",
		"https://www.movielinkbd.tv/",
		"https://movielinkbd.at/",
		"https://www.movielinkbd.at/",
		"https://35ep54.movielinkbd.at/",
	}
	var notes []string
	for _, u := range pages {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			notes = append(notes, u+" -> "+err.Error())
			t.Logf("%s: %v", u, err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		title := htmlTitle(body)
		cf := resp.Header.Get("cf-mitigated")
		notes = append(notes, fmtMovielinkNote(u, resp.StatusCode, resp.Request.URL.String(), title, cf, body))
		t.Logf("%s -> HTTP %d final %s title=%q cf-mitigated=%q m3u8=%v",
			u, resp.StatusCode, resp.Request.URL.String(), title, cf, bytes.Contains(bytes.ToLower(body), []byte("m3u8")))
	}
	if len(notes) == 0 {
		t.Skip("movielinkbd hosts were not reachable")
	}
	// This is a reachability check only: do not fail if Cloudflare blocks
	// the homepage, and do not claim a real movie stream was downloaded.
}

func fmtMovielinkNote(u string, status int, final, title, cf string, body []byte) string {
	return u
}

func htmlTitle(body []byte) string {
	low := bytes.ToLower(body)
	i := bytes.Index(low, []byte("<title"))
	if i < 0 {
		return ""
	}
	i = bytes.IndexByte(low[i:], '>')
	if i < 0 {
		return ""
	}
	// re-find from start of tag
	start := bytes.Index(low, []byte("<title"))
	start = start + bytes.IndexByte(low[start:], '>') + 1
	end := bytes.Index(low[start:], []byte("</title>"))
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(string(body[start : start+end]))
}

func fetchURL(ctx context.Context, raw string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", errors.New(resp.Status)
	}
	return cleanPlaylist(b, raw)
}

func needFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
}

func mustCopyDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func makeEncryptedHLS(t *testing.T, seconds int) string {
	t.Helper()
	needFFmpeg(t)
	dir := t.TempDir()
	key := bytes.Repeat([]byte{0x42}, 16)
	keyPath := filepath.Join(dir, "enc.key")
	if err := os.WriteFile(keyPath, key, 0644); err != nil {
		t.Fatal(err)
	}
	// ffmpeg's keyinfo file: playlist URI, path to the 16-byte key, IV.
	info := filepath.Join(dir, "key.info")
	if err := os.WriteFile(info, []byte("enc.key\n"+keyPath+"\n"+strings.Repeat("0", 32)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=320x180:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440", "-t", strconv.Itoa(seconds), "-c:v", "libx264", "-preset", "ultrafast",
		"-c:a", "aac", "-g", "25", "-f", "hls", "-hls_time", "1", "-hls_list_size", "0",
		"-hls_key_info_file", info,
		"-hls_segment_filename", filepath.Join(dir, "seg%03d.ts"), filepath.Join(dir, "index.m3u8"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg enc: %v %s", err, out)
	}
	return dir
}

func makeFMP4HLS(t *testing.T, seconds int) string {
	t.Helper()
	needFFmpeg(t)
	dir := t.TempDir()
	cmd := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=320x180:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440", "-t", strconv.Itoa(seconds), "-c:v", "libx264", "-preset", "ultrafast",
		"-c:a", "aac", "-g", "25", "-f", "hls", "-hls_time", "1", "-hls_list_size", "0",
		"-hls_segment_type", "fmp4", "-hls_fmp4_init_filename", "init.mp4",
		"-hls_segment_filename", filepath.Join(dir, "seg%03d.m4s"), filepath.Join(dir, "index.m3u8"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg fmp4: %v %s", err, out)
	}
	return dir
}
