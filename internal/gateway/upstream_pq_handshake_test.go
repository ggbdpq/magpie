package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// silentFirstUpstream is a TLS vendor whose first silentFirst new
// connections are accepted and then say nothing, so the client's handshake
// times out (a middlebox dropping the large post-quantum ClientHello,
// #4077); the rest are answered 200 with the body's length.
type silentFirstUpstream struct {
	addr    string
	accepts atomic.Int32
}

func newSilentFirstUpstream(t *testing.T, silentFirst int32) *silentFirstUpstream {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	srv.StartTLS()
	cert := srv.TLS.Certificates
	srv.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := &silentFirstUpstream{addr: ln.Addr().String()}
	served := make(chan net.Conn)
	done := make(chan struct{})
	conf := &tls.Config{Certificates: cert, NextProtos: []string{"http/1.1"}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n := u.accepts.Add(1)
			go func() {
				if n <= silentFirst {
					// accepted, then silent: the handshake never completes
					select {
					case <-done:
					}
					c.Close()
					return
				}
				tc := tls.Server(c, conf)
				if tc.Handshake() != nil {
					c.Close()
					return
				}
				select {
				case served <- tc:
				case <-done:
					c.Close()
				}
			}()
		}
	}()
	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		io.WriteString(w, http.StatusText(200)+" "+strconv.FormatInt(n, 10))
	})}
	go hs.Serve(&chanListener{c: served, done: done, addr: ln.Addr()})
	t.Cleanup(func() {
		ln.Close()
		close(done)
		hs.Close()
	})
	return u
}

func pqTestClient(t *testing.T) *http.Client {
	t.Helper()
	was := upstreamTLS
	upstreamTLS = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // the test's own server
	t.Cleanup(func() { upstreamTLS = was })
	cl := &http.Client{Transport: upstreamTransport()}
	t.Cleanup(cl.CloseIdleConnections)
	return cl
}

// pqGet sends a GET within pqTestWait: without the fix, a hung handshake
// is never given up on, and the request fails with its deadline.
func pqGet(t *testing.T, cl *http.Client, url string) (*http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), pqTestWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cl.Do(req)
}

// pqTestWait bounds a test request past the handshake's own 30s: the retry
// after it fits, the hung-handshake failure does not.
const pqTestWait = 45 * time.Second

// A vendor whose first handshake hangs — the middlebox dropped the large
// post-quantum ClientHello — is not the end of the request: it goes again
// without the hybrid share, on a new connection, and is answered.
func TestTLSHandshakeTimeoutGoesAgainWithoutThePostQuantumShare(t *testing.T) {
	u := newSilentFirstUpstream(t, 1)
	res, err := pqGet(t, pqTestClient(t), "https://"+u.addr+"/v1/models")
	if err != nil {
		t.Fatalf("a hung handshake did not go again without the post-quantum share: %v", err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Errorf("answered %d %q, want 200", res.StatusCode, b)
	}
	if n := u.accepts.Load(); n != 2 {
		t.Errorf("%d connections, want 2: one hung, one answered", n)
	}
}

// A POST whose body is replayable goes again whole: the vendor reads the
// same bytes on the connection that answers.
func TestPostWithABodyGoesAgainWholeWithoutTheShare(t *testing.T) {
	u := newSilentFirstUpstream(t, 1)
	body := bytes.Repeat([]byte("x"), 1<<20)
	ctx, cancel := context.WithTimeout(context.Background(), pqTestWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+u.addr+"/v1/responses", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := pqTestClient(t).Do(req)
	if err != nil {
		t.Fatalf("a POST whose handshake hung did not go again whole: %v", err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(b) != "OK "+strconv.Itoa(len(body)) {
		t.Errorf("answered %d %q, want 200 with the whole body", res.StatusCode, b)
	}
	if n := u.accepts.Load(); n != 2 {
		t.Errorf("%d connections, want 2", n)
	}
}
