package main

// 1.4.2 part 1: streams like anikoto.tv -> megaplay.buzz: the video server
// wants exactly the player site's Referer, its pieces hide behind a 1x1 PNG,
// and the quality list comes from master.m3u8.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A real 1x1 PNG like the one megaplay's pieces start with (70 bytes).
var fakePNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89" +
	"\x00\x00\x00\rIDATx\xdacd\xf8\xcfP\x0f\x00\x03\x86\x01\x80Z4}k\x00\x00\x00\x00IEND\xaeB`\x82")

func TestPolicyRefererAndChoices(t *testing.T) {
	frame := "https://megaplay.buzz/stream/s-2/810433/sub?s=tcdn"
	page := "https://anikototv.bz/watch/tokyo-revengers/ep-1#x"
	got := refererChoices(frame, page)
	want := []refChoice{{refOrigin, "https://megaplay.buzz/"}, {refFrame, frame}, {refPage, "https://anikototv.bz/watch/tokyo-revengers/ep-1"}}
	if len(got) != len(want) {
		t.Fatalf("choices %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("choice %d: %v, want %v", i, got[i], want[i])
		}
	}
	alts := refererAlternatives("https://megaplay.buzz/", "https://megap.shiora.top/a/master.m3u8", frame, page)
	if len(alts) != 2 || alts[0] != frame {
		t.Fatalf("alternatives %v", alts)
	}
	if k := refererKind(frame, frame, page); k != refFrame {
		t.Fatalf("kind %q", k)
	}
	if got := otherReferers([]string{"a", "b", "c"}, "b", "x"); strings.Join(got, ",") != "x,a,c" {
		t.Fatalf("otherReferers %v", got)
	}
	for _, msg := range []string{"HTTP Error 403: Forbidden", "Got HTTP Error 403 caused by Cloudflare anti-bot challenge", msgStreamRefused} {
		if !isRefused(msg) {
			t.Fatalf("not seen as refused: %q", msg)
		}
	}
	if isRefused("HTTP Error 404: Not Found") {
		t.Fatal("404 is not a refusal")
	}
}

func TestRefererMemory(t *testing.T) {
	dir := t.TempDir()
	r := &refMemory{}
	r.setDir(dir)
	r.remember("CDN.example", refFrame)
	r2 := &refMemory{}
	r2.setDir(dir)
	if got := r2.get("cdn.example"); got != refFrame {
		t.Fatalf("not remembered after a restart: %q", got)
	}
	old := refMem
	refMem = r2
	defer func() { refMem = old }()
	j := &Job{URL: "https://cdn.example/x/master.m3u8", Referer: "https://player.example/"}
	j.useRememberedReferer("https://player.example/embed/9", "https://site.example/watch")
	if j.Referer != "https://player.example/embed/9" {
		t.Fatalf("remembered kind not used: %q", j.Referer)
	}
	if len(j.RefAlts) == 0 || j.RefAlts[0] != "https://player.example/" {
		t.Fatalf("the old referer is not an alternative: %v", j.RefAlts)
	}
}

func TestHiddenPieces(t *testing.T) {
	ts := bytes.Repeat(append([]byte{0x47}, make([]byte, 187)...), 5)
	if !hiddenTS(append(append([]byte{}, fakePNG...), ts...)) {
		t.Fatal("PNG + MPEG-TS not seen")
	}
	if hiddenTS(ts) {
		t.Fatal("plain MPEG-TS seen as hidden")
	}
	if hiddenTS(append(append([]byte{}, fakePNG...), bytes.Repeat([]byte{1}, 2000)...)) {
		t.Fatal("a real picture seen as video")
	}
	if !imageMagic([]byte{0xFF, 0xD8, 0xFF, 0xE0}) || imageMagic([]byte("#EXTM3U")) {
		t.Fatal("imageMagic")
	}
}

