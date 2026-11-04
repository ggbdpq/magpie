package netproxy

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// settled waits until no read of the system's proxy is under way: System
// hands a request what was read before at once when the last read failed,
// its read finishing behind it.
func settled() {
	for range 1000 {
		sysCache.Lock()
		reading := sysCache.reading != nil
		sysCache.Unlock()
		if !reading {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// #4169: a machine whose reads of the system's proxy fail forked scutil
// every noneKeep while requests flowed, and a fork is what killed the
// process there (libmalloc's atfork parent handler). The reads after
// failed ones are put off longer each time, and one that answers resets
// that.
func TestFailedSystemProxyReadsBackOff(t *testing.T) {
	oldFail := failKeep
	failKeep = 10 * time.Second
	t.Cleanup(func() { failKeep = oldFail })

	set := Proxy{URL: "http://127.0.0.1:7890"}
	var calls atomic.Int32
	oldRead := readSystem
	readSystem = func() (Proxy, bool) { calls.Add(1); return Proxy{}, false }
	t.Cleanup(func() { readSystem = oldRead })
	sysCache.Lock()
	// a proxy was read before (at, p), 16 s ago: past a set proxy's keep,
	// so the first ask reads again — and that read fails
	sysCache.at, sysCache.p, sysCache.failed = time.Now().Add(-16*time.Second), set, 0
	sysCache.reading, sysCache.since = nil, time.Time{}
	sysCache.Unlock()
	t.Cleanup(func() {
		sysCache.Lock()
		sysCache.at, sysCache.p, sysCache.failed = time.Time{}, Proxy{}, 0
		sysCache.reading, sysCache.since = nil, time.Time{}
		sysCache.Unlock()
	})

	// the first read fails, and keeps what was read before
	if p := ask(t); p.URL != set.URL {
		t.Fatalf("a failed read dropped the proxy read before: %+v", p)
	}
	settled()
	if calls.Load() != 1 || sysCache.failed != 1 {
		t.Fatalf("the first read: %d calls, %d failures standing", calls.Load(), sysCache.failed)
	}

	// the reads after failed ones run again only past their keep, which
	// doubles each time up to failKeep; within it a request takes what
	// was read before and begins no read, past it the next one runs and
	// no request waits for it (a failed read is answered from the cache
	// at once, its read finishing behind it)
	for k := 1; ; k++ {
		keep := nextKeep(k)
		if keep > failKeep {
			t.Fatalf("the read after %d failures is put off %v, past failKeep", k, keep)
		}
		// within the window: no read
		if p := ask(t); p.URL != set.URL {
			t.Fatalf("within the window after %d failures, requests take %+v", k, p)
		}
		settled()
		if calls.Load() != int32(k) {
			t.Fatalf("a read began within the %v window after %d failures", keep, k)
		}
		// past it: the next read runs, and fails
		sysCache.Lock()
		sysCache.at = time.Now().Add(-keep - time.Second)
		sysCache.Unlock()
		if p := ask(t); p.URL != set.URL {
			t.Fatalf("the read after %d failures dropped the proxy read before", k)
		}
		settled()
		if calls.Load() != int32(k+1) || sysCache.failed != k+1 {
			t.Fatalf("the read after %d failures: %d calls, %d failures standing", k, calls.Load(), sysCache.failed)
		}
		if keep == failKeep {
			break
		}
	}

	// an answer among them stops the backing off
	readSystem = func() (Proxy, bool) { calls.Add(1); return set, true }
	sysCache.Lock()
	sysCache.at = time.Now().Add(-failKeep - time.Second)
	sysCache.Unlock()
	if p := ask(t); p.URL != set.URL {
		t.Fatalf("the read that answered, but requests take %+v", p)
	}
	settled()
	if sysCache.failed != 0 {
		t.Fatalf("an answering read left %d failures standing", sysCache.failed)
	}

	// and the reads after it are on the set proxy's keep again: a read
	// began at once, not on a failed one's put-off
	n := calls.Load()
	sysCache.Lock()
	sysCache.at = time.Now().Add(-systemKeep - time.Second)
	sysCache.Unlock()
	if p := ask(t); p.URL != set.URL {
		t.Fatalf("the read after an answering one: requests take %+v", p)
	}
	settled()
	if got := calls.Load(); got != n+1 {
		t.Fatalf("%d reads after an answering one: the backoff was not reset", got-n)
	}
}

// The fork the system's read makes on macOS is off the request path as
// far as the cache can take it: a request whose proxy the system has none
// of, where reads fail, still answers, on what was read before.
func TestRequestsKeepGoingWhileReadsFail(t *testing.T) {
	oldRead, oldFail, oldNone := readSystem, failKeep, noneKeep
	readSystem, failKeep = func() (Proxy, bool) { return Proxy{}, false }, 10*time.Second
	noneKeep = 2 * time.Second
	t.Cleanup(func() { readSystem, failKeep, noneKeep = oldRead, oldFail, oldNone })
	sysCache.Lock()
	sysCache.at, sysCache.p, sysCache.failed = time.Now().Add(-time.Hour), Proxy{}, 0
	sysCache.reading, sysCache.since = nil, time.Time{}
	sysCache.Unlock()
	t.Cleanup(func() {
		sysCache.Lock()
		sysCache.at, sysCache.p, sysCache.failed = time.Time{}, Proxy{}, 0
		sysCache.reading, sysCache.since = nil, time.Time{}
		sysCache.Unlock()
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = Func
	c := &http.Client{Transport: Dispatch(base)}
	defer c.CloseIdleConnections()
	start := time.Now()
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("a request with the system read failing: %v", err)
	}
	resp.Body.Close()
	if took := time.Since(start); took >= readWait {
		t.Fatalf("a request waited %v for a read that never answers", took)
	}
}
