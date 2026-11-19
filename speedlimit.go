package main

// speedlimit.go: download and upload speed limits (roadmap item 4 / v1.4.7).
//
// Settings has a global cap for regular (HTTP) downloads, and global caps
// for torrent download and torrent upload (seeding). A per-download limit
// overrides the global one for that job only. 0 bytes/s means unlimited.
//
// HTTP transfers share a token-bucket (or a per-job bucket when overridden).
// Torrents use anacrolix's *rate.Limiter on the shared client. When a torrent
// has its own limit, that job is paced with a token bucket as well, because
// anacrolix only exposes client-wide limiters.

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// speedSteps is the 0–9 slider scale used in Settings and the per-download
// popover. Index 10 is Unlimited. The labeled values in the approved mockups
// are 100 KB/s (2), 200 KB/s (3), 2 MB/s (4), 5 MB/s (5) and Unlimited (10).
var speedSteps = [...]int64{
	25 << 10,  // 25 KB/s
	50 << 10,  // 50 KB/s
	100 << 10, // 100 KB/s
	200 << 10, // 200 KB/s
	2 << 20,   // 2 MB/s
	5 << 20,   // 5 MB/s
	10 << 20,  // 10 MB/s
	20 << 20,  // 20 MB/s
	50 << 20,  // 50 MB/s
	100 << 20, // 100 MB/s
}

const speedStepUnlimited = 10

// speedStepBPS is the bytes/s for slider position i (0–10). 0 means unlimited.
func speedStepBPS(i int) int64 {
	if i < 0 {
		i = 0
	}
	if i >= speedStepUnlimited || i >= len(speedSteps) {
		return 0
	}
	return speedSteps[i]
}

// speedStepIndex is the slider position nearest to bps. 0 bps is Unlimited.
func speedStepIndex(bps int64) int {
	if bps <= 0 {
		return speedStepUnlimited
	}
	best := 0
	bestDiff := abs64(speedSteps[0] - bps)
	for i := 1; i < len(speedSteps); i++ {
		d := abs64(speedSteps[i] - bps)
		if d < bestDiff {
			best, bestDiff = i, d
		}
	}
	return best
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// formatSpeedLimit is the label next to a slider ("5 MB/s", "Unlimited").
func formatSpeedLimit(bps int64) string {
	if bps <= 0 {
		return "Unlimited"
	}
	if bps%(1<<20) == 0 {
		return fmt.Sprintf("%d MB/s", bps>>20)
	}
	if bps%(1<<10) == 0 {
		return fmt.Sprintf("%d KB/s", bps>>10)
	}
	if bps >= 1<<20 {
		return fmt.Sprintf("%.1f MB/s", float64(bps)/(1<<20))
	}
	if bps >= 1<<10 {
		return fmt.Sprintf("%.1f KB/s", float64(bps)/(1<<10))
	}
	return fmt.Sprintf("%d B/s", bps)
}

func clampLimit(bps int64) int64 {
	if bps < 0 {
		return 0
	}
	return bps
}

func int64ptr(v int64) *int64 { return &v }

func effectiveLimit(override *int64, global int64) int64 {
	if override != nil {
		return clampLimit(*override)
	}
	return clampLimit(global)
}

// tokenBucket is a byte-per-second token bucket. rate 0 means unlimited.
type tokenBucket struct {
	mu     sync.Mutex
	rate   int64
	tokens float64
	burst  float64
	last   time.Time
}

func newTokenBucket(bps int64) *tokenBucket {
	b := &tokenBucket{}
	b.SetRate(bps)
	return b
}

func (b *tokenBucket) SetRate(bps int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.refillLocked(now)
	b.rate = clampLimit(bps)
	if b.rate > 0 {
		burst := float64(b.rate) // about one second
		if burst < 16*1024 {
			burst = 16 * 1024
		}
		b.burst = burst
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
	}
}

func (b *tokenBucket) Rate() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rate
}

func (b *tokenBucket) unlimited() bool {
	return b == nil || b.Rate() == 0
}

