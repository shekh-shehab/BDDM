package main

// pieces.go: where each piece of a download comes from and how fast.
//
//   - Mirrors (A8): a download can have extra links to the same file (the
//     same size). The pieces are spread over all of them; a mirror that
//     fails is dropped and its pieces go to the others.
//   - Slow connections (A9): a connection much slower than the others is
//     closed and opened again (often to a faster server or path), without
//     counting as a failed try.
//   - Pieces with their own checksum (A7) are checked as they arrive.

import (
	"context"
	"errors"
	"hash"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------- mirrors

// maxMirrors: extra links per download.
const maxMirrors = 8

// cleanMirrors keeps valid http(s) links that differ from the main link.
func cleanMirrors(main string, in []string) []string {
	seen := map[string]bool{strings.TrimSpace(main): true}
	var out []string
	for _, raw := range in {
		for _, l := range strings.Fields(raw) {
			l = strings.TrimSpace(l)
			if seen[l] || !(strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")) {
				continue
			}
			seen[l] = true
			out = append(out, l)
			if len(out) == maxMirrors {
				return out
			}
		}
	}
	return out
}

// usableMirrors asks every mirror for its size; only mirrors with the same
// size that can send pieces are used.
func usableMirrors(ctx context.Context, j *Job, client *http.Client, hdr reqHeaders, mirrors []string, size int64) []string {
	if len(mirrors) == 0 || size <= 0 {
		return nil
	}
	ok := make([]bool, len(mirrors))
	var wg sync.WaitGroup
	for i, u := range mirrors {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			info, err := probe(c, client, u, hdr.forMirror()) // the browser's cookies are for the main site
			switch {
			case err != nil:
				log.Printf("[%s] mirror %s does not work: %v", j.displayName(), u, err)
			case info.Size != size:
				log.Printf("[%s] mirror %s has another file size (%d, not %d); not used", j.displayName(), u, info.Size, size)
			case !info.CanRange:
				log.Printf("[%s] mirror %s cannot send pieces; not used", j.displayName(), u)
			default:
				ok[i] = true
			}
		}(i, u)
	}
	wg.Wait()
	var out []string
	for i, u := range mirrors {
		if ok[i] {
			out = append(out, u)
		}
	}
	return out
}

// sourceURL returns link i (0 = the main link), wrapping around.
func (d *download) sourceURL(i int) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.sources) == 0 {
		return d.mainURL()
	}
	i %= len(d.sources)
	if i < 0 || d.srcDead[i] {
		i = 0
	}
	return d.sources[i]
}

func (d *download) mainURL() string {
	if d.fetchURL != "" {
		return d.fetchURL
	}
	return d.State.URL
}

// setSources sets the main link and the working mirrors.
func (d *download) setSources(mirrors []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sources = append([]string{d.mainURL()}, mirrors...)
	d.srcDead = make([]bool, len(d.sources))
	d.srcFails = make([]int, len(d.sources))
	d.srcActive = make([]int, len(d.sources))
}

// takeLocked gives part idx to a connection, on the least busy link.
func (d *download) takeLocked(idx int, now time.Time) {
	lv := d.liveLocked(idx)
	*lv = partLive{assigned: true, since: now}
	lv.src = d.pickSourceLocked()
	if lv.src < len(d.srcActive) {
		d.srcActive[lv.src]++
	}
}

func (d *download) pickSourceLocked() int {
	best := 0
	for i := range d.sources {
		if d.srcDead[i] {
			continue
		}
		if d.srcActive[i] < d.srcActive[best] || d.srcDead[best] {
			best = i
		}
	}
	return best
}

func (d *download) dropSourceLocked(lv *partLive) {
	if lv.assigned && lv.src < len(d.srcActive) && d.srcActive[lv.src] > 0 {
		d.srcActive[lv.src]--
	}
}

// partSource: the link part idx downloads from.
func (d *download) partSource(idx int) (int, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	src := d.liveLocked(idx).src
	if src >= len(d.sources) || (len(d.srcDead) > src && d.srcDead[src]) {
		return 0, d.mainURL()
	}
	if len(d.sources) == 0 {
		return 0, d.mainURL()
	}
	return src, d.sources[src]
}

// mirrorFailed: part idx failed on a mirror. The mirror is dropped after an
// error answer or 3 connection failures, and the part moves to another
// link. Returns false when the part was on the main link.
func (d *download) mirrorFailed(idx int, err error) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	lv := d.liveLocked(idx)
	src := lv.src
	if src == 0 || src >= len(d.sources) {
		return false
	}
	d.srcFails[src]++
	if !d.srcDead[src] && (!isNetErr(err) || d.srcFails[src] >= 3) {
		d.srcDead[src] = true
		log.Printf("[%s] mirror %s failed (%v); using the other links", d.job.Name, d.sources[src], err)
	}
	if d.srcActive[src] > 0 {
		d.srcActive[src]--
	}
	lv.src = d.pickSourceLocked()
	d.srcActive[lv.src]++
	return true
}

