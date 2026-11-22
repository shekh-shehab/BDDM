package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMinLimit(t *testing.T) {
	if got := minLimit(0, 0); got != 0 {
		t.Fatalf("two unlimited = %d", got)
	}
	if got := minLimit(0, 100); got != 100 {
		t.Fatalf("unlimited + cap = %d", got)
	}
	if got := minLimit(50, 0); got != 50 {
		t.Fatalf("cap + unlimited = %d", got)
	}
	if got := minLimit(50, 80); got != 50 {
		t.Fatalf("min = %d", got)
	}
}

func TestYieldDisabledAndNoNICStayUnlimited(t *testing.T) {
	var e yieldEngine
	now := time.Unix(1000, 0)
	got := e.step(now, false, true, 1<<20, 0, 100, 0)
	if got.Down != 0 || got.Yielding {
		t.Fatalf("disabled: %+v", got)
	}
	got = e.step(now.Add(time.Second), true, false, 1<<20, 0, 100, 0)
	if got.Down != 0 || got.Yielding {
		t.Fatalf("no NIC: %+v", got)
	}
}

func TestYieldIgnoresBDDMOwnTraffic(t *testing.T) {
	var e yieldEngine
	now := time.Unix(2000, 0)
	// First sample: 10 MB already on the NIC, 10 MB already in BDDM.
	e.step(now, true, true, 10<<20, 0, 10<<20, 0)
	// Next second: NIC and BDDM both grow by 5 MB — that's only BDDM.
	got := e.step(now.Add(time.Second), true, true, 15<<20, 0, 15<<20, 0)
	if got.Yielding || got.Down != 0 {
		t.Fatalf("own traffic should not yield: %+v", got)
	}
}

func TestYieldRampsDownNeverZeroThenBackUp(t *testing.T) {
	var e yieldEngine
	now := time.Unix(3000, 0)
	// Establish a 10 MB/s link of only BDDM traffic.
	e.step(now, true, true, 0, 0, 0, 0)
	now = now.Add(time.Second)
	e.step(now, true, true, 10<<20, 0, 10<<20, 0)

	// Other apps suddenly use 3 MB/s; BDDM still 7 MB/s; NIC 10 MB/s.
	var last yieldCaps
	for i := 0; i < 12; i++ {
		now = now.Add(500 * time.Millisecond)
		nic := 10<<20 + uint64(i+1)*5<<20 // 10 MB/s over each 0.5s
		own := int64(10<<20) + int64(i+1)*int64(3.5*float64(1<<20))
		last = e.step(now, true, true, nic, 0, own, 0)
		if last.Down != 0 && last.Down < yieldMinBPS {
			t.Fatalf("cap dropped below minimum: %d", last.Down)
		}
	}
	if !last.Yielding {
		t.Fatalf("should be yielding after other traffic: %+v", last)
	}
	if last.Down <= 0 {
		t.Fatal("cap must never be zero while yielding")
	}

	// Other traffic stops: NIC growth equals BDDM growth only.
	nic := 10<<20 + uint64(12)*5<<20
	own := int64(10<<20) + int64(12)*int64(3.5*float64(1<<20))
	var sawUnlimited bool
	var prev int64 = last.Down
	for i := 0; i < 30; i++ {
		now = now.Add(500 * time.Millisecond)
		nic += 1 << 20 // 2 MB/s of only BDDM
		own += 1 << 20
		last = e.step(now, true, true, nic, 0, own, 0)
		if last.Down != 0 && last.Down < yieldMinBPS {
			t.Fatalf("cap dropped below minimum on the way up: %d", last.Down)
		}
		if last.Down > 0 && prev > 0 && last.Down < prev {
			t.Fatalf("cap decreased while other traffic was gone: %d -> %d at i=%d", prev, last.Down, i)
		}
		if last.Down > prev {
			prev = last.Down
		}
		if !last.Yielding && last.Down == 0 {
			sawUnlimited = true
			break
		}
	}
	if !sawUnlimited {
		t.Fatalf("should return to full speed after quiet period: %+v", last)
	}
}

func TestYieldHysteresisDoesNotOscillate(t *testing.T) {
	var e yieldEngine
	now := time.Unix(4000, 0)
	e.step(now, true, true, 0, 0, 0, 0)
	now = now.Add(time.Second)
	e.step(now, true, true, 8<<20, 0, 8<<20, 0)

	// Enter yield with a clear burst of other traffic, then sit in the
	// hysteresis band (between off and on) and check the cap holds still.
	now = now.Add(time.Second)
	got := e.step(now, true, true, 8<<20+3<<20, 0, 8<<20+int64(200<<10), 0) // other ≈ 2.8 MB/s
	if !got.Yielding || got.Down <= 0 {
		t.Fatalf("expected yield, got %+v", got)
	}
	held := got.Down
	// other ≈ 50 KB/s (between 32 and 80 KB/s thresholds)
	for i := 0; i < 8; i++ {
		now = now.Add(500 * time.Millisecond)
		nicAdd := uint64(held/2) + 50<<10
		ownAdd := held / 2
		got = e.step(now, true, true, 8<<20+3<<20+uint64(i+1)*nicAdd/2, 0, 8<<20+int64(200<<10)+int64(i+1)*ownAdd/2, 0)
		if got.Down != held && got.Down != 0 {
			// allow first-tick EMA to settle once; after that hold
			if i > 1 {
				t.Fatalf("cap oscillated in the hysteresis band: %d -> %d at i=%d", held, got.Down, i)
			}
			held = got.Down
		}
	}
}

