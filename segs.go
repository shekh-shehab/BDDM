package main

// segs.go: real per-part progress for the download row (item 79).
//
// The row's bar used to be 8 cosmetic blocks of the overall percent. Now a
// file downloaded in several parts shows one segment per part, as wide as its
// byte range, with its own fill, and a protocol like HLS shows its fragments
// (bucketed). The snapshot is built once a second by the progress loop (not
// on every byte) and only read when the UI asks for the list, so it stays
// cheap. A single-connection download has no segments: the UI draws one bar.

import (
	"sort"
	"sync"
	"time"
)

const (
	segActive = "active" // a connection is downloading it
	segDone   = "done"
	segRetry  = "retry" // the connection failed or was replaced; trying again
	segWait   = "wait"  // not started, or no connection works on it now

	segKindParts     = "parts"
	segKindFragments = "fragments"

	maxFragmentBuckets = 40
	segPublishEvery    = 700 * time.Millisecond
)

// SegView is one segment of the bar: W is its weight (bytes of a part, or
// fragments of a bucket), Done how much of W is finished.
type SegView struct {
	W     int64  `json:"w"`
	Done  int64  `json:"d"`
	State string `json:"s"`
}

// SegInfo is what the UI draws. Kind is "parts" or "fragments".
type SegInfo struct {
	Kind string    `json:"kind"`
	Segs []SegView `json:"segs"`
	// Total/Finished count parts or fragments (not buckets).
	Total    int    `json:"total"`
	Finished int    `json:"finished"`
	Active   int    `json:"active"`
	Retrying int    `json:"retrying"`
	Label    string `json:"label,omitempty"` // "video" / "audio" for HLS
}

// segCounts fills Total, Finished, Active and Retrying from the segments of a
// parts bar (one segment is one part).
func (s *SegInfo) countParts() {
	s.Total, s.Finished, s.Active, s.Retrying = len(s.Segs), 0, 0, 0
	for _, g := range s.Segs {
		switch g.State {
		case segDone:
			s.Finished++
		case segActive:
			s.Active++
		case segRetry:
			s.Retrying++
		}
	}
}

// idle returns a copy for a job that is not downloading right now (paused,
// queued, failed): no connection is working, so nothing is active or retrying.
func (s *SegInfo) idle() *SegInfo {
	if s == nil {
		return nil
	}
	c := *s
	c.Segs = make([]SegView, len(s.Segs))
	for i, g := range s.Segs {
		if g.State == segActive || g.State == segRetry {
			g.State = segWait
		}
		c.Segs[i] = g
	}
	c.Active, c.Retrying = 0, 0
	return &c
}

func (j *Job) setSegs(s *SegInfo) {
	j.liveMu.Lock()
	j.segs = s
	j.liveMu.Unlock()
}

// segsFor is the segment bar for the UI: only while there is something
// unfinished to show, and nothing is "active" unless the job is downloading.
func (j *Job) segsFor(status string) *SegInfo {
	j.liveMu.Lock()
	s := j.segs
	j.liveMu.Unlock()
	if s == nil || len(s.Segs) < 2 || status == StatusDone || status == StatusSeeding {
		return nil
	}
	if status != StatusDownloading {
		return s.idle()
	}
	return s
}

// segSnapshotLocked builds the bar of a multi-part download (d.mu held).
// It returns nil when one connection does the whole file: a plain bar.
func (d *download) segSnapshotLocked() *SegInfo {
	if d.shared || d.State.Size <= 0 || len(d.State.Parts) < 2 {
		return nil
	}
	idx := make([]int, 0, len(d.State.Parts))
	for i, p := range d.State.Parts {
		if p.To < 0 {
			return nil // size of a part unknown: cannot draw by bytes
		}
		idx = append(idx, i)
	}
	sort.Slice(idx, func(a, b int) bool { return d.State.Parts[idx[a]].From < d.State.Parts[idx[b]].From })
	info := &SegInfo{Kind: segKindParts, Segs: make([]SegView, 0, len(idx))}
	for _, i := range idx {
		p := d.State.Parts[i]
		lv := d.liveLocked(i)
		g := SegView{W: p.To - p.From + 1, Done: min(p.Written, p.To-p.From+1)}
		switch {
		case partFinished(p):
			g.State = segDone
		case lv.assigned && (lv.retrying || lv.replacing):
			g.State = segRetry
		case lv.assigned:
			g.State = segActive
		default:
			g.State = segWait
		}
		info.Segs = append(info.Segs, g)
	}
	info.countParts()
	return info
}

