package gateway

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
)

// A try on a kept HTTP/2 connection has its TryNet: the calmed pool says
// GetConn when it hands the connection out, so timing() has the request's
// way out, and tries[].net keeps the Routing page's time — since the pool
// stopped saying it, a kept connection's try wrote no net at all and the
// page's "where the time went" said Masking alone (#6310, a keep-alive
// pair to one vendor).
func TestTimingOnAKeptCalmConnection(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, "ok")
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	tr := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	if err := netproxy.CalmHTTP2(tr); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tr.CloseIdleConnections)
	url := "https://" + srv.Listener.Addr().String() + "/turn"

	// two tries, the second riding the first's connection, as the
	// reporter's keep-alive pair: each with its own trace, as the try
	// loop builds them
	type one struct {
		nt                 *netTrace
		asking, asked, wrt atomic.Int64
	}
	tries := []*one{{nt: &netTrace{}}, {nt: &netTrace{}}}
	var began time.Time
	for i, o := range tries {
		ctx := httptrace.WithClientTrace(context.Background(), o.nt.hooks(&o.asking, &o.asked, &o.wrt))
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		res, err := tr.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if i == len(tries)-1 {
			began = time.Now().Add(-50 * time.Millisecond) // its own begin, before its GetConn
		}
	}
	tn := tries[1].nt.timing(began, 0)
	if tn == nil {
		t.Fatal("a kept connection's try has no TryNet; the Routing page's time says Masking alone")
	}
	if !tn.Reused {
		t.Errorf("the kept connection's try says reused %v; want it on the first's connection", tn.Reused)
	}
	if tn.Asks != 1 {
		t.Errorf("the kept connection's try says %d asks; want 1", tn.Asks)
	}
	tn1 := tries[0].nt.timing(time.Now().Add(-50*time.Millisecond), 0)
	if tn1 == nil {
		t.Fatal("the dialed try has no TryNet")
	}
	if tn1.Asks != 1 {
		t.Errorf("the dialed try says %d asks; want once, net/http's own", tn1.Asks)
	}
}