func TestYieldUploadMirrorsDownload(t *testing.T) {
	var e yieldEngine
	now := time.Unix(5000, 0)
	e.step(now, true, true, 0, 0, 0, 0)
	now = now.Add(time.Second)
	e.step(now, true, true, 0, 5<<20, 0, 5<<20)
	now = now.Add(time.Second)
	got := e.step(now, true, true, 0, 10<<20, 0, 5<<20+int64(200<<10)) // ~4.8 MB/s other upload
	if !got.Yielding || got.Up <= 0 {
		t.Fatalf("upload should yield: %+v", got)
	}
	if got.Up < yieldMinBPS {
		t.Fatalf("upload cap below minimum: %d", got.Up)
	}
}

func TestYieldWrapsHTTPReaderLive(t *testing.T) {
	m, _ := newTestManager(t, 1)
	defer m.Shutdown()
	m.yieldDown.SetRate(0)
	src := bytes.Repeat([]byte("x"), 64*1024)
	r := m.limitHTTPReader(context.Background(), &Job{ID: "n"}, bytes.NewReader(src))
	// Turning the cap on after the reader is created must still apply.
	m.yieldDown.SetRate(20 << 10)
	start := time.Now()
	n, err := io.Copy(io.Discard, r)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(src)) {
		t.Fatalf("copied %d", n)
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Fatalf("yield cap did not slow a running reader (%s)", time.Since(start))
	}
}

func TestYieldSettingsDefaultOnAndPersist(t *testing.T) {
	if !defaultSettings().YieldToOther {
		t.Fatal("YieldToOther should be on by default")
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "cfg")
	if err := os.MkdirAll(cfg, 0755); err != nil {
		t.Fatal(err)
	}
	// An older settings file without the field must keep the default (on).
	raw := []byte(`{"downloadDir":"` + filepath.ToSlash(filepath.Join(dir, "dl")) + `","parts":8,"maxActive":3}`)
	if err := os.WriteFile(filepath.Join(cfg, "settings.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()
	if !m.Settings().YieldToOther {
		t.Fatal("missing yieldToOther in settings.json should stay on")
	}

	st := m.Settings()
	st.YieldToOther = false
	if _, err := m.SetSettings(st); err != nil {
		t.Fatal(err)
	}
	if m.Settings().YieldToOther {
		t.Fatal("turning the setting off should persist")
	}
}

func TestYieldListAPIAndOffSetting(t *testing.T) {
	m, _ := newTestManager(t, 1)
	defer m.Shutdown()
	srv := httptest.NewServer(newServer(m, "0", func() {}, nil))
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/api/list")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["yielding"]; !ok {
		t.Fatal("/api/list should include yielding")
	}

	st := m.Settings()
	st.YieldToOther = false
	if _, err := m.SetSettings(st); err != nil {
		t.Fatal(err)
	}
	m.yieldEng = &yieldEngine{have: true, capDown: 100 << 10, yieldingDown: true}
	m.yieldTick()
	if m.Yielding() {
		t.Fatal("yield should stop when the setting is off")
	}
	if m.yieldDownCap != 0 {
		t.Fatalf("cap should be unlimited when off: %d", m.yieldDownCap)
	}
}

func TestYieldProxyLimitsHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("z"), 80*1024))
	}))
	t.Cleanup(upstream.Close)

	down := newTokenBucket(64 << 10)
	up := newTokenBucket(0)
	addr, stop, err := listenYieldProxy(down, up)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)

	proxyURL, err := url.Parse("http://" + addr)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 8 * time.Second}
	start := time.Now()
	res, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if n < 80*1024 {
		t.Fatalf("short body %d", n)
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Fatalf("proxy did not apply the yield cap (%s)", time.Since(start))
	}
}

func TestParseNetDevAndDefaultRoute(t *testing.T) {
	name, in, out, ok := parseNetDevLine("  eth0: 1234 1 0 0 0 0 0 0 5678 1 0 0 0 0 0 0")
	if !ok || name != "eth0" || in != 1234 || out != 5678 {
		t.Fatalf("parseNetDevLine: %s %d %d %v", name, in, out, ok)
	}
	dir := t.TempDir()
	route := filepath.Join(dir, "route")
	dev := filepath.Join(dir, "dev")
	if err := os.WriteFile(route, []byte("Iface Destination Gateway Flags Ref Use Metric Mask\neth0 00000000 0100A8C0 0003 0 0 100 00000000\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dev, []byte("Inter-|   Receive                                                |  Transmit\n"+
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n"+
		"    lo:  100 1 0 0 0 0 0 0  100 1 0 0 0 0 0 0\n"+
		"  eth0:  999 2 0 0 0 0 0 0  888 2 0 0 0 0 0 0\n"+
		"  eth1:  111 2 0 0 0 0 0 0  222 2 0 0 0 0 0 0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	in, out, ok = linuxNICCounters(route, dev)
	if !ok || in != 999 || out != 888 {
		t.Fatalf("linuxNICCounters: in=%d out=%d ok=%v", in, out, ok)
	}
}

func TestYieldUIHasToggleAndIndicator(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("ui", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		"Slow down downloads while I use the internet",
		`id="setYield"`,
		"yieldToOther",
		"Saving bandwidth",
		"yieldHint",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("ui/index.html missing %q", want)
		}
	}
}

func TestYieldProxyAddrSafeUnderLock(t *testing.T) {
	m, _ := newTestManager(t, 1)
	defer m.Shutdown()
	done := make(chan struct{})
	go func() {
		m.mu.Lock()
		_ = m.yieldProxyAddr()
		_, doneArgs := m.baseArgs(&Job{})
		doneArgs()
		m.mu.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("yieldProxyAddr/baseArgs deadlocked while holding the manager lock")
	}
}
