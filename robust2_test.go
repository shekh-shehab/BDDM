package main

// Tests for the robustness items A6-A13 (netwatch.go, verify.go, pieces.go,
// queue.go, explain.go, video.go).

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// xServer serves one file with ranges and hooks for the tests.
type xServer struct {
	data     []byte
	rate     int64                                // bytes/s per answer (0 = full speed)
	rateFor  func(from int64, n int64) int64      // per answer rate (overrides rate)
	headers  func(h http.Header, from, to int64)  // extra headers
	mutate   func(from int64, body []byte) []byte // change what is sent (damage)
	requests atomic.Int64
	served   atomic.Int64
	mu       sync.Mutex
	starts   []int64
}

func (xs *xServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	size := int64(len(xs.data))
	from, to := int64(0), size-1
	partial := false
	if rh := r.Header.Get("Range"); strings.HasPrefix(rh, "bytes=") {
		a, b, _ := strings.Cut(strings.TrimPrefix(rh, "bytes="), "-")
		from, _ = strconv.ParseInt(a, 10, 64)
		if b != "" {
			to, _ = strconv.ParseInt(b, 10, 64)
		}
		to = min(to, size-1)
		partial = true
	}
	if r.Method == http.MethodGet {
		xs.requests.Add(1)
		xs.mu.Lock()
		xs.starts = append(xs.starts, from)
		xs.mu.Unlock()
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(to-from+1, 10))
	if xs.headers != nil {
		xs.headers(w.Header(), from, to)
	}
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, to, size))
		w.WriteHeader(http.StatusPartialContent)
	}
	if r.Method == http.MethodHead {
		return
	}
	body := append([]byte(nil), xs.data[from:to+1]...)
	if xs.mutate != nil {
		body = xs.mutate(from, body)
	}
	rate := xs.rate
	if xs.rateFor != nil {
		rate = xs.rateFor(from, to-from+1)
	}
	const chunk = 16 * 1024
	start := time.Now()
	var sent int64
	for off := 0; off < len(body); off += chunk {
		end := min(off+chunk, len(body))
		if _, err := w.Write(body[off:end]); err != nil {
			return
		}
		sent += int64(end - off)
		xs.served.Add(int64(end - off))
		if rate > 0 {
			if want := time.Duration(float64(sent) / float64(rate) * float64(time.Second)); want > time.Since(start) {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(want - time.Since(start)):
				}
			}
		}
	}
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// ---------------------------------------------------------------- A6

