package netproxy

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The hybrid post-quantum key share (#4077: a provider's API answered
// curl, and Go alone timed its TLS handshake out, direct, on every model
// test). Go 1.24 sends a hybrid post-quantum key exchange first by
// default, X25519MLKEM768, whose ClientHello is ~1500 bytes where the
// classical ones are ~300 — big enough that some networks and middleboxes
// drop it, and the handshake never completes. A host whose handshake
// fails this way is remembered for a while, and its requests go without
// the hybrid share — classical curves alone: the request that met the
// failure goes again at once when its body can be had again, and is said
// plainly why when it can't. The mark fades after pqBroken, so a host
// whose path got fixed speaks the hybrid share again.

// pqBroken is how long a host:port whose TLS handshake timed out this way
// goes without the hybrid key share.
const pqBroken = 30 * time.Minute

var pqOnly struct {
	sync.Mutex
	at map[string]time.Time // host:port → when its handshake timed out
}

func pqSince(addr string) time.Time {
	pqOnly.Lock()
	defer pqOnly.Unlock()
	at := pqOnly.at[addr]
	if !at.IsZero() && time.Since(at) > pqBroken {
		delete(pqOnly.at, addr)
		return time.Time{}
	}
	return at
}

func markPQ(addr string) {
	pqOnly.Lock()
	defer pqOnly.Unlock()
	if pqOnly.at == nil {
		pqOnly.at = map[string]time.Time{}
	}
	pqOnly.at[addr] = time.Now()
}

// ClassicalTLS is base with the classical key exchanges alone (X25519,
// then P-256), no hybrid post-quantum share; base's other settings kept.
func ClassicalTLS(base *tls.Config) *tls.Config {
	if base == nil {
		return &tls.Config{CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256}}
	}
	c := base.Clone()
	c.CurvePreferences = []tls.CurveID{tls.X25519, tls.CurveP256}
	return c
}

// ClassicalRetry sends a request through RoundTripper — and when that
// fails with a timed-out TLS handshake, the way one whose large
// post-quantum ClientHello the path drops fails (HandshakeTimedOut),
// through Alt, which goes without the share: at once, on a copy of the
// body, when the request can be sent again; never (the host marked, so
// the next ones go without from the start) when it can't, with
// ErrPQHandshake saying why. A host marked before, within pqBroken, goes
// to Alt from the start.
type ClassicalRetry struct {
	RoundTripper http.RoundTripper
	Alt          http.RoundTripper
}

func (t ClassicalRetry) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL != nil && !pqSince(hostPort(req)).IsZero() {
		return t.Alt.RoundTrip(req)
	}
	res, err := t.RoundTripper.RoundTrip(req)
	if err == nil || req.URL == nil || !HandshakeTimedOut(err) {
		return res, err
	}
	markPQ(hostPort(req))
	if t.Alt == nil || req.Context().Err() != nil {
		return nil, ErrPQHandshake{err}
	}
	again := req.Clone(req.Context())
	if req.Body != nil && req.Body != http.NoBody {
		if req.GetBody == nil {
			return nil, ErrPQHandshake{err}
		}
		body, berr := req.GetBody()
		if berr != nil {
			return nil, err
		}
		again.Body = body
	}
	return t.Alt.RoundTrip(again)
}

// classicalDefault is a transport like the default one, with the
// classical key exchanges alone: what ClassicalRetry's Alt goes through.
func classicalDefault() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = ClassicalTLS(t.TLSClientConfig)
	return t
}

// HandshakeTimedOut says err is Go's transport giving up on a TLS
// handshake that got no answer — the way one whose ClientHello the path
// drops times out. A vendor that answers slowly later in the request is
// another error.
func HandshakeTimedOut(err error) bool {
	return strings.Contains(err.Error(), "TLS handshake timeout")
}

// ErrPQHandshake is the error a request ends with when its TLS handshake
// timed out this way and it could not be sent again: what happened, with
// Go's own words kept in front (the request log and the offline words
// read them) and what it means for the next requests.
type ErrPQHandshake struct{ err error }

func (e ErrPQHandshake) Error() string {
	return fmt.Sprintf("%v (the TLS handshake got no answer: this network drops Go's large post-quantum key share, so this host's requests go without it from now on)", e.err)
}

func (e ErrPQHandshake) Unwrap() error { return e.err }
