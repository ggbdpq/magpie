package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// eofVendor is an upstream that takes the connection and dies before a
// word: a relay whose site went dark answers this way, Go's client left
// reading EOF for every attempt (#2945: agentrouter.org, down for 32
// minutes, really asked 706 times over 55 conversations).
type eofVendor struct {
	mu    sync.Mutex
	dials []string
}

func (e *eofVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	e.mu.Lock()
	e.dials = append(e.dials, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	e.mu.Unlock()
	panic(http.ErrAbortHandler) // the connection dies with nothing said
}

func (e *eofVendor) took() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.dials
	e.dials = nil
	return out
}

func eofChain(t *testing.T, keys ...string) (*eofVendor, *Server) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	noRests()
	t.Cleanup(noRests)
	routed.Lock()
	routed.failures = map[string]int{}
	routed.Unlock()
	t.Cleanup(func() {
		routed.Lock()
		routed.failures = map[string]int{}
		routed.Unlock()
	})
	e := &eofVendor{}
	up := httptest.NewServer(e)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "dark", Name: "dark", Chat: up.URL + "/v1", Models: []string{"glm-5.3-flash"}, Routing: provider.Ordered}
	p.Key = keys[0]
	for _, k := range keys[1:] {
		p.Keys = append(p.Keys, provider.KeyAccount{Key: k})
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return e, New()
}

func postDark(t *testing.T, srv *Server) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"dark/glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// expireRests ends every rest there is, as an outage long enough does a
// minute, then two, after each: what a failure next makes is the ladder's
// next step.
func expireRests(t *testing.T) {
	t.Helper()
	restingUntil.Lock()
	for k := range restingUntil.m {
		restingUntil.m[k] = time.Now().Add(-time.Second)
	}
	restingUntil.Unlock()
}

// outageHeldOf says cs are all held: each rests its outage ladder's second
// step at least.
func outageHeldOf(t *testing.T, cs []candidate) {
	t.Helper()
	for _, c := range cs {
		r, ok := restingOf(c)
		if !ok || r.Why != failOther || r.By != "backoff" || r.Failures < 2 {
			t.Fatalf("%s rests %+v (ok %v), want an outage rest twice climbed", c.rest, r, ok)
		}
	}
}

// #2945 (an upstream 502/EOF hammer): a relay went dark — every dial met
// EOF — and magpie kept really asking it for 32 minutes, 706 calls over
// 55 conversations, every request the probe its resting keys still got,
// each try its patience's rounds. A key whose outage ladder has climbed
// twice is now answered from the rests, not asked, with the soonest one
// back as Retry-After; the list is asked in the background instead
// (rest_probe.go), which lifts the rest as soon as the vendor answers.
func TestAllRestingOutageAnswersFromRest(t *testing.T) {
	rp, sp := retryPause, singlePatience
	retryPause, singlePatience = time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { retryPause, singlePatience = rp, sp })
	e, srv := eofChain(t, "eof-a", "eof-b")

	// the outage begins: each key fails once, and each rests as the one
	// asked before the resting mate
	for i := 0; i < 2; i++ {
		if rec := postDark(t, srv); rec.Code != http.StatusBadGateway {
			t.Fatalf("request %d: %d %s", i+1, rec.Code, rec.Body)
		}
		e.took()
	}
	// an outage long enough climbs the ladder: its first rest over, a
	// failure again is counted again. The last one left's failure rests
	// nothing, so each key climbs as the one asked before its resting mate
	expireRests(t)
	if rec := postDark(t, srv); rec.Code != http.StatusBadGateway {
		t.Fatalf("the ladder's second step: %d %s", rec.Code, rec.Body)
	}
	e.took()
	rec := postDark(t, srv)
	if got := e.took(); len(got) == 0 || slices.Contains(got, "eof-a") {
		t.Fatalf("the key resting twice failed was really asked (%v), want only the one not resting", got)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("the ladder's second step: %d %s", rec.Code, rec.Body)
	}
	outageHeldOf(t, []candidate{
		{p: provider.Provider{ID: "dark"}, rest: "dark#" + provider.KeyID("eof-a")},
		{p: provider.Provider{ID: "dark"}, rest: "dark#" + provider.KeyID("eof-b")},
	})

	// every key rests, twice failed: the next requests are answered from
	// the rests, and the vendor is not asked at all
	for i := 0; i < 3; i++ {
		rec := postDark(t, srv)
		if got := e.took(); len(got) != 0 {
			t.Fatalf("request %d while every key rests twice failed: really asked %v", i+1, got)
		}
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("request %d: %d %s", i+1, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "dark (unreachable: not asked again before ") {
			t.Errorf("request %d says neither who nor when: %s", i+1, rec.Body)
		}
		ra, err := strconv.Atoi(rec.Header().Get("Retry-After"))
		if err != nil || ra < 60 || ra > 121 {
			t.Errorf("request %d: Retry-After %q, want the time left of the soonest rest", i+1, rec.Header().Get("Retry-After"))
		}
	}
}