// The internet goes away in the middle of a download and comes back: the
// download waits (it does not fail or use up its tries) and continues
// where it stopped.
func TestNetworkLossResumes(t *testing.T) {
	var online atomic.Bool
	online.Store(true)
	oldOnline, oldEvery := netOnline, onlineEvery
	netOnline = func(ctx context.Context, server string) bool { return online.Load() }
	onlineEvery = 100 * time.Millisecond
	defer func() { netOnline, onlineEvery = oldOnline, oldEvery }()

	data := randomData(t, 8<<20)
	xs := &xServer{data: data, rate: 1 << 20}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	srv := &http.Server{Handler: xs}
	go srv.Serve(ln)

	m, dl := newTestManager(t, 4)
	j, _ := m.Add(AddRequest{URL: "http://" + addr + "/f.bin", Force: true})
	waitBytes(t, m, j.ID, 2<<20)

	// The network goes down.
	online.Store(false)
	srv.Close()
	deadline := time.Now().Add(15 * time.Second)
	for !jobView(m, j.ID).NetWait {
		if time.Now().After(deadline) {
			t.Fatalf("not waiting for the network: %+v", jobView(m, j.ID))
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(time.Second)
	v := jobView(m, j.ID)
	if v.Status != StatusDownloading || !strings.Contains(v.Message, "No internet connection") {
		t.Fatalf("while offline: %s %q", v.Status, v.Message)
	}
	before := xs.served.Load()

	// And comes back.
	ln2, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv2 := &http.Server{Handler: xs}
	go srv2.Serve(ln2)
	defer srv2.Close()
	online.Store(true)
	v = waitDone(t, m, j.ID, time.Minute)
	if v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	checkFile(t, filepath.Join(dl, "f.bin"), data)
	again := xs.served.Load() - before
	t.Logf("before the loss: %d bytes; after: %d bytes (file %d)", before, again, len(data))
	if before+again > int64(len(data))+(1<<20) {
		t.Fatalf("it downloaded too much again: %d of %d", before+again, len(data))
	}
}

// ---------------------------------------------------------------- A7

func TestParseChecksums(t *testing.T) {
	sum := sha256.Sum256([]byte("abc"))
	hx := hex.EncodeToString(sum[:])
	for _, in := range []string{hx, "sha256:" + hx, "SHA-256 " + hx, strings.ToUpper(hx), hx + "  file.iso", "sha256=" + hx} {
		c, err := parseUserChecksum(in)
		if err != nil || c.Algo != "sha256" || c.Hex != hx {
			t.Errorf("%q: %+v %v", in, c, err)
		}
	}
	if c, err := parseUserChecksum("900150983cd24fb0d6963f7d28e17f72"); err != nil || c.Algo != "md5" {
		t.Errorf("md5: %+v %v", c, err)
	}
	for _, bad := range []string{"xyz", "sha256:" + hx[:10], "md5:" + hx} {
		if _, err := parseUserChecksum(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	m5 := md5.Sum([]byte("abc"))
	h := http.Header{}
	h.Set("Digest", "MD5="+b64(m5[:])+", SHA-256="+b64(sum[:]))
	if c := fileChecksum(h, false); c == nil || c.Algo != "sha256" || c.Hex != hx {
		t.Errorf("Digest: %+v", c)
	}
	h = http.Header{}
	h.Set("Repr-Digest", "sha-256=:"+b64(sum[:])+":")
	if c := fileChecksum(h, false); c == nil || c.Hex != hx {
		t.Errorf("Repr-Digest: %+v", c)
	}
	h = http.Header{}
	h.Add("X-Goog-Hash", "crc32c=n03x6A==")
	h.Add("X-Goog-Hash", "md5="+b64(m5[:]))
	if c := fileChecksum(h, false); c == nil || c.Algo != "md5" {
		t.Errorf("x-goog-hash: %+v", c)
	}
	h = http.Header{}
	h.Set("Content-MD5", b64(m5[:]))
	if c := fileChecksum(h, false); c != nil {
		t.Errorf("Content-MD5 of a piece used for the whole file: %+v", c)
	}
	if c := fileChecksum(h, true); c == nil || c.Algo != "md5" {
		t.Errorf("Content-MD5 of the whole file: %+v", c)
	}
	h = http.Header{}
	h.Set("X-Checksum-Sha1", "a9993e364706816aba3e25717850c26c9cd0d89d")
	if c := fileChecksum(h, false); c == nil || c.Algo != "sha1" {
		t.Errorf("X-Checksum-Sha1: %+v", c)
	}
}

// The server sends the file's SHA-256; one piece arrives damaged. BDDM
// notices, downloads only the damaged piece again, and the file is right.
func TestChecksumRepairsDamagedPiece(t *testing.T) {
	old := repairBlock
	repairBlock = 1 << 20
	defer func() { repairBlock = old }()
	data := randomData(t, 6<<20)
	sum := sha256.Sum256(data)
	bad := int64(3<<20 + 12345) // this byte is damaged the first time
	var damaged atomic.Bool
	xs := &xServer{data: data,
		headers: func(h http.Header, from, to int64) { h.Set("Digest", "SHA-256="+b64(sum[:])) },
		mutate: func(from int64, body []byte) []byte {
			if from <= bad && bad < from+int64(len(body)) && damaged.CompareAndSwap(false, true) {
				body[bad-from] ^= 0xff
			}
			return body
		}}
	srv := httptest.NewServer(xs)
	defer srv.Close()
	m, dl := newTestManager(t, 4)
	j, _ := m.Add(AddRequest{URL: srv.URL + "/f.bin", Force: true})
	v := waitDone(t, m, j.ID, time.Minute)
	if v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	checkFile(t, filepath.Join(dl, "f.bin"), data)
	if !strings.Contains(v.Checked, "SHA-256 matches the server's checksum") || !strings.Contains(v.Checked, "1 damaged piece") {
		t.Fatalf("checked: %q", v.Checked)
	}
	// Repair compared every piece (6 MB more) but the file had only 1 bad one.
	t.Logf("served %d bytes for a %d-byte file; %s", xs.served.Load(), len(data), v.Checked)
}

func TestUserChecksum(t *testing.T) {
	data := randomData(t, 2<<20)
	sum := sha256.Sum256(data)
	xs := &xServer{data: data}
	srv := httptest.NewServer(xs)
	defer srv.Close()
	m, dl := newTestManager(t, 4)

	j, err := m.Add(AddRequest{URL: srv.URL + "/ok.bin", Force: true, Hash: "sha256:" + hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	v := waitDone(t, m, j.ID, time.Minute)
	if v.Status != StatusDone || v.Checked != "SHA-256 matches your checksum" {
		t.Fatalf("%s %q %s", v.Status, v.Checked, v.Error)
	}

	wrong := sha256.Sum256([]byte("another file"))
	j, _ = m.Add(AddRequest{URL: srv.URL + "/bad.bin", Force: true, Hash: hex.EncodeToString(wrong[:])})
	v = waitDone(t, m, j.ID, time.Minute)
	if v.Status != StatusError || !strings.Contains(v.Reason, "does not match the checksum you entered") || !strings.Contains(v.Fix, "exact file") {
		t.Fatalf("%s reason=%q fix=%q err=%q", v.Status, v.Reason, v.Fix, v.Error)
	}
	checkFile(t, filepath.Join(dl, "bad.bin"), data) // kept for the user to decide

	if _, err := m.Add(AddRequest{URL: srv.URL + "/x.bin", Force: true, Hash: "12345"}); err == nil {
		t.Fatal("a bad checksum was accepted")
	}
}

// The server sends a checksum for every piece (Content-MD5 on range
// answers): a damaged piece is downloaded again at once.
func TestPieceChecksum(t *testing.T) {
	data := randomData(t, 4<<20)
	bad := int64(1<<20 + 77)
	var damaged atomic.Int32
	xs := &xServer{data: data,
		headers: func(h http.Header, from, to int64) {
			s := md5.Sum(data[from : to+1])
			h.Set("Content-MD5", b64(s[:]))
		},
		mutate: func(from int64, body []byte) []byte {
			// (from > 0: the first answer also covers the bad byte, but only
			// its first part is used.)
			if from > 0 && from <= bad && bad < from+int64(len(body)) && damaged.Add(1) == 1 {
				body[bad-from] ^= 0x01
			}
			return body
		}}
	srv := httptest.NewServer(xs)
	defer srv.Close()
	m, dl := newTestManager(t, 4)
	j, _ := m.Add(AddRequest{URL: srv.URL + "/f.bin", Force: true})
	v := waitDone(t, m, j.ID, time.Minute)
	if v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	checkFile(t, filepath.Join(dl, "f.bin"), data)
	if damaged.Load() < 2 {
		t.Fatalf("the damaged piece was not downloaded again (%d)", damaged.Load())
	}
	if xs.served.Load() > int64(len(data))+(2<<20) {
		t.Fatalf("too much downloaded again: %d", xs.served.Load())
	}
}

// ---------------------------------------------------------------- A8

// Two mirrors of the same file share the work; a mirror with another size
// and a dead mirror are not used.
func TestMirrors(t *testing.T) {
	data := randomData(t, 8<<20)
	a := &xServer{data: data, rate: 1 << 20}
	b := &xServer{data: data, rate: 1 << 20}
	other := &xServer{data: data[:1000]}
	sa, sb, so := httptest.NewServer(a), httptest.NewServer(b), httptest.NewServer(other)
	defer sa.Close()
	defer sb.Close()
	defer so.Close()
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	m, dl := newTestManager(t, 4)
	j, err := m.Add(AddRequest{URL: sa.URL + "/f.iso", Force: true,
		Mirrors: []string{sb.URL + "/f.iso", so.URL + "/f.iso", deadURL + "/f.iso", "not a link", sa.URL + "/f.iso"}})
	if err != nil {
		t.Fatal(err)
	}
	if jobView(m, j.ID).Mirrors != 3 {
		t.Fatalf("mirrors kept: %d", jobView(m, j.ID).Mirrors)
	}
	start := time.Now()
	v := waitDone(t, m, j.ID, time.Minute)
	if v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	took := time.Since(start)
	checkFile(t, filepath.Join(dl, "f.iso"), data)
	t.Logf("main served %d, mirror %d, wrong-size mirror %d bytes; %v", a.served.Load(), b.served.Load(), other.served.Load(), took.Round(time.Millisecond))
	if a.served.Load() < 2<<20 || b.served.Load() < 2<<20 {
		t.Fatal("the work was not shared")
	}
	if other.requests.Load() > 1 { // only the size check
		t.Fatalf("the mirror with another size was used: %d requests", other.requests.Load())
	}
}

// A mirror that breaks in the middle is dropped; the download finishes from
// the main link.
func TestMirrorFails(t *testing.T) {
	data := randomData(t, 6<<20)
	a := &xServer{data: data, rate: 2 << 20}
	var calls atomic.Int32
	b := &xServer{data: data, rate: 2 << 20}
	broken := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.Header.Get("Range") != "bytes=0-0" && calls.Add(1) > 1 {
			http.Error(w, "gone", http.StatusNotFound)
			return
		}
		b.ServeHTTP(w, r)
	})
	sa, sb := httptest.NewServer(a), httptest.NewServer(broken)
	defer sa.Close()
	defer sb.Close()
	m, dl := newTestManager(t, 4)
	j, _ := m.Add(AddRequest{URL: sa.URL + "/f.bin", Force: true, Mirrors: []string{sb.URL + "/f.bin"}})
	v := waitDone(t, m, j.ID, time.Minute)
	if v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	checkFile(t, filepath.Join(dl, "f.bin"), data)
}

// ---------------------------------------------------------------- A9

// One connection is far slower than the others: it is closed and opened
// again (fast this time), so the download does not wait for it.
func TestSlowConnectionReplaced(t *testing.T) {
	oldSteal, oldGrace, oldFor := stealEnabled, slowGrace, slowFor
	stealEnabled, slowGrace, slowFor = false, time.Second, time.Second
	defer func() { stealEnabled, slowGrace, slowFor = oldSteal, oldGrace, oldFor }()
	data := randomData(t, 32<<20)
	slowStart := int64(len(data)) / 4 * 2
	var slowOnce atomic.Bool
	xs := &xServer{data: data, rateFor: func(from, n int64) int64 {
		if from == slowStart && slowOnce.CompareAndSwap(false, true) {
			return 32 << 10 // one bad connection: 32 KB/s
		}
		return 2 << 20
	}}
	srv := httptest.NewServer(xs)
	defer srv.Close()
	m, dl := newTestManager(t, 4)
	start := time.Now()
	j, _ := m.Add(AddRequest{URL: srv.URL + "/f.bin", Force: true})
	v := waitDone(t, m, j.ID, 2*time.Minute)
	took := time.Since(start)
	if v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	checkFile(t, filepath.Join(dl, "f.bin"), data)
	t.Logf("32 MB, 4 connections at 2 MB/s, one at 32 KB/s: %v, %d requests", took.Round(time.Millisecond), xs.requests.Load())
	// Without replacing it, the slow part alone takes 8 MB / 32 KB/s > 4 min.
	if took > 30*time.Second {
		t.Fatalf("the slow connection was not replaced (%v)", took)
	}
}

// ---------------------------------------------------------------- A10

// A server name with two addresses where the first does not answer, and a
// name the computer's DNS cannot find: both work.
func TestOtherAddressesAndFallbackDNS(t *testing.T) {
	oldSys, oldFb := systemLookup, fallbackLookup
	defer func() { systemLookup, fallbackLookup = oldSys, oldFb }()
	var fallbackUsed atomic.Bool
	systemLookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		switch host {
		case "multi.bddm.test":
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.2")}, {IP: net.ParseIP("127.0.0.1")}}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	fallbackLookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		if host == "dnsfail.bddm.test" {
			fallbackUsed.Store(true)
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	data := randomData(t, 3<<20)
	srv := httptest.NewServer(&xServer{data: data})
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	m, dl := newTestManager(t, 4)

	j, _ := m.Add(AddRequest{URL: "http://multi.bddm.test:" + port + "/a.bin", Force: true})
	if v := waitDone(t, m, j.ID, time.Minute); v.Status != StatusDone {
		t.Fatalf("other address: %s: %s", v.Status, v.Error)
	}
	checkFile(t, filepath.Join(dl, "a.bin"), data)
	dialer.mu.Lock()
	good := dialer.host["multi.bddm.test"].good
	dialer.mu.Unlock()
	if good != "127.0.0.1" {
		t.Fatalf("the working address was not remembered: %q", good)
	}
	if got := dialer.order("multi.bddm.test", []net.IPAddr{{IP: net.ParseIP("127.0.0.2")}, {IP: net.ParseIP("127.0.0.1")}}); got[0] != "127.0.0.1" {
		t.Fatalf("order: %v", got)
	}

	j, _ = m.Add(AddRequest{URL: "http://dnsfail.bddm.test:" + port + "/b.bin", Force: true})
	if v := waitDone(t, m, j.ID, time.Minute); v.Status != StatusDone {
		t.Fatalf("fallback DNS: %s: %s", v.Status, v.Error)
	}
	if !fallbackUsed.Load() {
		t.Fatal("public DNS was not asked")
	}
	checkFile(t, filepath.Join(dl, "b.bin"), data)

	// A name nobody can find: a plain error with a fix.
	j, _ = m.Add(AddRequest{URL: "http://nowhere.bddm.test:" + port + "/c.bin", Force: true})
	v := waitDone(t, m, j.ID, 2*time.Minute)
	if v.Status != StatusError || v.Reason != "The website's address could not be found." {
		t.Fatalf("%s %q (%s)", v.Status, v.Reason, v.Error)
	}
}

// ---------------------------------------------------------------- A11

// At most "Downloads at the same time" run; the others wait and start by
// themselves, in queue order (which can be changed).
func TestQueue(t *testing.T) {
	data := randomData(t, 600<<10)
	xs := &xServer{data: data, rate: 600 << 10}
	srv := httptest.NewServer(xs)
	defer srv.Close()
	m, _ := newTestManager(t, 1)
	s := m.Settings()
	s.MaxActive = 2
	if _, err := m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 6; i++ {
		j, _ := m.Add(AddRequest{URL: fmt.Sprintf("%s/f%d.bin", srv.URL, i), Force: true})
		ids = append(ids, j.ID)
	}
	// The last one goes to the front of the queue.
	if err := m.Move(ids[5], "first"); err != nil {
		t.Fatal(err)
	}
	if err := m.Move(ids[0], "up"); err == nil {
		t.Fatal("a running download was moved")
	}
	if v := jobView(m, ids[5]); v.QueuePos != 0 { // History has no queue numbers
		_ = v
	}
	for _, v := range m.List() {
		if v.ID == ids[5] && v.QueuePos != 1 {
			t.Fatalf("moved job is at %d", v.QueuePos)
		}
	}
	started := map[string]int{}
	maxRunning, tick := 0, 0
	deadline := time.Now().Add(time.Minute)
	for {
		tick++
		running, done := 0, 0
		views := map[string]JobView{}
		for _, v := range m.List() { // one snapshot, so the count is exact
			views[v.ID] = v
		}
		for _, id := range ids {
			v := views[id]
			if v.Status == StatusDownloading {
				running++
				if _, ok := started[id]; !ok {
					started[id] = tick
				}
			}
			if v.Status == StatusDone {
				done++
				if _, ok := started[id]; !ok {
					started[id] = tick
				}
			}
		}
		maxRunning = max(maxRunning, running)
		if done == len(ids) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queue did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if maxRunning > 2 {
		t.Fatalf("%d downloads at once (limit 2)", maxRunning)
	}
	for _, id := range ids[2:5] {
		if started[ids[5]] > started[id] {
			t.Fatalf("the moved download did not start first")
		}
	}
	// Raising the limit starts waiting downloads at once.
	for i := 0; i < 4; i++ {
		m.Add(AddRequest{URL: fmt.Sprintf("%s/g%d.bin", srv.URL, i), Force: true})
	}
	s.MaxActive = 4
	m.SetSettings(s)
	time.Sleep(200 * time.Millisecond)
	running := 0
	for _, v := range m.List() {
		if v.Status == StatusDownloading {
			running++
		}
	}
	if running != 4 {
		t.Fatalf("after raising the limit: %d running", running)
	}
}

// ---------------------------------------------------------------- A12

func TestExplainError(t *testing.T) {
	cases := []struct{ err, reason, fixHas string }{
		{`could not connect: Head "http://x.example/f": dial tcp: lookup x.example: no such host`, "The website's address could not be found.", "typed correctly"},
		{`part 3 failed after 10 tries: read tcp 1.2.3.4:5->6.7.8.9:443: wsarecv: An existing connection was forcibly closed by the remote host.`, "The connection was cut off.", "Retry"},
		{`The server refused the download (HTTP 403). The link may have expired or need a sign-in: start the download again from your browser.`, "The website refused to give this file to BDDM (HTTP 403 Forbidden).", "hold Alt"},
		{`The server refused the download (HTTP 403 Forbidden).`, "The website refused to give this file to BDDM (HTTP 403 Forbidden).", "fresh link"},
		{msgStreamRefused, msgStreamRefused, "hold Alt"},
		{`The server refused the download (HTTP 404). The link may have expired or need a sign-in: start the download again from your browser.`, "The file was not found on the server (HTTP 404).", "fresh link"},
		{`could not connect: server returned HTTP 404`, "The file was not found on the server.", "fresh link"},
		{`HTTP 503`, "The website's server had a problem.", "later"},
		{`could not create file: open D:\x\f.bin.part: Access is denied.`, "BDDM is not allowed to save files in this folder.", "Settings"},
		{`Not enough free space on D:\: this download needs 4.0 GB, but only 1.0 GB is free. Free up some space or choose another folder in Settings, then click Retry.`, "Not enough free space on D:\\: this download needs 4.0 GB, but only 1.0 GB is free. Free up some space or choose another folder in Settings, then click Retry.", "Free up"},
		{`Get "https://x/": tls: failed to verify certificate: x509: certificate has expired`, "The website's security certificate could not be checked.", "date and time"},
		{`could not connect: Get "https://x/f": net/http: TLS handshake timeout`, "The server stopped answering.", "internet"},
		{msgLinkExpired, msgLinkExpired, "again on the website"},
		{`server says too many requests`, "The server is limiting how much you can download right now.", "few minutes"},
		{`weird thing`, "The download failed: weird thing", "Retry"},
	}
	for _, c := range cases {
		r, f := explainError(c.err)
		if r != c.reason || !strings.Contains(f, c.fixHas) {
			t.Errorf("%q\n  reason %q\n  fix    %q", c.err, r, f)
		}
	}
	if r, f := explainError(""); r != "" || f != "" {
		t.Error("empty")
	}
}

// ---------------------------------------------------------------- A13 (and crash resume)

// A download that was running when BDDM closed continues when it starts
// again (and is not started over).
func TestResumeAfterRestart(t *testing.T) {
	data := randomData(t, 6<<20)
	xs := &xServer{data: data, rate: 1 << 20}
	srv := httptest.NewServer(xs)
	defer srv.Close()
	base := t.TempDir()
	cfg := filepath.Join(base, "cfg")
	dl := filepath.Join(base, "dl")
	m, err := NewManager(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := m.Settings()
	s.DownloadDir, s.Parts = dl, 4
	m.SetSettings(s)
	j, _ := m.Add(AddRequest{URL: srv.URL + "/f.bin", Force: true})
	waitBytes(t, m, j.ID, 2<<20)
	m.Shutdown() // like closing BDDM (or the installer updating it)
	got := xs.served.Load()

	m2, err := NewManager(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Shutdown()
	v := waitDone(t, m2, j.ID, time.Minute)
	if v.Status != StatusDone {
		t.Fatalf("%s: %s", v.Status, v.Error)
	}
	checkFile(t, filepath.Join(dl, "f.bin"), data)
	if xs.served.Load() > int64(len(data))+(1<<20) {
		t.Fatalf("started over: %d bytes for %d (had %d)", xs.served.Load(), len(data), got)
	}

	// With the setting off it waits paused.
	s = m2.Settings()
	s.ResumeOnStart = false
	m2.SetSettings(s)
	j2, _ := m2.Add(AddRequest{URL: srv.URL + "/g.bin", Force: true})
	waitBytes(t, m2, j2.ID, 1<<20)
	m2.Shutdown()
	m3, _ := NewManager(cfg, nil)
	defer m3.Shutdown()
	time.Sleep(300 * time.Millisecond)
	if v := jobView(m3, j2.ID); v.Status != StatusPaused {
		t.Fatalf("setting off: %s", v.Status)
	}
	os.RemoveAll(base)
}