// mirrorsInUse: links still used (the main link included).
func (d *download) mirrorsInUse() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for i := range d.sources {
		if !d.srcDead[i] {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------- slow connections

var (
	slowReplace = true            // tests turn it off
	slowRatio   = 4.0             // "slow": this many times slower than the typical connection
	slowGrace   = 6 * time.Second // a new connection gets this long to speed up
	slowFor     = 5 * time.Second // and must stay slow this long to be replaced
	slowMaxPer  = 3               // replacements per part
)

// errReplaced: the connection was closed because it was too slow.
var errReplaced = errors.New("slow connection replaced")

// checkSlow runs every second: it measures every connection and reconnects
// one that is far slower than the others.
func (d *download) checkSlow(now time.Time) {
	if !slowReplace {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var rates []float64
	var active []int
	for i := range d.State.Parts {
		lv := d.liveLocked(i)
		if !lv.assigned || lv.cancel == nil {
			continue
		}
		inst := float64(lv.got - lv.lastGot)
		lv.lastGot = lv.got
		if lv.rate == 0 {
			lv.rate = inst
		} else {
			lv.rate = 0.5*inst + 0.5*lv.rate
		}
		if now.Sub(lv.since) >= slowGrace {
			rates = append(rates, lv.rate)
			active = append(active, i)
		}
	}
	var typical float64
	switch {
	case len(rates) >= 2:
		sorted := append([]float64(nil), rates...)
		sort.Float64s(sorted)
		typical = sorted[len(sorted)/2] // the median (with two: the faster one)
		d.typicalRate = typical
	case len(rates) == 1 && d.typicalRate > 0:
		// The other connections already finished: compare the last one
		// with the speed they had.
		typical = d.typicalRate
	default:
		return
	}
	if typical < 64<<10 {
		return // everything is slow: the network, not one connection
	}
	for k, i := range active {
		lv := &d.live[i]
		pt := d.State.Parts[i]
		left := int64(1 << 62)
		if pt.To >= 0 {
			left = pt.To - (pt.From + pt.Written) + 1
		}
		if rates[k]*slowRatio >= typical || left < minSteal || lv.replaced >= slowMaxPer {
			lv.slowSince = time.Time{}
			continue
		}
		if lv.slowSince.IsZero() {
			lv.slowSince = now
			continue
		}
		if now.Sub(lv.slowSince) < slowFor {
			continue
		}
		lv.replaced++
		lv.replacing = true
		lv.slowSince = time.Time{}
		log.Printf("[%s] part %d: %.0f KB/s while the others do %.0f KB/s; reconnecting it", d.job.Name, i+1, rates[k]/1024, typical/1024)
		lv.cancel()
	}
}

// startRequestLocked records the cancel function of part idx's request.
func (d *download) setRequest(idx int, cancel context.CancelFunc) {
	d.mu.Lock()
	lv := d.liveLocked(idx)
	lv.cancel = cancel
	lv.replacing = false
	d.mu.Unlock()
}

// endRequest: the request of part idx ended. Reports whether it was closed
// because it was slow; then the part starts again fresh.
func (d *download) endRequest(idx int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	lv := d.liveLocked(idx)
	lv.cancel = nil
	if !lv.replacing {
		return false
	}
	lv.replacing = false
	lv.since, lv.got, lv.lastGot, lv.rate = time.Now(), 0, 0, 0
	d.replacedSlow++
	// Another link may be faster for this part.
	if len(d.sources) > 1 {
		d.dropSourceLocked(lv)
		lv.src = (lv.src + 1) % len(d.sources)
		for d.srcDead[lv.src] {
			lv.src = (lv.src + 1) % len(d.sources)
		}
		d.srcActive[lv.src]++
	}
	return true
}

// ---------------------------------------------------------------- piece checksums

// hashWriter feeds what was really written to a hash.
type hashWriter struct {
	w io.Writer
	h hash.Hash
	n int64
}

func (hw *hashWriter) Write(p []byte) (int, error) {
	n, err := hw.w.Write(p)
	if n > 0 {
		hw.h.Write(p[:n])
		hw.n += int64(n)
	}
	return n, err
}

// contentRange parses "bytes a-b/size".
func contentRange(s string) (from, to int64, ok bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "bytes ") {
		return 0, 0, false
	}
	r, _, _ := strings.Cut(strings.TrimPrefix(s, "bytes "), "/")
	a, b, found := strings.Cut(r, "-")
	if !found {
		return 0, 0, false
	}
	f, err1 := strconv.ParseInt(a, 10, 64)
	t, err2 := strconv.ParseInt(b, 10, 64)
	return f, t, err1 == nil && err2 == nil && t >= f
}

// noteChecksum keeps the file checksum the main server sends (not for
// Google Drive, whose links and answers change).
func (d *download) noteChecksum(resp *http.Response, src int, rangeRequested bool) {
	if src != 0 || d.fetchURL != "" || resp.StatusCode >= 300 {
		return
	}
	c := fileChecksum(resp.Header, resp.StatusCode == http.StatusOK && !rangeRequested)
	if c == nil {
		return
	}
	d.mu.Lock()
	if d.State.Sum == nil {
		d.State.Sum = c
	}
	d.mu.Unlock()
}