// publishSegs stores the current bar on the job (called once a second).
func (d *download) publishSegs() {
	d.mu.Lock()
	info := d.segSnapshotLocked()
	d.mu.Unlock()
	d.job.setSegs(info)
}

// markRetrying flags part idx as reconnecting (shown as "retry"); a new
// request clears it (setRequest).
func (d *download) markRetrying(idx int) {
	d.mu.Lock()
	if idx < len(d.State.Parts) {
		d.liveLocked(idx).retrying = true
	}
	d.mu.Unlock()
}

// ---------------------------------------------------------------------------
// HLS fragments
// ---------------------------------------------------------------------------

// hlsProgress tracks the fragments of one playlist (video or audio) of a
// native HLS download and publishes a bucketed bar, at most every
// segPublishEvery. It keeps its own copy of the states, so it never reads the
// downloader's slices.
type hlsProgress struct {
	j     *Job
	label string
	mu    sync.Mutex
	state []int8
}

const (
	fragNone  int8 = 0
	fragBusy  int8 = 1
	fragRetry int8 = 2
	fragDone  int8 = 3
)

func newHLSProgress(j *Job, label string, done []bool) *hlsProgress {
	p := &hlsProgress{j: j, label: label, state: make([]int8, len(done))}
	for i, ok := range done {
		if ok {
			p.state[i] = fragDone
		}
	}
	p.publish()
	return p
}

// set records the state of fragment i. Nothing is published here: start()
// publishes on a timer, so a fast stream does not flood the UI.
func (p *hlsProgress) set(i int, st int8) {
	p.mu.Lock()
	if i >= 0 && i < len(p.state) {
		p.state[i] = st
	}
	p.mu.Unlock()
}

// start publishes the bar every segPublishEvery until the returned function
// is called (which publishes the last state once more).
func (p *hlsProgress) start() (stop func()) {
	quit := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(segPublishEvery)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-t.C:
				p.publish()
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(quit)
			<-finished
			p.publish()
		})
	}
}

func (p *hlsProgress) publish() {
	p.mu.Lock()
	info := hlsSegInfo(p.state, p.label)
	p.mu.Unlock()
	p.j.setSegs(info)
}

// hlsSegInfo groups the fragments in at most maxFragmentBuckets buckets.
func hlsSegInfo(state []int8, label string) *SegInfo {
	n := len(state)
	if n < 2 {
		return nil
	}
	b := min(n, maxFragmentBuckets)
	info := &SegInfo{Kind: segKindFragments, Total: n, Label: label, Segs: make([]SegView, 0, b)}
	for k := 0; k < b; k++ {
		lo, hi := k*n/b, (k+1)*n/b
		g := SegView{W: int64(hi - lo)}
		busy, retry := false, false
		for i := lo; i < hi; i++ {
			switch state[i] {
			case fragDone:
				g.Done++
			case fragBusy:
				busy = true
				info.Active++
			case fragRetry:
				retry = true
				info.Retrying++
			}
		}
		switch {
		case g.Done == g.W:
			g.State = segDone
		case retry:
			g.State = segRetry
		case busy:
			g.State = segActive
		default:
			g.State = segWait
		}
		info.Finished += int(g.Done)
		info.Segs = append(info.Segs, g)
	}
	return info
}
