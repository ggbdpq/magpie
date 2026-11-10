package netproxy

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

var deadProbeErr = errors.New("connection refused")

func probeSaysDead(dead bool) func(string, time.Duration) (net.Conn, error) {
	return func(string, time.Duration) (net.Conn, error) {
		if dead {
			return nil, deadProbeErr
		}
		// a listener took the dial: a pipe, so the test's answer is the
		// same whatever really listens on the machine
		a, b := net.Pipe()
		go func() {
			time.Sleep(time.Second)
			a.Close()
			b.Close()
		}()
		return b, nil
	}
}

func freshSystemCache(t *testing.T) {
	t.Helper()
	sysCache.Lock()
	sysCache.at, sysCache.p, sysCache.failed, sysCache.reading = time.Time{}, Proxy{}, false, nil
	sysCache.Unlock()
	t.Cleanup(func() {
		sysCache.Lock()
		sysCache.at, sysCache.p, sysCache.failed, sysCache.reading = time.Time{}, Proxy{}, false, nil
		sysCache.Unlock()
	})
}

// #6012: the proxy app died with its system setting left behind — Clash
// crashes, or the user kills its core while ProxyEnable stays 1 — and its
// loopback port listens no more. Read as no proxy, so requests go direct
// instead of dialing the dead port for good: the port is probed when the
// system's proxy is read (at most every few seconds) and when the cache's
// answer is taken, on the loopback alone, and the app coming back is
// taken again on the next read.
func TestLoopbackProxyDeadPortIsNone(t *testing.T) {
	oldRead, oldNone, oldDial := readSystem, noneKeep, deadDial
	noneKeep = 50 * time.Millisecond
	deadDial = probeSaysDead(true)
	var reads atomic.Int32
	readSystem = func() (Proxy, bool) {
		reads.Add(1)
		return Proxy{URL: "http://127.0.0.1:7897", HTTP: "http://127.0.0.1:7897"}, true
	}
	freshSystemCache(t)
	t.Cleanup(func() {
		readSystem, noneKeep, deadDial = oldRead, oldNone, oldDial
	})

	if got := System(); got.URL != "" {
		t.Fatalf("a loopback proxy nobody listens on was read as %+v, want none", got)
	}
	// The next read is due while none stands, and is none again: the app
	// coming back is taken, the dead port never dialled.
	time.Sleep(100 * time.Millisecond)
	if got := System(); got.URL != "" {
		t.Fatalf("read again: %+v, want none", got)
	}
	if reads.Load() < 2 {
		t.Fatalf("the system was read %d times, want at least 2", reads.Load())
	}
}

// The same death inside the cache's own window: the read had named the
// port while the app still lived, it died since, and the next request
// takes none at once rather than dialing the dead port until the read is
// due again.
func TestLoopbackProxyDeadPortInCacheIsNone(t *testing.T) {
	oldRead, oldKeep, oldDial := readSystem, systemKeep, deadDial
	systemKeep = time.Hour
	deadDial = probeSaysDead(true)
	readSystem = func() (Proxy, bool) { return Proxy{URL: "http://127.0.0.1:7897", HTTP: "http://127.0.0.1:7897"}, true }
	freshSystemCache(t)
	t.Cleanup(func() { readSystem, systemKeep, deadDial = oldRead, oldKeep, oldDial })

	if got := System(); got.URL != "" {
		t.Fatalf("a loopback proxy nobody listens on was read as %+v, want none", got)
	}
	// The cache was left holding the dead proxy (the read had put it
	// there while the app lived): the next ask probes it, not the read.
	sysCache.Lock()
	sysCache.at, sysCache.p = time.Now(), Proxy{URL: "http://127.0.0.1:7897", HTTP: "http://127.0.0.1:7897"}
	sysCache.Unlock()
	if got := System(); got.URL != "" {
		t.Fatalf("a dead proxy held in the cache was taken as %+v, want none", got)
	}
}

// A loopback proxy whose port answers, and one elsewhere on the network,
// are kept as they were read; the probe takes no dial for a proxy with no
// port in it.
func TestProxyNotDeadIsKept(t *testing.T) {
	oldRead, oldDial := readSystem, deadDial
	var probed atomic.Int32
	deadDial = func(addr string, timeout time.Duration) (net.Conn, error) {
		probed.Add(1)
		return probeSaysDead(false)(addr, timeout)
	}
	var which atomic.Value
	which.Store(Proxy{})
	readSystem = func() (Proxy, bool) { return which.Load().(Proxy), true }
	freshSystemCache(t)
	t.Cleanup(func() { readSystem, deadDial = oldRead, oldDial })

	which.Store(Proxy{URL: "http://127.0.0.1:7897", HTTP: "http://127.0.0.1:7897"})
	if got := System(); got.URL != "http://127.0.0.1:7897" {
		t.Fatalf("a loopback proxy whose port answers was read as %+v", got)
	}

	// A proxy off this machine is none of this read's to probe.
	sysCache.Lock()
	sysCache.at, sysCache.p = time.Time{}, Proxy{}
	sysCache.Unlock()
	far := Proxy{URL: "http://10.9.8.7:3128", HTTP: "http://10.9.8.7:3128"}
	which.Store(far)
	if got := System(); got.URL != far.URL {
		t.Fatalf("a proxy elsewhere was read as %+v, want it kept", got)
	}

	// A loopback name with no port in it is not probed either.
	sysCache.Lock()
	sysCache.at, sysCache.p = time.Time{}, Proxy{}
	sysCache.Unlock()
	which.Store(Proxy{URL: "http://127.0.0.1", HTTP: "http://127.0.0.1"})
	System()
	if n := probed.Load(); n != 1 {
		t.Fatalf("the port was probed %d times beyond the live loopback one, want no more", n)
	}
}
