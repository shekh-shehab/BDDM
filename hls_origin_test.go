package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamOriginPrefersCapturedThenFrame(t *testing.T) {
	j := &Job{extra: map[string]string{"Origin": "https://player.example"}}
	if got := streamOrigin(j); got != "https://player.example" {
		t.Fatalf("captured Origin: %q", got)
	}
	j = &Job{Referer: "https://from-referer.example/p/1", refFrame: "https://player.example/p/1"}
	if got := streamOrigin(j); got != "https://from-referer.example" {
		t.Fatalf("Referer wins when no captured Origin: %q", got)
	}
	j = &Job{Referer: "", refFrame: "https://qmr7cm.movielinkbd.at/p/abc", refPage: "https://qmr7cm.movielinkbd.at/download18plus/x"}
	if got := streamOrigin(j); got != "https://qmr7cm.movielinkbd.at" {
		t.Fatalf("empty Referer should use player frame: %q", got)
	}
	j = &Job{Referer: "", refFrame: "", refPage: "https://site.example/watch"}
	if got := streamOrigin(j); got != "https://site.example" {
		t.Fatalf("page fallback: %q", got)
	}
	if got := streamOrigin(&Job{}); got != "" {
		t.Fatalf("empty job: %q", got)
	}
}

func packDisguisedFMP4(t *testing.T, src string) (blob []byte, media, master string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(src, "index.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	pl, err := parseMediaPlaylist(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	var mapOff, mapLen int
	if pl.Map != nil {
		b, err := os.ReadFile(filepath.Join(src, filepath.Base(pl.Map.URL)))
		if err != nil {
			t.Fatal(err)
		}
		mapOff = buf.Len()
		buf.Write(b)
		mapLen = len(b)
	}
	type br struct{ off, ln int }
	ranges := make([]br, len(pl.Segs))
	for i, s := range pl.Segs {
		b, err := os.ReadFile(filepath.Join(src, filepath.Base(s.URL)))
		if err != nil {
			t.Fatal(err)
		}
		ranges[i] = br{buf.Len(), len(b)}
		buf.Write(b)
	}
	var b strings.Builder
	td := pl.Target
	if td <= 0 {
		td = 2
	}
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:%g\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-MEDIA-SEQUENCE:0\n", td)
	if pl.Map != nil {
		fmt.Fprintf(&b, "#EXT-X-MAP:URI=\"clip.jpg\",BYTERANGE=\"%d@%d\"\n", mapLen, mapOff)
	}
	for i, s := range pl.Segs {
		d := s.Dur
		if d <= 0 {
			d = 1
		}
		fmt.Fprintf(&b, "#EXTINF:%0.3f,\n#EXT-X-BYTERANGE:%d@%d\nclip.jpg\n", d, ranges[i].ln, ranges[i].off)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	media = b.String()
	master = "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=400000,RESOLUTION=320x180\n/hls/token\n"
	return buf.Bytes(), media, master
}

func serveDisguisedHLS(t *testing.T, wantOrigin, master, media string, blob []byte) (*httptest.Server, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	var withOrigin, missingOrigin atomic.Int64
	var masterBody, mediaBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Header.Get("Origin") != wantOrigin {
			missingOrigin.Add(1)
			w.Header().Set("Content-Type", "image/jpeg")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-ERROR:forbidden\n"))
			return
		}
		withOrigin.Add(1)
		switch {
		case strings.HasPrefix(r.URL.Path, "/m/"):
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte(masterBody))
		case strings.HasPrefix(r.URL.Path, "/hls/"):
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte(mediaBody))
		case strings.HasPrefix(r.URL.Path, "/s/"):
			http.ServeContent(w, r, "clip.jpg", time.Time{}, bytes.NewReader(blob))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	masterBody = strings.ReplaceAll(master, "/hls/token", srv.URL+"/hls/token")
	mediaBody = strings.ReplaceAll(media, "clip.jpg", srv.URL+"/s/clip.jpg")
	return srv, &withOrigin, &missingOrigin
}

func addHLSJobWithOrigin(m *Manager, u, dir, title, quality, origin, frame, page, referer string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	extra := map[string]string{}
	if origin != "" {
		extra["Origin"] = origin
	}
	j := &Job{ID: newID(), URL: u, Kind: "video", Stream: "hls", Name: title, Dir: dir, Parts: 4,
		Status: StatusQueued, Started: time.Now(), Video: &VideoOptions{Quality: quality, Title: title},
		Referer: referer, extra: extra, refFrame: frame, refPage: page}
	m.jobs = append(m.jobs, j)
	m.scheduleLocked()
	return j.ID
}

func TestNativeHLSOriginRequiredJPEGAndByteRange(t *testing.T) {
	needFFmpeg(t)
	src := makeFMP4HLS(t, 4)
	blob, media, master := packDisguisedFMP4(t, src)
	if !strings.Contains(media, "#EXT-X-BYTERANGE:") || !strings.Contains(media, "clip.jpg") {
		t.Fatalf("packed media: %s", media)
	}
	wantOrigin := "https://player.example"
	const frame = "https://player.example/p/abc"
	const page = "https://site.example/watch/1"

	t.Run("captured Origin, empty Referer", func(t *testing.T) {
		srv, with, missing := serveDisguisedHLS(t, wantOrigin, master, media, blob)
		m, dl := newTestManager(t, 4)
		if m.tools.Find("ffmpeg") == "" {
			t.Skip("ffmpeg not found by BDDM")
		}
		id := addHLSJobWithOrigin(m, srv.URL+"/m/token", dl, "Origin Clip", "best", wantOrigin, frame, page, "")
		v := waitDone(t, m, id, 2*time.Minute)
		if v.Status != StatusDone {
			t.Fatalf("%s: %s", v.Status, v.Error)
		}
		if with.Load() == 0 {
			t.Fatal("server never saw the required Origin")
		}
		if missing.Load() != 0 {
			t.Fatalf("requests were sent without Origin (%d)", missing.Load())
		}
		if d := videoSeconds(t, filepath.Join(dl, "Origin Clip.mp4")); d < 3 {
			t.Fatalf("duration %.1f", d)
		}
	})

	t.Run("Origin from player frame, empty Referer and extra", func(t *testing.T) {
		srv, with, missing := serveDisguisedHLS(t, wantOrigin, master, media, blob)
		m, dl := newTestManager(t, 4)
		if m.tools.Find("ffmpeg") == "" {
			t.Skip("ffmpeg not found by BDDM")
		}
		id := addHLSJobWithOrigin(m, srv.URL+"/m/token", dl, "Frame Origin Clip", "best", "", frame, page, "")
		v := waitDone(t, m, id, 2*time.Minute)
		if v.Status != StatusDone {
			t.Fatalf("%s: %s", v.Status, v.Error)
		}
		if with.Load() == 0 {
			t.Fatal("server never saw the required Origin")
		}
		if missing.Load() != 0 {
			t.Fatalf("requests were sent without Origin (%d)", missing.Load())
		}
		if d := videoSeconds(t, filepath.Join(dl, "Frame Origin Clip.mp4")); d < 3 {
			t.Fatalf("duration %.1f", d)
		}
	})

	t.Run("no Origin at all is refused", func(t *testing.T) {
		srv, _, missing := serveDisguisedHLS(t, wantOrigin, master, media, blob)
		m, _ := newTestManager(t, 1)
		j := &Job{URL: srv.URL + "/m/token", Stream: "hls"}
		status, body, err := m.fetchPlaylist(context.Background(), j, j.URL)
		if err != nil {
			t.Fatal(err)
		}
		if status != 403 {
			t.Fatalf("status %d body %q", status, body)
		}
		if !bytes.Contains(body, []byte("#EXT-X-ERROR:forbidden")) {
			t.Fatalf("body %q", body)
		}
		if missing.Load() == 0 {
			t.Fatal("expected a request without Origin")
		}
	})
}

const liveMovielinkPage = "https://qmr7cm.movielinkbd.at/download18plus/mKs_r32wlccbvg7sb4snzhzqnkmxbu"

func TestLiveMovielinkbdDisguisedPlaylist(t *testing.T) {
	if os.Getenv("BDDM_SKIP_LIVE") == "1" {
		t.Skip("BDDM_SKIP_LIVE=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	page := liveMovielinkPage
	origin := "https://qmr7cm.movielinkbd.at"
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0"
	get := func(raw string, extra map[string]string) (int, []byte, string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return 0, nil, "", err
		}
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Accept", "*/*")
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, nil, "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		return resp.StatusCode, b, resp.Header.Get("Content-Type"), err
	}

	status, body, _, err := get(page, nil)
	if err != nil {
		t.Skip("page: ", err)
	}
	if status >= 400 {
		t.Skipf("page HTTP %d", status)
	}
	iframeRe := regexp.MustCompile(`(?i)<iframe[^>]+src=["']([^"']+)["']`)
	m := iframeRe.FindSubmatch(body)
	if m == nil {
		t.Skip("page has no player iframe (layout may have changed)")
	}
	iframe := string(m[1])
	if strings.HasPrefix(iframe, "/") {
		iframe = origin + iframe
	}
	st, pbody, _, err := get(iframe, map[string]string{"Origin": origin})
	if err != nil || st >= 400 {
		t.Skipf("player iframe HTTP %d %v", st, err)
	}
	masterRe := regexp.MustCompile(`https://watch\.movielink\.tv/m/[^\s"'<>]+`)
	master := string(masterRe.Find(pbody))
	if master == "" {
		t.Skip("player HTML has no watch.movielink.tv/m/ URL")
	}
	t.Logf("master %s", master)

	st0, body0, _, err := get(master, nil)
	if err != nil {
		t.Fatalf("master without Origin: %v", err)
	}
	if st0 != 403 && !bytes.Contains(body0, []byte("forbidden")) {
		t.Logf("master without Origin: HTTP %d body %q (expected 403 forbidden)", st0, trimForLog(body0))
	} else {
		t.Logf("master without Origin: HTTP %d (forbidden, as expected)", st0)
	}

	st1, body1, ct, err := get(master, map[string]string{"Origin": origin})
	if err != nil {
		t.Fatalf("master with Origin: %v", err)
	}
	if st1 >= 400 {
		t.Fatalf("master with Origin HTTP %d body %q", st1, trimForLog(body1))
	}
	text, err := cleanPlaylist(body1, master)
	if err != nil {
		t.Fatalf("master with Origin was not a playlist (ct=%s): %v body %q", ct, err, trimForLog(body1))
	}
	qs := parseMaster(text)
	if len(qs) == 0 {
		t.Fatalf("master has no variants: %s", text)
	}
	t.Logf("live master HTTP %d ct=%s qualities=%d highest=%dp", st1, ct, len(qs), qs[0].Height)

	m2, dl := newTestManager(t, 2)
	j := &Job{
		URL: master, Stream: "hls", extra: map[string]string{"Origin": origin},
		refFrame: iframe, refPage: page,
	}
	stP, pbytes, err := m2.fetchPlaylist(ctx, j, master)
	if err != nil || stP >= 400 {
		t.Fatalf("BDDM fetchPlaylist with Origin: HTTP %d %v body %q", stP, err, trimForLog(pbytes))
	}
	mediaURL := qs[0].URL
	if !strings.HasPrefix(mediaURL, "http") {
		// should already be absolute from parseMaster? check
	}
	stM, mbytes, err := m2.fetchPlaylist(ctx, j, mediaURL)
	if err != nil || stM >= 400 {
		t.Fatalf("media playlist with Origin: HTTP %d %v body %q", stM, err, trimForLog(mbytes))
	}
	ctext, err := cleanPlaylist(mbytes, mediaURL)
	if err != nil {
		t.Fatalf("media playlist was not HLS: %v body %q", err, trimForLog(mbytes))
	}
	mpl, err := parseMediaPlaylist(ctext)
	if err != nil {
		t.Fatalf("media: %v", err)
	}
	if len(mpl.Segs) == 0 {
		t.Fatal("media playlist has no segments")
	}
	seg := mpl.Segs[0]
	raw, statusSeg, err := m2.hlsGet(ctx, j, seg, 32<<10)
	if err != nil {
		t.Fatalf("segment with Origin: %v", err)
	}
	if statusSeg >= 400 {
		t.Fatalf("segment HTTP %d", statusSeg)
	}
	if len(raw) < 16 {
		t.Fatalf("segment too small: %d bytes", len(raw))
	}
	t.Logf("live media %d segs, first segment HTTP %d %d bytes (fmp4=%v range=%d+%d)",
		len(mpl.Segs), statusSeg, len(raw), mpl.FMP4, seg.Off, seg.Len)
	_ = dl
}

func trimForLog(b []byte) string {
	s := string(b)
	if len(s) > 240 {
		s = s[:240] + "..."
	}
	return s
}
