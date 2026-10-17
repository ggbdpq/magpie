package gateway

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// TestProxyDownDoesNotRest (#381): a member whose proxy isn't listening
// (127.0.0.1:1082 down, "proxyconnect tcp: … connection refused") never
// reached its vendor. The next member answers, and the first isn't set
// aside: it is asked first again as soon as the proxy is back, not after a
// backoff that grew with every request made while it was down.
func TestProxyDownDoesNotRest(t *testing.T) {
	for _, scheme := range []string{"http", "socks5"} {
		t.Run(scheme, func(t *testing.T) {
			fresh(t)
			for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
				t.Setenv(k, "")
			}
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from b"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
			}))
			t.Cleanup(up.Close)
			down := deadAddr(t, up.Listener.Addr().String())
			for _, p := range []provider.Provider{
				{ID: "a", Name: "A", Key: "ka", Models: []string{"m"}, Chat: "http://a.test/v1", Proxy: scheme + "://" + down},
				{ID: "b", Name: "B", Key: "kb", Models: []string{"m"}, Chat: up.URL + "/v1"},
			} {
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
			}
			if err := provider.SaveGroup(provider.Group{Name: "Grok", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			s := New()
			for i := 0; i < 2; i++ {
				code, body := postAs(t, s, "", `{"model":"group/grok","messages":[{"role":"user","content":"hi"}]}`)
				if code != 200 || !strings.Contains(body, "from b") {
					t.Fatalf("request %d: %d %s", i, code, body)
				}
				r := s.trace.routes[len(s.trace.routes)-1]
				if len(r.Tries) != 2 || r.Tries[0].ID != r.Order[0].ID || r.Order[0].Provider != "a" {
					t.Fatalf("request %d: a wasn't asked first: %+v", i, r.Tries)
				}
				if f := r.Tries[0]; f.Fail != "proxy" || f.Rest != nil || !strings.Contains(f.Error, down) {
					t.Fatalf("request %d: a's try %+v", i, f)
				}
			}
			restingUntil.Lock()
			n := len(restingUntil.m)
			restingUntil.Unlock()
			if n != 0 {
				t.Fatalf("%d set aside over a proxy that was down", n)
			}
		})
	}
}