func TestMasterQualities(t *testing.T) {
	master := `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aac",NAME="Japanese",DEFAULT=YES,URI="https://c.example/a/audio/ja/index.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,AUDIO="aac",NAME="480p"
https://c.example/a/index-f3.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=5500000,RESOLUTION=1920x1080,AUDIO="aac",NAME="1080p"
https://c.example/a/index-f1.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2800000,RESOLUTION=1280x720,AUDIO="aac"
https://c.example/a/index-f2.m3u8
`
	qs := parseMaster(master)
	if len(qs) != 3 || qs[0].Height != 1080 || qs[2].Height != 360 || qs[0].URL != "https://c.example/a/index-f1.m3u8" {
		t.Fatalf("qualities %+v", qs)
	}
	opts := qualitiesFromMaster(qs, 1440)
	if len(opts) != 3 || opts[0].Label != "1080p" || opts[0].Size != 5500000/8*1440 {
		t.Fatalf("window qualities %+v", opts)
	}
	vi := &VideoInfo{Duration: 100, Qualities: []QualityOpt{{Height: 720, Label: "720p"}}}
	addMasterSizes(vi, qs)
	if vi.Qualities[0].Size != 2800000/8*100 {
		t.Fatalf("size not filled: %+v", vi.Qualities)
	}
	if got := siblingMaster("https://c.example/a/index-f1.m3u8?t=sub&token=x"); got != "https://c.example/a/master.m3u8?t=sub&token=x" {
		t.Fatalf("sibling %q", got)
	}
	if siblingMaster("https://c.example/a/master.m3u8") != "" {
		t.Fatal("a master has no sibling master")
	}
	if !samePlaylist("https://c.example/a/index-f1.m3u8?x=1", "https://c.example/a/index-f1.m3u8") {
		t.Fatal("samePlaylist")
	}
}

// megaServer acts like megap.shiora.top: everything needs the exact Referer
// want; pieces are MPEG-TS behind a PNG.
type megaServer struct {
	dir     string
	want    string
	refused atomic.Int64
	srv     *httptest.Server
}

func newMegaServer(t *testing.T, want string) *megaServer {
	ms := &megaServer{dir: makeHLS(t, 6), want: want}
	// Two qualities (the same pieces), a master next to them.
	src, err := os.ReadFile(filepath.Join(ms.dir, "index.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	pl := strings.ReplaceAll(string(src), ".ts", ".jpg")
	for _, n := range []string{"index-f1.m3u8", "index-f2.m3u8"} {
		if err := os.WriteFile(filepath.Join(ms.dir, n), []byte(pl), 0644); err != nil {
			t.Fatal(err)
		}
	}
	master := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=900000,RESOLUTION=320x180\nindex-f2.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720\nindex-f1.m3u8\n"
	if err := os.WriteFile(filepath.Join(ms.dir, "master.m3u8"), []byte(master), 0644); err != nil {
		t.Fatal(err)
	}
	ms.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Referer() != ms.want {
			ms.refused.Add(1)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		name := filepath.Base(r.URL.Path)
		if strings.HasSuffix(name, ".jpg") {
			data, err := os.ReadFile(filepath.Join(ms.dir, strings.TrimSuffix(name, ".jpg")+".ts"))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(append(append([]byte{}, fakePNG...), data...)))
			return
		}
		http.ServeFile(w, r, filepath.Join(ms.dir, name))
	}))
	t.Cleanup(ms.srv.Close)
	return ms
}

func waitVideoReady(t *testing.T, m *Manager, id string) VideoView {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		v, err := m.VideoPending(id)
		if err != nil {
			t.Fatal(err)
		}
		if v.State == "ready" || v.State == "error" {
			return v
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the video window never got its details")
	return VideoView{}
}

func probeCodecs(t *testing.T, p string) string {
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", p).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", p, err)
	}
	return strings.Join(strings.Fields(string(out)), ",")
}

// The whole way: a single-quality link sent with the player frame as
// referer and nothing captured. BDDM sends only the player's site, finds
// the master (two qualities), sees the hidden pieces and makes a real MP4
// and an MP3.
func TestMegaplayLikeStream(t *testing.T) {
	needVideoTools(t)
	ms := newMegaServer(t, "http://player.example/")
	m, dl := newTestManager(t, 4)
	if !m.tools.Ready() {
		t.Skip("video tools not found by BDDM")
	}
	frame := "http://player.example/stream/s-2/810433/sub?s=tcdn"
	add := func() string {
		v, err := m.AddVideoPending(AddRequest{URL: ms.srv.URL + "/index-f1.m3u8?t=sub", Referer: frame, Frame: frame,
			Page: "https://site.example/watch/show/ep-1", Kind: "video", Stream: "hls", Title: "Tokyo Revengers Episode 1 - Watch on Site.example"})
		if err != nil {
			t.Fatal(err)
		}
		return v.ID
	}
	id := add()
	v := waitVideoReady(t, m, id)
	if v.State != "ready" {
		t.Fatalf("window error: %s", v.Error)
	}
	if n := len(v.Info.Qualities); n != 2 || v.Info.Qualities[0].Height != 720 {
		t.Fatalf("qualities from the master: %+v", v.Info.Qualities)
	}
	m.mu.Lock()
	j := m.findLocked(id)
	ref, fix, u := j.Referer, j.TSFix, j.URL
	m.mu.Unlock()
	if ref != "http://player.example/" || !fix || !strings.HasSuffix(u, "/master.m3u8?t=sub") {
		t.Fatalf("referer %q, hidden pieces %v, url %q", ref, fix, u)
	}
	if _, err := m.StartVideo(id, VideoOptions{Quality: "best"}, dl, false); err != nil {
		t.Fatal(err)
	}
	done := waitDone(t, m, id, 2*time.Minute)
	if done.Status != StatusDone {
		t.Fatalf("%s: %s", done.Status, done.Error)
	}
	p := filepath.Join(dl, done.Name)
	if c := probeCodecs(t, p); !strings.Contains(c, "h264") || strings.Contains(c, "png") {
		t.Fatalf("%s has %s", done.Name, c)
	}
	if d := videoSeconds(t, p); d < 5 {
		t.Fatalf("video is %.1f s", d)
	}
	if !strings.HasPrefix(done.Name, "Tokyo Revengers Episode 1") {
		t.Fatalf("file name %q", done.Name)
	}

	// Audio only: the MP3 is made by BDDM from the hidden pieces.
	id = add()
	if v := waitVideoReady(t, m, id); v.State != "ready" {
		t.Fatalf("window error: %s", v.Error)
	}
	if _, err := m.StartVideo(id, VideoOptions{Quality: "mp3"}, dl, false); err != nil {
		t.Fatal(err)
	}
	done = waitDone(t, m, id, 2*time.Minute)
	if done.Status != StatusDone || !strings.HasSuffix(done.Name, ".mp3") {
		t.Fatalf("%s %q: %s", done.Status, done.Name, done.Error)
	}
	if c := probeCodecs(t, filepath.Join(done.Dir, done.Name)); c != "mp3" {
		t.Fatalf("mp3 has %s", c)
	}
}

