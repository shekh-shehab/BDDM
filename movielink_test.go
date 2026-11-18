package main

// 1.4.2 part 4: movielinkbd.tv-like streams. The master playlist is at
// /m/<id> (no ".m3u8"), sent as image/jpeg; qualities are at /hls/...;
// without the player site's Origin/Referer the server answers 403
// "#EXT-X-ERROR:forbidden".

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type mlServer struct {
	dir     string
	refused atomic.Int64
	origins atomic.Int64
	srv     *httptest.Server
}

func mlAllowed(r *http.Request) bool {
	for _, v := range []string{r.Header.Get("Origin"), r.Header.Get("Referer")} {
		if u, err := url.Parse(v); err == nil && (u.Hostname() == "movielinkbd.tv" || u.Hostname() == "movielinkbd.at") {
			return true
		}
	}
	return false
}

func newMLServer(t *testing.T) *mlServer {
	ms := &mlServer{dir: makeHLS(t, 5)}
	src, err := os.ReadFile(filepath.Join(ms.dir, "index.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	// The quality playlist has no extension; its pieces are in /hls/v1/.
	if err := os.WriteFile(filepath.Join(ms.dir, "v1"), src, 0644); err != nil {
		t.Fatal(err)
	}
	master := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=600000,RESOLUTION=320x180\n/hls/v1/index\n"
	ms.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			ms.origins.Add(1)
		}
		if !mlAllowed(r) {
			ms.refused.Add(1)
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("#EXTM3U\n#EXT-X-ERROR:forbidden\n"))
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/m/"):
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte(master))
		case r.URL.Path == "/hls/v1/index":
			w.Header().Set("Content-Type", "image/jpeg")
			http.ServeFile(w, r, filepath.Join(ms.dir, "v1"))
		case strings.HasPrefix(r.URL.Path, "/hls/v1/"):
			http.ServeFile(w, r, filepath.Join(ms.dir, filepath.Base(r.URL.Path)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ms.srv.Close)
	return ms
}

func TestPlaylistErrorIsRefusal(t *testing.T) {
	if playlistStatus(200, []byte("#EXTM3U\n#EXT-X-ERROR:forbidden\n")) != 403 {
		t.Fatal("#EXT-X-ERROR not seen as a refusal")
	}
	if playlistStatus(200, []byte("#EXTM3U\n#EXTINF:4,\na.ts\n")) != 200 {
		t.Fatal("a real playlist refused")
	}
}

func TestMovielinkLikeStream(t *testing.T) {
	needVideoTools(t)
	ms := newMLServer(t)
	m, dl := newTestManager(t, 4)
	if !m.tools.Ready() {
		t.Skip("video tools not found by BDDM")
	}
	old := refMem
	refMem = &refMemory{}
	refMem.setDir(t.TempDir())
	defer func() { refMem = old }()
	stream := ms.srv.URL + "/m/aGVsbG8td29ybGQ"

	// 1. The player is on the movie page itself: its site goes as Referer
	// and Origin.
	page := "http://movielinkbd.tv/movie/some-film-2026/"
	v, err := m.AddVideoPending(AddRequest{URL: stream, Referer: page, Frame: page, Page: page, Kind: "video", Stream: "hls",
		Title: "Some Film (2026) Bangla Subtitle - MovieLinkBD"})
	if err != nil {
		t.Fatal(err)
	}
	got := waitVideoReady(t, m, v.ID)
	if got.State != "ready" || len(got.Info.Qualities) != 1 {
		t.Fatalf("window: %s %q %+v", got.State, got.Error, got.Info)
	}
	if ms.origins.Load() == 0 {
		t.Fatal("no Origin was sent")
	}
	if _, err := m.StartVideo(v.ID, VideoOptions{Quality: "best"}, dl, false); err != nil {
		t.Fatal(err)
	}
	done := waitDone(t, m, v.ID, 2*time.Minute)
	if done.Status != StatusDone {
		t.Fatalf("%s: %s", done.Status, done.Error)
	}
	if d := videoSeconds(t, filepath.Join(done.Dir, done.Name)); d < 4 {
		t.Fatalf("video is %.1f s", d)
	}

	// 2. The player is in a frame from another site: the frame is refused,
	// the movie page (the tab) works.
	ms.refused.Store(0)
	frame := "http://player.example/embed/9"
	v, err = m.AddVideoPending(AddRequest{URL: stream, Referer: frame, Frame: frame, Page: page, Kind: "video", Stream: "hls", Title: "Some Film"})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitVideoReady(t, m, v.ID); got.State != "ready" {
		t.Fatalf("window: %q", got.Error)
	}
	m.mu.Lock()
	ref := m.findLocked(v.ID).Referer
	m.mu.Unlock()
	if ref != page || ms.refused.Load() == 0 {
		t.Fatalf("referer %q after %d refusals", ref, ms.refused.Load())
	}
}