func (b *tokenBucket) refillLocked(now time.Time) {
	if b.rate <= 0 {
		b.last = now
		return
	}
	if b.last.IsZero() {
		b.tokens = b.burst
		b.last = now
		return
	}
	dt := now.Sub(b.last).Seconds()
	if dt > 0 {
		b.tokens += dt * float64(b.rate)
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
		b.last = now
	}
}

// Wait blocks until n tokens can be taken, or ctx is done.
func (b *tokenBucket) Wait(ctx context.Context, n int) error {
	if n <= 0 || b.unlimited() {
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		b.mu.Lock()
		if b.rate <= 0 {
			b.mu.Unlock()
			return nil
		}
		now := time.Now()
		b.refillLocked(now)
		need := float64(n)
		if b.tokens >= need {
			b.tokens -= need
			b.mu.Unlock()
			return nil
		}
		missing := need - b.tokens
		wait := time.Duration(missing / float64(b.rate) * float64(time.Second))
		b.mu.Unlock()
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// give returns unused tokens (for example after a short Read).
func (b *tokenBucket) give(n int) {
	if n <= 0 || b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rate <= 0 {
		return
	}
	b.refillLocked(time.Now())
	b.tokens += float64(n)
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
}

// takeDelta consumes up to n tokens that were already used (feedback pacing).
// It returns whether any tokens remain.
func (b *tokenBucket) takeDelta(n int64) bool {
	if b == nil || n < 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rate <= 0 {
		return true
	}
	b.refillLocked(time.Now())
	b.tokens -= float64(n)
	return b.tokens > 0
}

type limitedReader struct {
	ctx context.Context
	r   io.Reader
	b   *tokenBucket
}

func (r *limitedReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	ncap := len(p)
	if ncap > 32*1024 {
		ncap = 32 * 1024
	}
	if err := r.b.Wait(r.ctx, ncap); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p[:ncap])
	if n < ncap {
		r.b.give(ncap - n)
	}
	return n, err
}

func newRateLimiter(bps int64) *rate.Limiter {
	l := rate.NewLimiter(rate.Inf, 1<<20)
	setRateLimiter(l, bps)
	return l
}

func setRateLimiter(l *rate.Limiter, bps int64) {
	if l == nil {
		return
	}
	bps = clampLimit(bps)
	if bps <= 0 {
		l.SetLimit(rate.Inf)
		if l.Burst() < 1<<20 {
			l.SetBurst(1 << 20)
		}
		return
	}
	burst := int(bps)
	if burst < 32*1024 {
		burst = 32 * 1024
	}
	if burst > 1<<20 {
		burst = 1 << 20
	}
	// Burst must be set before a lower limit so a leftover burst is not huge.
	l.SetBurst(burst)
	l.SetLimit(rate.Limit(bps))
}

func (m *Manager) initSpeedLimits() {
	if m.httpLim == nil {
		m.httpLim = newTokenBucket(0)
	}
	if m.jobLim == nil {
		m.jobLim = map[string]*tokenBucket{}
	}
	if m.torDownLim == nil {
		m.torDownLim = newRateLimiter(0)
	}
	if m.torUpLim == nil {
		m.torUpLim = newRateLimiter(0)
	}
	if m.torDownShare == nil {
		m.torDownShare = newTokenBucket(0)
	}
	if m.torUpShare == nil {
		m.torUpShare = newTokenBucket(0)
	}
	m.applySpeedLimitsLocked()
}

func (m *Manager) applySpeedLimitsLocked() {
	s := m.settings
	m.httpLim.SetRate(s.HTTPDownloadLimit)
	anyOverride := false
	for _, j := range m.jobs {
		if j == nil {
			continue
		}
		if j.DlLimit != nil {
			if m.jobLim[j.ID] == nil {
				m.jobLim[j.ID] = newTokenBucket(*j.DlLimit)
			} else {
				m.jobLim[j.ID].SetRate(*j.DlLimit)
			}
		} else {
			delete(m.jobLim, j.ID)
		}
		if j.Kind == kindTorrent && (j.DlLimit != nil || j.UlLimit != nil) {
			if j.Status == StatusDownloading || j.Status == StatusSeeding || j.Status == StatusQueued {
				anyOverride = true
			}
		}
	}
	if !anyOverride {
		setRateLimiter(m.torDownLim, s.TorrentDownloadLimit)
		setRateLimiter(m.torUpLim, s.TorrentUploadLimit)
		m.torPacing = false
		m.torDownShare.SetRate(s.TorrentDownloadLimit)
		m.torUpShare.SetRate(s.TorrentUploadLimit)
		for _, j := range m.jobs {
			if j != nil && j.th != nil {
				j.th.AllowDataDownload()
				j.th.AllowDataUpload()
			}
		}
		return
	}
	// A per-torrent override cannot be expressed with the single client
	// limiter, so the client is left unlimited and each torrent is paced.
	setRateLimiter(m.torDownLim, 0)
	setRateLimiter(m.torUpLim, 0)
	m.torPacing = true
	m.torDownShare.SetRate(s.TorrentDownloadLimit)
	m.torUpShare.SetRate(s.TorrentUploadLimit)
}

func (m *Manager) httpBucketFor(j *Job) *tokenBucket {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j != nil && j.DlLimit != nil {
		b := m.jobLim[j.ID]
		if b == nil {
			b = newTokenBucket(*j.DlLimit)
			m.jobLim[j.ID] = b
		}
		return b
	}
	return m.httpLim
}

func (m *Manager) limitHTTPReader(ctx context.Context, j *Job, r io.Reader) io.Reader {
	b := m.httpBucketFor(j)
	if b.unlimited() {
		return r
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &limitedReader{ctx: ctx, r: r, b: b}
}

// SetJobSpeedLimit sets or clears the per-download limit. off clears it so
// the job uses the global Settings cap again. A non-nil limit of 0 means this
// job is unlimited, even if a global cap is set. Changes apply immediately.
func (m *Manager) SetJobSpeedLimit(id string, off bool, dl, ul *int64) (JobView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.findLocked(id)
	if j == nil || j.Removed {
		return JobView{}, fmt.Errorf("download not found")
	}
	if off {
		j.DlLimit = nil
		j.UlLimit = nil
	} else {
		if dl != nil {
			v := clampLimit(*dl)
			j.DlLimit = &v
		}
		if ul != nil {
			v := clampLimit(*ul)
			j.UlLimit = &v
		}
	}
	m.applySpeedLimitsLocked()
	m.dirty = true
	m.saveHistoryLocked()
	return j.viewLocked(), nil
}

func (m *Manager) paceTorrent(j *Job, done, uploaded int64) {
	m.mu.Lock()
	pacing := m.torPacing
	th := j.th
	s := m.settings
	m.mu.Unlock()
	if !pacing || th == nil {
		return
	}

	m.mu.Lock()
	dlBPS := effectiveLimit(j.DlLimit, s.TorrentDownloadLimit)
	ulBPS := effectiveLimit(j.UlLimit, s.TorrentUploadLimit)
	var dlB, ulB *tokenBucket
	if j.DlLimit != nil {
		if m.jobLim[j.ID] == nil {
			m.jobLim[j.ID] = newTokenBucket(dlBPS)
		}
		dlB = m.jobLim[j.ID]
	} else {
		dlB = m.torDownShare
	}
	if j.UlLimit != nil {
		key := j.ID + "/up"
		if m.jobLim[key] == nil {
			m.jobLim[key] = newTokenBucket(ulBPS)
		} else {
			m.jobLim[key].SetRate(ulBPS)
		}
		ulB = m.jobLim[key]
	} else {
		ulB = m.torUpShare
	}
	if !j.tPaceInit {
		j.tPaceDown, j.tPaceUp, j.tPaceInit = done, uploaded, true
		m.mu.Unlock()
		return
	}
	lastD, lastU := j.tPaceDown, j.tPaceUp
	j.tPaceDown, j.tPaceUp = done, uploaded
	m.mu.Unlock()

	dDelta := done - lastD
	uDelta := uploaded - lastU
	if dDelta < 0 {
		dDelta = 0
	}
	if uDelta < 0 {
		uDelta = 0
	}
	if dlBPS == 0 || dlB.takeDelta(dDelta) {
		th.AllowDataDownload()
	} else {
		th.DisallowDataDownload()
	}
	if ulBPS == 0 || ulB.takeDelta(uDelta) {
		th.AllowDataUpload()
	} else {
		th.DisallowDataUpload()
	}
}