// The server wants the player's full address: the first Referer (only the
// site) is refused, BDDM tries the others and remembers what worked.
func TestRefererRetryAfter403(t *testing.T) {
	needVideoTools(t)
	frame := "http://player2.example/e/77?autoplay=1" // http like the local test server
	ms := newMegaServer(t, frame)
	m, _ := newTestManager(t, 4)
	if !m.tools.Ready() {
		t.Skip("video tools not found by BDDM")
	}
	old := refMem
	refMem = &refMemory{}
	refMem.setDir(t.TempDir())
	defer func() { refMem = old }()
	v, err := m.AddVideoPending(AddRequest{URL: ms.srv.URL + "/master.m3u8", Referer: frame, Frame: frame,
		Page: "https://site2.example/w/1", Kind: "video", Stream: "hls", Title: "Clip"})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitVideoReady(t, m, v.ID); got.State != "ready" {
		t.Fatalf("window error: %s", got.Error)
	}
	m.mu.Lock()
	ref := m.findLocked(v.ID).Referer
	m.mu.Unlock()
	if ref != frame {
		t.Fatalf("referer %q", ref)
	}
	if ms.refused.Load() == 0 {
		m.mu.Lock()
		jj := m.findLocked(v.ID)
		t.Logf("alts %v frame %q page %q", jj.RefAlts, jj.refFrame, jj.refPage)
		m.mu.Unlock()
		t.Fatal("the site-only referer was never tried")
	}
	host := hostOfLink(ms.srv.URL)
	if k := refMem.get(host); k != refFrame {
		t.Fatalf("remembered %q", k)
	}
	// Next time BDDM starts with the full address.
	ms.refused.Store(0)
	v, _ = m.AddVideoPending(AddRequest{URL: ms.srv.URL + "/master.m3u8", Referer: frame, Frame: frame,
		Page: "https://site2.example/w/1", Kind: "video", Stream: "hls", Title: "Clip"})
	if got := waitVideoReady(t, m, v.ID); got.State != "ready" {
		t.Fatalf("window error: %s", got.Error)
	}
	if n := ms.refused.Load(); n != 0 {
		t.Fatalf("%d refused requests although the referer was remembered", n)
	}
}

func TestRepairHiddenFile(t *testing.T) {
	needVideoTools(t)
	dir := makeHLS(t, 2)
	ts, _ := os.ReadFile(filepath.Join(dir, "seg000.ts"))
	p := filepath.Join(t.TempDir(), "clip.mp4")
	os.WriteFile(p, append(append([]byte{}, fakePNG...), ts...), 0644)
	if !fileHidden(p) {
		t.Fatal("not seen as hidden")
	}
	np, err := repairHidden(context.Background(), mustLook(t, "ffmpeg"), p, "best")
	if err != nil {
		t.Fatal(err)
	}
	if np != p || fileHidden(np) || !strings.Contains(probeCodecs(t, np), "h264") {
		t.Fatalf("repaired %s: %s", np, probeCodecs(t, np))
	}
}

func mustLook(t *testing.T, n string) string {
	p, err := exec.LookPath(n)
	if err != nil {
		t.Skip(n + " missing")
	}
	return p
}
