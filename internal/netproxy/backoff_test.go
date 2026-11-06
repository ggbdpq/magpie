package netproxy

import (
	"sync/atomic"
	"testing"
	"time"
)

// shortRun puts the cache at nothing read yet and the keep windows at
// 5ms, so a run of reads that find no proxy plays out in milliseconds. It
// answers the reporter's shape (#4169): scutil answers at once, and no
// proxy is set. The reads it makes are counted.
func shortRun(t *testing.T) (*atomic.Int32, *atomic.Value) {
	t.Helper()
	var reads atomic.Int32
	var ans atomic.Value
	ans.Store(readAnswer{Proxy{}, true})
	oldRead, oldNone, oldSet := readSystem, noneKeep, systemKeep
	readSystem = func() (Proxy, bool) {
		reads.Add(1)
		a := ans.Load().(readAnswer)
		return a.p, a.ok
	}
	noneKeep, systemKeep = 5*time.Millisecond, 5*time.Millisecond
	sysCache.Lock()
	sysCache.at, sysCache.p, sysCache.quiet, sysCache.failed = time.Time{}, Proxy{}, 0, false
	sysCache.Unlock()
	t.Cleanup(func() {
		readSystem, noneKeep, systemKeep = oldRead, oldNone, oldSet
		sysCache.Lock()
		sysCache.at, sysCache.p, sysCache.quiet, sysCache.failed = time.Time{}, Proxy{}, 0, false
		sysCache.Unlock()
	})
	return &reads, &ans
}

// readsWhile asks for the system proxy without a pause for d, the way a
// machine with requests flowing does.
func readsWhile(d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		System()
	}
}

// #4169, the machine the report came from: scutil --proxy exits 0 and
// names no proxy (PAC only, HTTP/HTTPS/SOCKS all off), so the read
// succeeds — nothing ever fails — and it still forked a scutil every two
// seconds while requests flowed. A run of reads that come back without a
// proxy, empty or failed alike, is put off longer each time; the count
// here is the count of forks.
func TestEmptySystemProxyReadsBackOff(t *testing.T) {
	reads, ans := shortRun(t)

	readsWhile(250 * time.Millisecond)
	// at the old pace — one read every noneKeep — 250ms is about 50 reads
	if n := reads.Load(); n > 25 {
		t.Fatalf("%d reads in 250ms with noneKeep at 5ms: reads that keep coming back empty are not put off", n)
	}

	// a proxy that turns up mid-run is taken at the read the window lets
	// through, whatever run was going
	set := Proxy{URL: "http://127.0.0.1:7890"}
	ans.Store(readAnswer{set, true})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if p := System(); p.URL == set.URL {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if p := System(); p.URL != set.URL {
		t.Fatalf("a proxy turned up mid-run, but magpie reads %+v", p)
	}

	// and the run starts over: misses after it come at the none pace again
	reads.Store(0)
	ans.Store(readAnswer{Proxy{}, true})
	readsWhile(30 * time.Millisecond)
	if n := reads.Load(); n < 3 {
		t.Fatalf("%d reads in 30ms after the run started over with noneKeep at 5ms: it did not", n)
	}
}

// The read that fails backs off the same way, and the requests it serves
// keep going with what was read before.
func TestFailedSystemProxyReadsBackOff(t *testing.T) {
	reads, ans := shortRun(t)
	ans.Store(readAnswer{Proxy{}, false})

	readsWhile(250 * time.Millisecond)
	if n := reads.Load(); n > 25 {
		t.Fatalf("%d reads in 250ms with noneKeep at 5ms: reads that keep failing are not put off", n)
	}
	if p := System(); p.URL != "" {
		t.Fatalf("reads that fail read %+v", p)
	}
}
