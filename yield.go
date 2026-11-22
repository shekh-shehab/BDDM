package main

// yield.go: automatically slow BDDM when other programs use the network
// (item 68). A shared limiter is applied live to HTTP parts (regular
// downloads, Google Drive, fast YouTube) and to torrent download/upload.
// yt-dlp is pointed at a local proxy that uses the same limiter.
//
// Windows measures NIC throughput with GetIfEntry2 (octet counters of the
// default-route interface). BDDM's own bytes are subtracted so only other
// traffic triggers a slowdown. The cap never goes to zero; it ramps down
// and up smoothly with hysteresis so it does not oscillate.

import (
	"context"
	"io"
	"sync/atomic"
	"time"
)

const (
	yieldTick     = 500 * time.Millisecond
	yieldMinBPS   = 64 << 10 // never stall a download completely
	yieldOtherOn  = 80 << 10 // other traffic that starts a slowdown
	yieldOtherOff = 32 << 10 // below this is "quiet" (hysteresis)
	yieldQuietFor = 2 * time.Second
	yieldHeadroom = 128 << 10 // leave this much spare for other apps
	yieldRampDown = 0.72      // each tick, move this fraction toward the lower target
	yieldRampUp   = 256 << 10 // added to the cap each tick while ramping up
	yieldFirstCut = 0.5       // first cap as a fraction of BDDM's current speed
)

// nicCounters returns the interface's cumulative in/out octets.
// ok is false when the system counters cannot be read.
var nicCounters = systemNICCounters

// yieldCaps is the live global cap. 0 means unlimited.
type yieldCaps struct {
	Down     int64
	Up       int64
	Yielding bool
}

// yieldEngine holds the ramp state. It is pure (clock and counters are
// passed in) so tests can drive it with fake NIC and BDDM samples.
type yieldEngine struct {
	lastT                  time.Time
	lastNicIn, lastNicOut  uint64
	lastOwnDown, lastOwnUp int64
	have                   bool
	otherDown, otherUp     float64
	capDown, capUp         int64
	quietDown, quietUp     time.Time
	peakDown, peakUp       float64
	yieldingDown           bool
	yieldingUp             bool
}

func (e *yieldEngine) step(now time.Time, enabled, nicOK bool, nicIn, nicOut uint64, ownDown, ownUp int64) yieldCaps {
	if !enabled || !nicOK {
		e.have = false
		e.capDown, e.capUp = 0, 0
		e.yieldingDown, e.yieldingUp = false, false
		e.quietDown, e.quietUp = time.Time{}, time.Time{}
		e.otherDown, e.otherUp = 0, 0
		return yieldCaps{}
	}
	if !e.have {
		e.lastT, e.lastNicIn, e.lastNicOut = now, nicIn, nicOut
		e.lastOwnDown, e.lastOwnUp = ownDown, ownUp
		e.have = true
		return yieldCaps{Down: e.capDown, Up: e.capUp, Yielding: e.yieldingDown || e.yieldingUp}
	}
	dt := now.Sub(e.lastT).Seconds()
	if dt < 0.05 {
		return yieldCaps{Down: e.capDown, Up: e.capUp, Yielding: e.yieldingDown || e.yieldingUp}
	}
	nicDown := perSec(delta64(nicIn, e.lastNicIn), dt)
	nicUp := perSec(delta64(nicOut, e.lastNicOut), dt)
	bddmDown := perSecSigned(ownDown-e.lastOwnDown, dt)
	bddmUp := perSecSigned(ownUp-e.lastOwnUp, dt)
	e.lastT, e.lastNicIn, e.lastNicOut = now, nicIn, nicOut
	e.lastOwnDown, e.lastOwnUp = ownDown, ownUp

	otherDown := nicDown - bddmDown
	if otherDown < 0 {
		otherDown = 0
	}
	otherUp := nicUp - bddmUp
	if otherUp < 0 {
		otherUp = 0
	}
	e.otherDown = 0.5*e.otherDown + 0.5*otherDown
	e.otherUp = 0.5*e.otherUp + 0.5*otherUp

	e.peakDown = updatePeak(e.peakDown, nicDown, e.yieldingDown)
	e.peakUp = updatePeak(e.peakUp, nicUp, e.yieldingUp)

	e.capDown, e.yieldingDown, e.quietDown = rampOne(e.capDown, e.yieldingDown, e.quietDown, now, e.otherDown, e.peakDown, bddmDown)
	e.capUp, e.yieldingUp, e.quietUp = rampOne(e.capUp, e.yieldingUp, e.quietUp, now, e.otherUp, e.peakUp, bddmUp)

	return yieldCaps{Down: e.capDown, Up: e.capUp, Yielding: e.yieldingDown || e.yieldingUp}
}

func updatePeak(peak, nic float64, yielding bool) float64 {
	if nic > peak {
		return nic
	}
	if yielding || peak <= 0 {
		return peak
	}
	// Slowly forget an old peak so a later, slower link is not overestimated.
	return peak*0.995 + nic*0.005
}

