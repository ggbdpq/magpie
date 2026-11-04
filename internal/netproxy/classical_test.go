package netproxy

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The vendor is example.com: the base transport dials a connection that is
// accepted and then says nothing — the handshake times out, the way one
// whose large post-quantum ClientHello the path dropped does (#4077) —
// and Alt dials a real TLS server.
type pqTestUpstream struct {
	srv   *httptest.Server
	hole  net.Listener
	held  chan net.Conn
	done  chan struct{}
	dials []string
	mu    sync.Mutex
}

func newPQTestUpstream(t *testing.T) *pqTestUpstream {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		io.WriteString(w, r.Method+" "+string(b))
	}))
	srv.EnableHTTP2 = false
	srv.StartTLS()
	holeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	q := &pqTestUpstream{srv: srv, hole: holeLn, held: make(chan net.Conn, 8), done: make(chan struct{})}
	go func() {
		for {
			c, err := holeLn.Accept()
			if err != nil {
				return
			}
			select {
			case q.held <- c:
			case <-q.done:
				c.Close()
			}
		}
	}()
	t.Cleanup(func() {
		close(q.done)
		holeLn.Close()
		srv.Close()
		for {
			select {
			case c := <-q.held:
				c.Close()
			default:
				return
			}
		}
	})
	return q
}

func (q *pqTestUpstream) client() *http.Client {
	tlsConf := q.srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	dialer := &net.Dialer{}
	base := &http.Transport{
		TLSClientConfig:     tlsConf,
		TLSHandshakeTimeout: 300 * time.Millisecond,
	}
	base.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		q.mu.Lock()
		q.dials = append(q.dials, "base")
		q.mu.Unlock()
		return dialer.DialContext(ctx, "tcp", q.hole.Addr().String())
	}
	alt := &http.Transport{
		TLSClientConfig:     ClassicalTLS(tlsConf),
		TLSHandshakeTimeout: 300 * time.Millisecond,
	}
	alt.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		q.mu.Lock()
		q.dials = append(q.dials, "alt")
		q.mu.Unlock()
		return dialer.DialContext(ctx, "tcp", q.srv.Listener.Addr().String())
	}
	return &http.Client{Transport: ClassicalRetry{RoundTripper: base, Alt: alt}}
}

func (q *pqTestUpstream) dialed() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return strings.Join(q.dials, ",")
}

func cleanPQMarks(t *testing.T) {
	t.Cleanup(func() { pqOnly.Lock(); pqOnly.at = nil; pqOnly.Unlock() })
}

// A handshake that gets no answer — the large post-quantum ClientHello
// dropped by the path — is not the end of the request: it goes again
// without the hybrid share and is answered, and the host goes without it
// from the start after that.
func TestClassicalRetrySendsAgainWithoutTheHybridShare(t *testing.T) {
	cleanPQMarks(t)
	q := newPQTestUpstream(t)
	cl := q.client()
	res, err := cl.Get("https://example.com/v1/models")
	if err != nil {
		t.Fatalf("a request whose handshake got no answer did not go again without the share: %v", err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if got := q.dialed(); got != "base,alt" {
		t.Fatalf("dials %s, want base,alt", got)
	}
	if pqSince("example.com:443").IsZero() {
		t.Fatalf("the host was not marked")
	}
	// the next request goes without the share from the start, on the
	// connection the first one's second try left
	res, err = cl.Get("https://example.com/v1/models")
	if err != nil {
		t.Fatalf("a marked host's request failed: %v", err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if got := q.dialed(); got != "base,alt" {
		t.Fatalf("dials %s, want base,alt", got)
	}
}

// A body that can't be had again is not sent again: the request fails
// saying why, and the host still goes without the share from the next
// request on.
func TestAnUnreplayableBodySaysWhyAndMarksTheHost(t *testing.T) {
	cleanPQMarks(t)
	q := newPQTestUpstream(t)
	cl := q.client()
	req, _ := http.NewRequest(http.MethodPost, "https://example.com/v1/responses", plainReader{strings.NewReader("turn")})
	_, err := cl.Do(req)
	var pq ErrPQHandshake
	if !errors.As(err, &pq) {
		t.Fatalf("err = %v, want ErrPQHandshake", err)
	}
	if !strings.Contains(err.Error(), "TLS handshake timeout") || !strings.Contains(err.Error(), "without it") {
		t.Errorf("err = %v, want Go's words and the why", err)
	}
	res, err := cl.Get("https://example.com/v1/models")
	if err != nil {
		t.Fatalf("the marked host's next request failed: %v", err)
	}
	res.Body.Close()
	if got := q.dialed(); got != "base,alt" {
		t.Fatalf("dials %s, want base,alt", got)
	}
}

type plainReader struct{ io.Reader }

// The classical config keeps base's settings and drops the hybrid share
// alone.
func TestTheClassicalConfigDropsTheHybridShareAlone(t *testing.T) {
	base := &tls.Config{MinVersion: tls.VersionTLS12}
	c := ClassicalTLS(base)
	if c.MinVersion != tls.VersionTLS12 || c == base {
		t.Errorf("base's settings not kept, or the config shared")
	}
	if len(c.CurvePreferences) != 2 || c.CurvePreferences[0] != tls.X25519 || c.CurvePreferences[1] != tls.CurveP256 {
		t.Errorf("CurvePreferences %v, want X25519 then P-256", c.CurvePreferences)
	}
}

// The mark fades: after pqBroken the host is asked with the share again,
// and met with the timeout, it is marked and sent without once more.
func TestTheMarkFades(t *testing.T) {
	cleanPQMarks(t)
	q := newPQTestUpstream(t)
	cl := q.client()
	pqOnly.Lock()
	pqOnly.at = map[string]time.Time{"example.com:443": time.Now().Add(-pqBroken - time.Minute)}
	pqOnly.Unlock()
	res, err := cl.Get("https://example.com/v1/models")
	if err != nil {
		t.Fatalf("a request after the mark faded did not go again without the share: %v", err)
	}
	res.Body.Close()
	if got := q.dialed(); got != "base,alt" {
		t.Fatalf("dials %s, want base,alt", got)
	}
}
