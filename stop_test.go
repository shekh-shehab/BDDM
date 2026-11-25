package main

import (
	"net"
	"testing"
	"time"
)

func TestWaitForClosedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if !tcpPortOpen(addr, time.Second) {
		t.Fatalf("expected the test listener on %s to be open", addr)
	}

	closed := make(chan bool, 1)
	go func() {
		time.Sleep(80 * time.Millisecond)
		ln.Close()
	}()
	go func() {
		closed <- waitForClosedPort(addr, 2*time.Second, 0)
	}()
	select {
	case ok := <-closed:
		if !ok {
			t.Fatal("waitForClosedPort should return true after the listener is closed")
		}
	case <-time.After(3 * time.Second):
		ln.Close()
		t.Fatal("waitForClosedPort timed out")
	}
	if tcpPortOpen(addr, 200*time.Millisecond) {
		t.Fatal("port should stay closed")
	}
}

func TestWaitForClosedPortAlreadyClosed(t *testing.T) {
	// Pick an address that is not listening.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	start := time.Now()
	if !waitForClosedPort(addr, time.Second, 0) {
		t.Fatal("a free port should look closed immediately")
	}
	if time.Since(start) > 800*time.Millisecond {
		t.Fatalf("waited too long for an already-closed port: %s", time.Since(start))
	}
}

func TestStopRunningAtTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	start := time.Now()
	stopRunningAt("http://"+addr, addr, 300*time.Millisecond)
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("stopRunningAt did not honour the timeout: %s", elapsed)
	}
	if elapsed < 200*time.Millisecond {
		// The port is open and is not a BDDM API, so we should wait until the timeout.
		t.Fatalf("stopRunningAt returned too quickly on an open port: %s", elapsed)
	}
}
