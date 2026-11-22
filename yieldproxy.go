package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Do not follow HTTP_PROXY: this proxy is only for yt-dlp on this computer
// and must talk to the website (or a test server) directly.
var yieldDirectTransport = &http.Transport{Proxy: nil, DisableKeepAlives: true}

// listenYieldProxy listens on 127.0.0.1 and rate-limits proxied traffic
// with the shared yield buckets so yt-dlp picks up the cap live.
func listenYieldProxy(down, up *tokenBucket) (addr string, stop func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	s := &http.Server{
		Handler:           yieldProxyHandler{down: down, up: up},
		ReadHeaderTimeout: 30 * time.Second,
	}
	go s.Serve(ln)
	return ln.Addr().String(), func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
		_ = ln.Close()
	}, nil
}

type yieldProxyHandler struct {
	down, up *tokenBucket
}

func (h yieldProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		h.connect(w, r)
		return
	}
	h.forward(w, r)
}

func (h yieldProxyHandler) connect(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if host == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !strings.Contains(host, ":") {
		host += ":443"
	}
	dest, err := net.DialTimeout("tcp", host, 20*time.Second)
	if err != nil {
		dropProxyClient(w) // connection error, not HTTP 502 (BDDM treats that as a site error)
		return
	}
	defer dest.Close()
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "not supported", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	if _, err := io.WriteString(buf, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := buf.Flush(); err != nil {
		return
	}
	ctx := r.Context()
	// dest -> client is download (the remote site's response)
	// client -> dest is upload
	go func() {
		_, _ = io.Copy(dest, &limitedReader{ctx: ctx, r: buf, b: h.up})
		_ = dest.Close()
	}()
	_, _ = io.Copy(client, &limitedReader{ctx: ctx, r: dest, b: h.down})
}

func (h yieldProxyHandler) forward(w http.ResponseWriter, r *http.Request) {
	if r.URL == nil || r.URL.Host == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	out, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	out.Header = r.Header.Clone()
	resp, err := yieldDirectTransport.RoundTrip(out)
	if err != nil {
		dropProxyClient(w)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, &limitedReader{ctx: r.Context(), r: resp.Body, b: h.down})
}

// dropProxyClient closes the client connection without an HTTP error status,
// so yt-dlp and BDDM treat a down site as a network problem and can wait
// and continue, instead of seeing "HTTP 502".
func dropProxyClient(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "could not connect", http.StatusBadGateway)
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	_ = conn.Close()
}
