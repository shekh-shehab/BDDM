package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func segDownload(parts []Part, size int64) *download {
	d := &download{job: &Job{}, State: &State{Size: size, Parts: parts}}
	return d
}

// A multi-part download: one segment per part, in file order (a part that was
// split off a slow one is appended last in State.Parts but belongs in the middle).
func TestSegSnapshotParts(t *testing.T) {
	parts := []Part{
		{From: 0, To: 99, Written: 100},   // done
		{From: 100, To: 199, Written: 40}, // downloading
		{From: 200, To: 299, Written: 10}, // reconnecting
		{From: 300, To: 399, Written: 0},  // not started
		{From: 150, To: 199, Written: 0},  // split off part 2 (appended later)
	}
	parts[1].To = 149 // part 2 was cut at 150
	d := segDownload(parts, 400)
	d.mu.Lock()
	d.liveLocked(1).assigned = true
	d.liveLocked(2).assigned = true
	d.liveLocked(2).retrying = true
	d.liveLocked(4).assigned = true
	info := d.segSnapshotLocked()
	d.mu.Unlock()
	if info == nil || info.Kind != segKindParts || len(info.Segs) != 5 {
		t.Fatalf("snapshot: %+v", info)
	}
	want := []SegView{
		{W: 100, Done: 100, State: segDone},
		{W: 50, Done: 40, State: segActive},
		{W: 50, Done: 0, State: segActive}, // the split-off part, by byte order
		{W: 100, Done: 10, State: segRetry},
		{W: 100, Done: 0, State: segWait},
	}
	for i, w := range want {
		if info.Segs[i] != w {
			t.Errorf("segment %d: got %+v want %+v", i, info.Segs[i], w)
		}
	}
	if info.Total != 5 || info.Finished != 1 || info.Active != 2 || info.Retrying != 1 {
		t.Errorf("counts: %+v", info)
	}
	if got := partsTextGo(info); got != "Parts: 2 active, 1 retrying, 1 done, 1 waiting" {
		t.Errorf("text %q", got)
	}
	// The weights add up to the file: each segment is as wide as its bytes.
	var sum int64
	for _, g := range info.Segs {
		sum += g.W
	}
	if sum != 400 {
		t.Errorf("segments cover %d of 400 bytes", sum)
	}
}

