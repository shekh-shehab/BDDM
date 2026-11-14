package main

// netwatch.go: what BDDM does when the network is the problem.
//
//   - No internet (Wi-Fi dropped, cable out): downloads wait and continue by
//     themselves when the connection is back, without using up their retries.
//   - A server name has several addresses (IPs) and one does not answer:
//     the next one is tried, and the one that worked is used first next time.
//   - The computer's DNS cannot find a name: public DNS servers are asked.

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ---------------------------------------------------------------- offline

// onlineProbes are well-known addresses that answer from almost anywhere.
// When neither they nor the download's server can be reached, the computer
// is offline.
var onlineProbes = []string{"1.1.1.1:443", "8.8.8.8:443", "9.9.9.9:443", "208.67.222.222:443"}

// onlineEvery: how often BDDM checks whether the connection is back.
var onlineEvery = 3 * time.Second

// netOnline reports whether the network works: the server of the download
// (or the proxy) or one of the well-known addresses answers.
var netOnline = func(ctx context.Context, server string) bool {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	addrs := append([]string(nil), onlineProbes...)
	if server != "" {
		addrs = append([]string{server}, addrs...)
	}
	ok := make(chan bool, len(addrs))
	for _, a := range addrs {
		go func(a string) {
			c, err := dialer.DialContext(ctx, "tcp", a)
			if err == nil {
				c.Close()
			}
			ok <- err == nil
		}(a)
	}
	for range addrs {
		if <-ok {
			return true
		}
	}
	return false
}

// serverAddr is the host:port BDDM connects to for a link (the proxy when
// one is set).
func serverAddr(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	if req, err := http.NewRequest(http.MethodGet, raw, nil); err == nil {
		if p, err := http.ProxyFromEnvironment(req); err == nil && p != nil {
			u = p
		}
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// isNetErr: the connection itself failed (no internet, DNS, timeouts, a
// dropped connection), not an answer of the server.
func isNetErr(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, errStalled) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		return true
	}
	for _, e := range []error{syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ECONNABORTED, syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.ETIMEDOUT} {
		if errors.Is(err, e) {
			return true
		}
	}
	return looksLikeNetMsg(err.Error())
}

// looksLikeNetMsg recognises network failures in text (yt-dlp's messages,
// wrapped errors).
func looksLikeNetMsg(s string) bool {
	s = strings.ToLower(s)
	for _, k := range []string{
		"no such host", "name resolution", "getaddrinfo failed", "failed to resolve", "nodename nor servname",
		"network is unreachable", "no route to host", "connection reset", "connection refused", "connection aborted",
		"timed out", "timeout", "i/o timeout", "unexpected eof", "broken pipe", "forcibly closed",
		"errno 11001", "errno 11002", "errno 10054", "errno 10060", "errno 10061", "errno 10065", "errno 101", "errno 104", "errno 110", "errno 111",
		"unable to connect", "network error", "connection was reset", "incompleteread", "remote end closed",
		"dial tcp", "tls handshake timeout", "download stalled",
	} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

const msgNoInternet = "No internet connection. BDDM continues automatically when the connection is back..."

// waitOnline: when the network is down, wait (showing why) until it is back.
// Returns true when it waited (the caller tries again without counting a
// failed try), false when the network works (the failure is the server's).
func waitOnline(ctx context.Context, j *Job, server string) (bool, error) {
	if netOnline(ctx, server) {
		return false, nil
	}
	j.setNetWait(true)
	defer j.setNetWait(false)
	log.Printf("[%s] no internet connection; waiting", j.displayName())
	for {
		// Shown in the list (not logged again every few seconds).
		j.liveMu.Lock()
		j.message = msgNoInternet
		j.msgTime = time.Now()
		j.liveMu.Unlock()
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-time.After(onlineEvery):
		}
		if netOnline(ctx, server) {
			j.setMessage("The connection is back; continuing...")
			return true, nil
		}
	}
}

func (j *Job) setNetWait(on bool) {
	j.liveMu.Lock()
	j.netWait = on
	j.liveMu.Unlock()
}

// ---------------------------------------------------------------- dialing

// systemLookup asks the computer's DNS; fallbackLookup asks public DNS
// servers when that fails. (Variables so tests can replace them.)
var systemLookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

var fallbackDNS = []string{"1.1.1.1:53", "8.8.8.8:53", "9.9.9.9:53"}

var fallbackLookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
	var last error = errors.New("no fallback DNS servers")
	for _, srv := range fallbackDNS {
		r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, srv)
		}}
		c, cancel := context.WithTimeout(ctx, 4*time.Second)
		ips, err := r.LookupIPAddr(c, host)
		cancel()
		if err == nil && len(ips) > 0 {
			return ips, nil
		}
		if err != nil {
			last = err
		}
	}
	return nil, last
}

type hostIPs struct {
	good string               // the address that worked last
	bad  map[string]time.Time // addresses that failed, and when
}

// smartDialer connects to every address of a name in turn, starting with
// the one that worked last; addresses that just failed come last.
type smartDialer struct {
	base net.Dialer
	mu   sync.Mutex
	host map[string]*hostIPs
}

var dialer = &smartDialer{base: net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}, host: map[string]*hostIPs{}}

// badFor: how long a failed address is tried last.
const badFor = 10 * time.Minute

func (s *smartDialer) lookup(ctx context.Context, host string) ([]net.IPAddr, error) {
	ips, err := systemLookup(ctx, host)
	if err == nil && len(ips) > 0 {
		return ips, nil
	}
	if ctx.Err() != nil {
		return nil, err
	}
	if fips, ferr := fallbackLookup(ctx, host); ferr == nil && len(fips) > 0 {
		log.Printf("DNS could not find %s (%v); found it with public DNS", host, err)
		return fips, nil
	}
	if err == nil {
		err = &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true}
	}
	return nil, err
}

// order puts the address that worked first and the ones that failed last.
func (s *smartDialer) order(host string, ips []net.IPAddr) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.host[host]
	var first, mid, last []string
	for _, ip := range ips {
		a := ip.String()
		switch {
		case h != nil && a == h.good:
			first = append(first, a)
		case h != nil && time.Since(h.bad[a]) < badFor:
			last = append(last, a)
		default:
			mid = append(mid, a)
		}
	}
	return append(append(first, mid...), last...)
}

func (s *smartDialer) mark(host, ip string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.host[host]
	if h == nil {
		h = &hostIPs{bad: map[string]time.Time{}}
		s.host[host] = h
	}
	if ok {
		h.good = ip
		delete(h.bad, ip)
	} else {
		h.bad[ip] = time.Now()
		if h.good == ip {
			h.good = ""
		}
	}
}

func (s *smartDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) != nil || strings.EqualFold(host, "localhost") {
		return s.base.DialContext(ctx, network, addr)
	}
	ips, err := s.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	list := s.order(host, ips)
	if network == "tcp4" || network == "tcp6" {
		var keep []string
		for _, a := range list {
			if (network == "tcp4") == (net.ParseIP(a).To4() != nil) {
				keep = append(keep, a)
			}
		}
		list = keep
	}
	var lastErr error = &net.DNSError{Err: "no usable address", Name: host}
	for i, ip := range list {
		d := s.base
		if left := len(list) - i; left > 1 {
			d.Timeout = 8 * time.Second // leave time for the other addresses
		}
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip, port))
		if err == nil {
			s.mark(host, ip, true)
			if i > 0 {
				log.Printf("%s: address %s did not answer; using %s", host, list[0], ip)
			}
			return c, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
		s.mark(host, ip, false)
	}
	return nil, lastErr
}
