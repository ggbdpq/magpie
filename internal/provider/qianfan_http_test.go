package provider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/netproxy"
)

// yideriwen (#8018): Qianfan's own gateway cancels a request body over
// ~2 MiB on HTTP/2 ("stream error: stream ID 17; CANCEL; received from
// peer", a 502 some 33s in) and resets one over HTTP/1.1 that doesn't
// wait for the 100; with Expect: 100-continue, 3 MiB goes. Whatever
// provider asks it — the baidu-qianfan preset's, or one pointed at
// qianfan.baidubce.com — is spoken HTTP/1.1, and only there.
func TestQianfanSpokenHTTP1(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, r.Proto)
	}))
	srv.EnableHTTP2 = true
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	d := netproxy.Dispatch(&http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{RootCAs: pool}})
	if c, ok := d.(interface{ CloseIdleConnections() }); ok {
		t.Cleanup(c.CloseIdleConnections)
	}
	proto := func(p Provider) string {
		t.Helper()
		req, _ := http.NewRequestWithContext(p.Via(context.Background()), http.MethodGet, srv.URL+"/v1/models", nil)
		res, err := d.RoundTrip(req)
		if err != nil {
			t.Fatalf("%s: %v", p.ID, err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	qn := Provider{ID: "qn", Name: "Qianfan", Preset: "baidu-qianfan", Chat: srv.URL + "/v2/tokenplan/personal", Models: []string{"m"}}
	if got := proto(qn); got != "HTTP/1.1" {
		t.Fatalf("the preset spoken %s, want HTTP/1.1", got)
	}
	// a custom provider pointed at Qianfan's own host is spoken it too
	custom := Provider{ID: "myqf", Name: "My Qianfan", Chat: "https://qianfan.baidubce.com/v2/tokenplan/personal", Models: []string{"m"}}
	if !custom.toQianfan() || len(custom.qianfanHTTP1Hosts()) == 0 {
		t.Fatal("a custom provider on Qianfan's host not taken")
	}
	// another host of its own is left as it is
	mixed := Provider{ID: "mix", Name: "Mix", Chat: "https://qianfan.baidubce.com/v2", Anthropic: srv.URL, Models: []string{"m"}}
	if hosts := mixed.qianfanHTTP1Hosts(); len(hosts) != 1 || HostOf(hosts[0]) != "qianfan.baidubce.com" {
		t.Fatalf("hosts %v, want Qianfan's alone", hosts)
	}
	plain := Provider{ID: "p", Name: "P", Chat: srv.URL + "/v2", Models: []string{"m"}}
	if got := proto(plain); got != "HTTP/2.0" {
		t.Fatalf("another vendor spoken %s, want HTTP/2.0", got)
	}
}

// ExpectsContinue: a body of a MiB or more to Qianfan asks for the 100
// first; a smaller one, and any other vendor's, is sent as it was.
func TestQianfanExpectsContinue(t *testing.T) {
	big := make([]byte, 1<<20+64)
	qn := Provider{ID: "qn", Preset: "baidu-qianfan", Chat: "https://qianfan.baidubce.com/v2/tokenplan/personal"}
	if !ExpectsContinue(qn, len(big)) {
		t.Fatal("a MiB body to Qianfan asks no 100")
	}
	if ExpectsContinue(qn, 1<<19) {
		t.Fatal("a half-MiB body asked for the 100")
	}
	other := Provider{ID: "o", Chat: "https://api.example.com/v1"}
	if ExpectsContinue(other, len(big)) {
		t.Fatal("another vendor asked for the 100")
	}
}