// partsTextGo mirrors partsText() of ui/index.html for the counts.
func partsTextGo(info *SegInfo) string {
	waiting := info.Total - info.Finished - info.Active - info.Retrying
	var bits []string
	add := func(n int, w string) {
		if n > 0 {
			bits = append(bits, strings.TrimSpace(strings.Join([]string{itoa(n), w}, " ")))
		}
	}
	add(info.Active, "active")
	add(info.Retrying, "retrying")
	bits = append(bits, itoa(info.Finished)+" done")
	add(waiting, "waiting")
	return "Parts: " + strings.Join(bits, ", ")
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestSegSnapshotPlainBar(t *testing.T) {
	one := segDownload([]Part{{From: 0, To: 999, Written: 10}}, 1000)
	one.mu.Lock()
	got := one.segSnapshotLocked()
	one.mu.Unlock()
	if got != nil {
		t.Errorf("a single connection should show a plain bar, got %+v", got)
	}
	unknown := segDownload([]Part{{From: 0, To: -1}, {From: 5, To: -1}}, 0)
	unknown.mu.Lock()
	got = unknown.segSnapshotLocked()
	unknown.mu.Unlock()
	if got != nil {
		t.Errorf("unknown size should show a plain bar, got %+v", got)
	}
	shared := segDownload([]Part{{From: 0, To: 4}, {From: 5, To: 9}}, 10)
	shared.shared = true // a video's several files: their sum is not one file
	shared.mu.Lock()
	got = shared.segSnapshotLocked()
	shared.mu.Unlock()
	if got != nil {
		t.Errorf("a shared download should show a plain bar, got %+v", got)
	}
}

// Not downloading (paused, queued, failed): no part is "active" any more, and
// a finished download shows no parts at all.
func TestSegsForStatus(t *testing.T) {
	j := &Job{}
	j.setSegs(&SegInfo{Kind: segKindParts, Total: 3, Finished: 1, Active: 1, Retrying: 1, Segs: []SegView{
		{W: 1, Done: 1, State: segDone}, {W: 1, State: segActive}, {W: 1, State: segRetry},
	}})
	if s := j.segsFor(StatusDownloading); s == nil || s.Active != 1 || s.Segs[1].State != segActive {
		t.Errorf("downloading: %+v", s)
	}
	p := j.segsFor(StatusPaused)
	if p == nil || p.Active != 0 || p.Retrying != 0 || p.Segs[1].State != segWait || p.Segs[2].State != segWait || p.Segs[0].State != segDone {
		t.Errorf("paused: %+v", p)
	}
	if j.segs.Segs[1].State != segActive {
		t.Error("the stored snapshot must not be changed by a view")
	}
	if j.segsFor(StatusDone) != nil {
		t.Error("done: no parts")
	}
	if (&Job{}).segsFor(StatusDownloading) != nil {
		t.Error("no snapshot: plain bar")
	}
}

func TestHLSSegInfoBuckets(t *testing.T) {
	state := make([]int8, 400)
	for i := 0; i < 130; i++ {
		state[i] = fragDone
	}
	state[130], state[131], state[132] = fragBusy, fragBusy, fragRetry
	info := hlsSegInfo(state, "video")
	if info == nil || info.Kind != segKindFragments || len(info.Segs) != maxFragmentBuckets {
		t.Fatalf("buckets: %+v", info)
	}
	if info.Total != 400 || info.Finished != 130 || info.Active != 2 || info.Retrying != 1 || info.Label != "video" {
		t.Errorf("counts: %+v", info)
	}
	var w, d int64
	for _, g := range info.Segs {
		w += g.W
		d += g.Done
	}
	if w != 400 || d != 130 {
		t.Errorf("weights %d done %d", w, d)
	}
	if info.Segs[0].State != segDone || info.Segs[len(info.Segs)-1].State != segWait {
		t.Errorf("first/last: %+v %+v", info.Segs[0], info.Segs[len(info.Segs)-1])
	}
	// bucket 13 holds fragments 130..139: partly busy and retrying
	if g := info.Segs[13]; g.State != segRetry || g.Done != 0 {
		t.Errorf("bucket 13: %+v", g)
	}
	small := hlsSegInfo(make([]int8, 5), "audio")
	if small == nil || len(small.Segs) != 5 {
		t.Errorf("5 fragments make 5 buckets: %+v", small)
	}
	if hlsSegInfo(make([]int8, 1), "video") != nil {
		t.Error("one fragment: plain bar")
	}
}

// A real download in several parts shows its parts while it runs, and the
// plain finished row afterwards. The snapshot is refreshed once a second.
func TestSegsLiveInRealDownload(t *testing.T) {
	data := randomData(t, 6<<20)
	ts := &testServer{data: data, fastRate: 1 << 20, slowRate: 1 << 20, slowStart: -1}
	srv := httptest.NewServer(ts)
	defer srv.Close()
	m, dl := newTestManager(t, 4)
	j, err := m.Add(AddRequest{URL: srv.URL + "/file.bin", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	var seen *SegInfo
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		v := jobView(m, j.ID)
		if v.Segs != nil && v.Status == StatusDownloading {
			seen = v.Segs
			if seen.Active >= 2 {
				break
			}
		}
		if v.Status == StatusDone || v.Status == StatusError {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if seen == nil || seen.Kind != segKindParts || len(seen.Segs) < 2 {
		t.Fatalf("no parts shown while downloading: %+v", seen)
	}
	b, _ := json.Marshal(jobView(m, j.ID))
	if !strings.Contains(string(b), `"segs"`) {
		t.Errorf("the row JSON should carry segs while downloading: %s", b)
	}
	waitStatus(t, m, j.ID, StatusDone, 2*time.Minute)
	checkFile(t, filepath.Join(dl, "file.bin"), data)
	if v := jobView(m, j.ID); v.Segs != nil {
		t.Errorf("a finished download shows no parts: %+v", v.Segs)
	}
}

// The row with parts is drawn as one segment per part (headless render): the
// bar, the "Parts:" line and the HLS fragments line, in both themes. The
// screenshots go to ui-shots/79-*.png.
func TestRowPartsRenderAndScreenshot(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no chrome for a screenshot")
	}
	m, dl := newTestManager(t, 4)
	now := time.Now()
	mk := func(id, name string, ago time.Duration, size, done int64, segs *SegInfo) *Job {
		j := &Job{ID: id, URL: "https://example.com/" + name, Name: name, Kind: "", Status: StatusDownloading,
			Size: size, Dir: dl, Started: now.Add(-ago)}
		j.done.Store(done)
		j.liveMu.Lock()
		j.speed = 6.2 * 1024 * 1024
		j.liveMu.Unlock()
		j.setSegs(segs)
		return j
	}
	parts := &SegInfo{Kind: segKindParts, Total: 8, Finished: 3, Active: 3, Retrying: 1, Segs: []SegView{
		{W: 100, Done: 100, State: segDone}, {W: 100, Done: 100, State: segDone}, {W: 60, Done: 60, State: segDone},
		{W: 40, Done: 15, State: segActive}, {W: 100, Done: 55, State: segActive}, {W: 100, Done: 30, State: segActive},
		{W: 100, Done: 12, State: segRetry}, {W: 100, Done: 0, State: segWait},
	}}
	state := make([]int8, 400)
	for i := 0; i < 210; i++ {
		state[i] = fragDone
	}
	state[210], state[211], state[212] = fragBusy, fragBusy, fragBusy
	state[213] = fragRetry
	frags := hlsSegInfo(state, "video")
	jobs := []*Job{
		mk("r-parts", "Ubuntu-desktop-amd64.iso", 3*time.Minute, 5<<30, 2<<30, parts),
		mk("r-hls", "Live event recording.mp4", 2*time.Minute, 0, 310<<20, frags),
		mk("r-one", "setup-single-connection.exe", time.Minute, 300<<20, 120<<20, nil),
	}
	m.mu.Lock()
	m.jobs = append(jobs, m.jobs...)
	m.mu.Unlock()
	base := startUITestServer(t, m)

	for _, theme := range []string{"light", "dark"} {
		out := filepath.Join("ui-shots", "79-parts-"+theme+".png")
		chromeShot(t, chrome, base+"/?scheme="+theme, out, 1000, 720)
	}
	cmd := execChromeDump(chrome, base+"/?scheme=light", 1000, 700)
	dom, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	s := string(dom)
	if n := len(regexp.MustCompile(`<i class="s-(done|active|retry|wait)"`).FindAllString(s, -1)); n != 8+maxFragmentBuckets {
		t.Errorf("segments drawn: %d, want %d (8 parts + %d fragment buckets)", n, 8+maxFragmentBuckets, maxFragmentBuckets)
	}
	for _, want := range []string{
		"Parts: 3 active, 1 retrying, 3 done",
		"Fragments (video): 210 of 400 done, 3 active, 1 retrying",
		"s-retry", "s-active", "s-done", "s-wait",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
	// The single-connection row is one plain bar and has no parts line.
	if !regexp.MustCompile(`seg rt single`).MatchString(s) {
		t.Error("a single-connection row should be a plain bar")
	}
	assertEnglishUI(t, "parts text", s[:0]+partsTextGo(parts))
	if _, err := os.Stat(filepath.Join("ui-shots", "79-parts-light.png")); err != nil {
		t.Error(err)
	}
}
