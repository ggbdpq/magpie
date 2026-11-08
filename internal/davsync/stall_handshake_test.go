package davsync

// A TLS handshake that times out ends a request before any byte of it
// reached the server — the HTTP request is written only once the
// handshake is done — so it is sent again on a new connection (#5424:
// InfiniCLOUD's server times handshakes out often enough that sync said
// "TLS handshake timeout" while the backup itself had gone up fine).
// A stall after the request was sent is another matter and is not sent
// again: the server may have had it.

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// tlsHandshakeTimeoutError is what Go's transport answers with when the
// TLS handshake itself times out, as net/http's own type has it.
type tlsHandshakeTimeoutError struct{}

func (tlsHandshakeTimeoutError) Error() string   { return "net/http: TLS handshake timeout" }
func (tlsHandshakeTimeoutError) Timeout() bool   { return true }
func (tlsHandshakeTimeoutError) Temporary() bool { return true }

// funcTransport is a transport a test scripts each answer of.
type funcTransport func(*http.Request) (*http.Response, error)

func (f funcTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func answer(n int) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Status: fmt.Sprintf("%d OK", n),
		Body: io.NopCloser(strings.NewReader("ok")), Header: http.Header{}, ContentLength: 2}, nil
}

func TestHandshakeTimeoutIsSentAgain(t *testing.T) {
	var calls atomic.Int32
	tr := stallTransport{funcTransport(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return nil, tlsHandshakeTimeoutError{}
		}
		if err := r.Context().Err(); err != nil {
			t.Errorf("the attempt sent again runs on a cancelled context: %v", err)
		}
		return answer(2)
	})}
	req, _ := http.NewRequest(http.MethodGet, "https://higa.example.jp/dav/magpie/magpie.magpie-backup", nil)
	res, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := calls.Load(); got != 2 {
		t.Fatalf("a handshake timeout is sent again, %d attempts", got)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the answer of the second attempt comes back, got %s", res.Status)
	}
}

func TestHandshakeTimeoutBodyIsSentAgain(t *testing.T) {
	var calls atomic.Int32
	var got string
	tr := stallTransport{funcTransport(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return nil, tlsHandshakeTimeoutError{}
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("body of the attempt sent again: %v", err)
		}
		got = string(b)
		return answer(2)
	})}
	req, _ := http.NewRequest(http.MethodPut, "https://higa.example.jp/dav/magpie/magpie.magpie-backup", strings.NewReader("backup bytes"))
	res, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got != "backup bytes" || calls.Load() != 2 {
		t.Fatalf("a PUT over a handshake timeout goes again whole: %d attempts, body %q", calls.Load(), got)
	}
}

func TestHandshakeTimeoutWithoutGetBodyIsNotSentAgain(t *testing.T) {
	var calls atomic.Int32
	tr := stallTransport{funcTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, tlsHandshakeTimeoutError{}
	})}
	req, _ := http.NewRequest(http.MethodPost, "https://higa.example.jp/x", io.NopCloser(strings.NewReader("once")))
	req.GetBody = nil // a body that can't be had again is not sent again
	_, err := tr.RoundTrip(req)
	if err == nil {
		t.Fatal("the error comes back")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("a body without GetBody is not sent again, %d attempts", got)
	}
}

func TestStallAfterSentIsNotSentAgain(t *testing.T) {
	var calls atomic.Int32
	tr := stallTransport{funcTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		// a timeout on the line after the request went — the watchdog's
		// shape, not the handshake's
		return nil, errors.New(`Get "https://higa.example.jp/dav/f": timeout`)
	})}
	req, _ := http.NewRequest(http.MethodGet, "https://higa.example.jp/dav/f", nil)
	_, err := tr.RoundTrip(req)
	if err == nil {
		t.Fatal("the error comes back")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("a stall after the request was sent is not sent again, %d attempts", got)
	}
}