func rampOne(cap int64, yielding bool, quiet time.Time, now time.Time, other, peak, bddm float64) (int64, bool, time.Time) {
	if other >= float64(yieldOtherOn) {
		target := int64(peak - other - float64(yieldHeadroom))
		if peak <= 0 || target < yieldMinBPS {
			// No good capacity estimate yet: cut BDDM's current speed in half.
			cut := int64(bddm * yieldFirstCut)
			if cut > target {
				target = cut
			}
		}
		if target < yieldMinBPS {
			target = yieldMinBPS
		}
		if cap <= 0 {
			start := int64(bddm)
			if start < target {
				start = target
			}
			if start < yieldMinBPS {
				start = yieldMinBPS
			}
			cap = start
		}
		if target < cap {
			next := int64(float64(cap)*yieldRampDown + float64(target)*(1-yieldRampDown))
			if next < target {
				next = target
			}
			cap = next
		}
		if cap < yieldMinBPS {
			cap = yieldMinBPS
		}
		return cap, true, time.Time{}
	}
	if other > float64(yieldOtherOff) {
		// Hysteresis band: hold the current cap.
		return cap, yielding, quiet
	}
	// Other traffic is quiet.
	if !yielding && cap <= 0 {
		return 0, false, time.Time{}
	}
	if quiet.IsZero() {
		return cap, yielding, now
	}
	if now.Sub(quiet) < yieldQuietFor {
		return cap, yielding, quiet
	}
	if cap <= 0 {
		return 0, false, quiet
	}
	cap += yieldRampUp
	// Once the cap is well above what BDDM is actually using, drop it.
	if float64(cap) >= 2*bddm && (peak <= 0 || float64(cap) >= peak*0.85) {
		return 0, false, time.Time{}
	}
	return cap, true, quiet
}

func delta64(now, last uint64) uint64 {
	if now >= last {
		return now - last
	}
	// Counter reset or wrap: skip this interval.
	return 0
}

func perSec(n uint64, dt float64) float64 {
	if dt <= 0 {
		return 0
	}
	return float64(n) / dt
}

func perSecSigned(n int64, dt float64) float64 {
	if n < 0 {
		n = 0
	}
	return perSec(uint64(n), dt)
}

func minLimit(a, b int64) int64 {
	a, b = clampLimit(a), clampLimit(b)
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	if a < b {
		return a
	}
	return b
}

func (m *Manager) initYield() {
	if m.yieldDown == nil {
		m.yieldDown = newTokenBucket(0)
	}
	if m.yieldUp == nil {
		m.yieldUp = newTokenBucket(0)
	}
	if m.yieldStop == nil {
		m.yieldStop = make(chan struct{})
	}
	// Start the yt-dlp proxy now so later baseArgs (often called with m.mu
	// held) can read the address without locking.
	if addr, stop, err := listenYieldProxy(m.yieldDown, m.yieldUp); err == nil {
		m.yieldProxy = addr
		m.yieldProxyStop = stop
	}
	go m.yieldLoop()
}

func (m *Manager) stopYield() {
	m.mu.Lock()
	ch := m.yieldStop
	m.yieldStop = nil
	stopProxy := m.yieldProxyStop
	m.yieldProxyStop = nil
	m.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	if stopProxy != nil {
		stopProxy()
	}
}

func (m *Manager) yieldLoop() {
	t := time.NewTicker(yieldTick)
	defer t.Stop()
	for {
		m.mu.Lock()
		stop := m.yieldStop
		m.mu.Unlock()
		if stop == nil {
			return
		}
		select {
		case <-stop:
			return
		case <-t.C:
			m.yieldTick()
		}
	}
}

func (m *Manager) yieldTick() {
	m.mu.Lock()
	if m.closing || m.yieldStop == nil {
		m.mu.Unlock()
		return
	}
	enabled := m.settings.YieldToOther
	ownD, ownU := m.ownBytesLocked()
	eng := m.yieldEng
	if eng == nil {
		eng = &yieldEngine{}
		m.yieldEng = eng
	}
	m.mu.Unlock()

	nicIn, nicOut, ok := nicCounters()
	caps := eng.step(time.Now(), enabled, ok, nicIn, nicOut, ownD, ownU)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return
	}
	m.yieldDown.SetRate(caps.Down)
	m.yieldUp.SetRate(caps.Up)
	changed := m.yieldDownCap != caps.Down || m.yieldUpCap != caps.Up
	m.yieldDownCap, m.yieldUpCap = caps.Down, caps.Up
	atomic.StoreInt32(&m.yielding, boolToInt(caps.Yielding))
	if changed {
		m.applySpeedLimitsLocked()
	}
}

func boolToInt(v bool) int32 {
	if v {
		return 1
	}
	return 0
}

func (m *Manager) Yielding() bool { return atomic.LoadInt32(&m.yielding) != 0 }

func (m *Manager) ownBytesLocked() (down, up int64) {
	for _, j := range m.jobs {
		if j == nil || j.Removed {
			continue
		}
		down += j.done.Load()
		if j.Torrent != nil {
			up += j.Torrent.Uploaded
		}
	}
	return
}

func (m *Manager) wrapYield(ctx context.Context, r io.Reader) io.Reader {
	if m == nil || m.yieldDown == nil {
		return r
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &limitedReader{ctx: ctx, r: r, b: m.yieldDown}
}

// yieldProxyAddr returns the localhost proxy for yt-dlp. It does not lock,
// because video code often calls it while already holding m.mu.
func (m *Manager) yieldProxyAddr() string {
	if m == nil {
		return ""
	}
	return m.yieldProxy
}