// TestProxyDownSkipsSharedProxy (#2682): two members whose providers name
// the same proxy share its failure within one request. Once the first
// member's dial to it is refused, the second isn't dialled either — the
// listener refused magpie once this request already — and the member that
// goes another way is asked at once. Nobody is set aside: the proxy is
// tried again by the next request, so recovery is as quick as #381 kept it.
func TestProxyDownSkipsSharedProxy(t *testing.T) {
	fresh(t)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from c"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(up.Close)
	down := deadAddr(t, up.Listener.Addr().String())
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Key: "ka", Models: []string{"m"}, Chat: "http://a.test/v1", Proxy: "http://" + down},
		{ID: "b", Name: "B", Key: "kb", Models: []string{"m"}, Chat: "http://b.test/v1", Proxy: "http://" + down},
		{ID: "c", Name: "C", Key: "kc", Models: []string{"m"}, Chat: up.URL + "/v1"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "Grok", Members: []string{"a/m", "b/m", "c/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/grok","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from c") {
		t.Fatalf("%d %s", code, body)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 {
		t.Fatalf("the member behind the failed proxy was asked anyway: %+v", r.Tries)
	}
	if f := r.Tries[0]; f.ID != "a" || f.Fail != "proxy" || f.Rest != nil || !strings.Contains(f.Error, down) {
		t.Fatalf("a's try %+v", f)
	}
	if r.Tries[1].ID != "c" || r.Tries[1].Status != 200 {
		t.Fatalf("c's try %+v", r.Tries[1])
	}
	restingUntil.Lock()
	n := len(restingUntil.m)
	restingUntil.Unlock()
	if n != 0 {
		t.Fatalf("%d set aside over a proxy that was down", n)
	}
}

// TestProxyDownLastNotRetried (#2682): when every member is behind the
// same failed proxy and there is no other way out, the second isn't asked
// at all — the listener refused magpie within this request — and the
// agent gets the error at once, with no backoff round. Nobody is set
// aside: a new request tries the proxy again, so recovery keeps its pace.
func TestProxyDownLastNotRetried(t *testing.T) {
	fresh(t)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	down := deadAddr(t)
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Key: "ka", Models: []string{"m"}, Chat: "http://a.test/v1", Proxy: "http://" + down},
		{ID: "b", Name: "B", Key: "kb", Models: []string{"m"}, Chat: "http://b.test/v1", Proxy: "http://" + down},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "Grok", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	old := retryPause
	retryPause = time.Millisecond
	t.Cleanup(func() { retryPause = old })
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/grok","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusBadGateway || !strings.Contains(body, down) {
		t.Fatalf("%d %s", code, body)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 1 {
		t.Fatalf("the failed proxy was asked %d times: %+v", len(r.Tries), r.Tries)
	}
	for _, f := range r.Tries {
		if f.Fail != "proxy" || f.Rest != nil || f.Again != 0 {
			t.Fatalf("try %+v", f)
		}
	}
	restingUntil.Lock()
	n := len(restingUntil.m)
	restingUntil.Unlock()
	if n != 0 {
		t.Fatalf("%d set aside over a proxy that was down", n)
	}
}

// TestProxyDownDistinctProxiesLastNotRetried (#2682): two members behind
// two different dead proxies, no other way out. The last one is asked
// once — its own exit hasn't failed yet — but not again after a backoff:
// its listener refused magpie within this request, and it refuses again a
// moment later.
func TestProxyDownDistinctProxiesLastNotRetried(t *testing.T) {
	fresh(t)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	down1 := deadAddr(t)
	down2 := deadAddr(t, down1)
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Key: "ka", Models: []string{"m"}, Chat: "http://a.test/v1", Proxy: "http://" + down1},
		{ID: "b", Name: "B", Key: "kb", Models: []string{"m"}, Chat: "http://b.test/v1", Proxy: "http://" + down2},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "Grok", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	old := retryPause
	retryPause = time.Millisecond
	t.Cleanup(func() { retryPause = old })
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/grok","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusBadGateway || !strings.Contains(body, down2) {
		t.Fatalf("%d %s", code, body)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 {
		t.Fatalf("the failed proxy was asked %d times: %+v", len(r.Tries), r.Tries)
	}
	for _, f := range r.Tries {
		if f.Fail != "proxy" || f.Rest != nil || f.Again != 0 {
			t.Fatalf("try %+v", f)
		}
	}
}

// TestProxyDownGlobalLeavesALoopbackMemberDirect (#2682): the global proxy
// is down. A member that would really go out through it is skipped for the
// rest of the request; a member at loopback (Ollama, LM Studio) never goes
// through it — netproxy sends loopback direct whatever the setting — so it
// is asked and answers. The shared thing is the exit a member's request
// resolves to, not its proxy choice: two members that both follow the
// global setting don't share an exit when only one of them has one.
func TestProxyDownGlobalLeavesALoopbackMemberDirect(t *testing.T) {
	fresh(t)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"b","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from b"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(up.Close)
	down := deadAddr(t, up.Listener.Addr().String())
	os.MkdirAll(settings.Dir(), 0o755)
	if err := os.WriteFile(settings.Path(), []byte(`{"proxy":"http://`+down+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Key: "ka", Models: []string{"m"}, Chat: "http://a.test/v1"},
		{ID: "b", Name: "B", Key: "kb", Models: []string{"m"}, Chat: up.URL + "/v1"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "Grok", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/grok","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from b") {
		t.Fatalf("the loopback member wasn't asked: %d %s", code, body)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 {
		t.Fatalf("tries: %+v", r.Tries)
	}
	if f := r.Tries[0]; f.ID != "a" || f.Fail != "proxy" || f.Rest != nil || f.Again != 0 || !strings.Contains(f.Error, down) {
		t.Fatalf("a's try %+v", f)
	}
	if r.Tries[1].ID != "b" || r.Tries[1].Status != 200 {
		t.Fatalf("b's try %+v", r.Tries[1])
	}
	restingUntil.Lock()
	n := len(restingUntil.m)
	restingUntil.Unlock()
	if n != 0 {
		t.Fatalf("%d set aside over a proxy that was down", n)
	}
}

// TestProxyDownAsksAMemberOnItsOwnProxy (#2682): the global proxy is down,
// and the next member's own proxy is another exit. Its request doesn't
// resolve to the exit that just refused magpie, so it is asked: its dial
// fails on its own listener, and gets the no-retry treatment with it.
func TestProxyDownAsksAMemberOnItsOwnProxy(t *testing.T) {
	fresh(t)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	down := deadAddr(t)
	own := deadAddr(t, down)
	os.MkdirAll(settings.Dir(), 0o755)
	if err := os.WriteFile(settings.Path(), []byte(`{"proxy":"http://`+down+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Key: "ka", Models: []string{"m"}, Chat: "http://a.test/v1"},
		{ID: "b", Name: "B", Key: "kb", Models: []string{"m"}, Chat: "http://b.test/v1", Proxy: "http://" + own},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "Grok", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	old := retryPause
	retryPause = time.Millisecond
	t.Cleanup(func() { retryPause = old })
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/grok","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusBadGateway || !strings.Contains(body, own) {
		t.Fatalf("%d %s", code, body)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 {
		t.Fatalf("the member on its own proxy wasn't asked: %+v", r.Tries)
	}
	if f := r.Tries[0]; f.ID != "a" || f.Fail != "proxy" || !strings.Contains(f.Error, down) {
		t.Fatalf("a's try %+v", f)
	}
	if f := r.Tries[1]; f.ID != "b" || f.Fail != "proxy" || f.Rest != nil || f.Again != 0 || !strings.Contains(f.Error, own) {
		t.Fatalf("b's try %+v", f)
	}
}

// deadAddr is a loopback address nothing listens on, for a proxy that is
// down. It is taken after the test's own servers are up and is never one of
// theirs: a port just let go can be handed out again at once (Linux CI gave
// the closed port to the next httptest server, so the "down" proxy answered).
func deadAddr(t *testing.T, taken ...string) string {
	t.Helper()
	for range 20 {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		a := l.Addr().String()
		l.Close()
		if !slices.Contains(taken, a) {
			return a
		}
	}
	t.Fatal("no free loopback port apart from the test's servers")
	return ""
}
