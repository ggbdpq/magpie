package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A reply whose stream the upstream severed mid-answer ends as magpie's
// own words for it, under the 200 the vendor had already sent (#5252): a
// Codex reply ran 12m26s, died with "connection lost mid-reply
// (unexpected EOF)", and the account was rested for it — though the
// connection dropping is no fault of the account, and the same request is
// asked again. A 502 whose body is the read that failed was rested too:
// transportWords knew the words, but only offlineFailure read them, and it
// wants a down network. Neither rests now; an answer the vendor did give
// still rests as it did.
func TestMidReplyCutRestsNobody(t *testing.T) {
	fresh(t)
	s := New()
	acct := candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "me@example.com"}}, model: "gpt-6.1-sol", rest: "codex@me@example.com"}

	cut := "codex: connection lost mid-reply (unexpected EOF)"
	s.restAfter(acct, http.StatusOK, http.Header{}, []byte(cut))
	if r, ok := restOf(acct.restKey()); ok {
		t.Fatalf("a mid-reply cut rested the account: %v by %s (%s)", time.Until(r.Until).Round(time.Second), r.By, r.Error)
	}

	// the same for a 502 whose body is the read that failed, magpie's
	// wrapper around a stream severed before it said anything
	r := s.restAfter(acct, http.StatusBadGateway, http.Header{}, []byte("up: connection lost mid-reply (unexpected EOF)"))
	if r.By == "backoff" || r.By == "cooldown" {
		t.Fatalf("a severed read rested the account: %v by %s", time.Until(r.Until).Round(time.Second), r.By)
	}
	if _, ok := restOf(acct.restKey()); ok {
		t.Fatalf("a severed read left a rest behind: %v by %s", time.Until(r.Until).Round(time.Second), r.By)
	}

	// an answer the vendor did give rests as it did: a 502 with nothing
	// of the transport in it is the vendor failing, and the cooldown stands
	r = s.restAfter(acct, http.StatusBadGateway, http.Header{}, []byte(`{"error":{"message":"the model had an internal error"}}`))
	if r.By == "off" {
		t.Fatalf("a vendor's own 502 no longer rests: %v by %s", time.Until(r.Until).Round(time.Second), r.By)
	}
	if _, ok := restOf(acct.restKey()); !ok {
		t.Fatal("a vendor's own 502 rests nobody now")
	}
}

// A relay that hangs on every request ends as a timeout, in the words the
// transport says it with — i/o timeout, context deadline exceeded,
// Client.Timeout, timeout awaiting response headers, a TLS handshake that
// never finished. A connection that came up and then dropped is no fault
// of the account and rests nobody; one that never came up, or never came
// back in time, is: it rests, or nothing would ever fail over from a
// relay that answers nothing (yetone).
func TestHangingRelayStillRests(t *testing.T) {
	fresh(t)
	s := New()
	acct := candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "me@example.com"}}, model: "gpt-6.1-sol", rest: "codex@me@example.com"}

	for _, hang := range []string{
		"codex: the reply went silent past its stream idle: context deadline exceeded",
		`Post "https://api.openai.com/v1/replies": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`,
		"codex: timeout awaiting response headers",
		"codex: read tcp 10.0.0.8:52417->93.184.216.34:443: i/o timeout",
		"codex: TLS handshake timeout",
	} {
		r := s.restAfter(acct, http.StatusBadGateway, http.Header{}, []byte(hang))
		if _, ok := restOf(acct.restKey()); !ok {
			t.Fatalf("a hanging relay rested nobody: %.60s (said by %s)", hang, r.By)
		}
		clearRest(acct.restKey())
	}

	// a connection severed after it was up rests nobody, whatever carried
	// the words — the same exemption, its own class (unexpected EOF,
	// reset, closed, aborted, broken pipe, ECONNRESET, EPIPE)
	for _, cut := range []string{
		"codex: connection lost mid-reply (unexpected EOF)",
		"codex: read tcp 10.0.0.8:52417->93.184.216.34:443: connection reset",
		"codex: write tcp 10.0.0.8:52417->93.184.216.34:443: broken pipe",
		"codex: an existing connection was forcibly closed by the remote host",
		"codex: ECONNRESET while reading the reply",
	} {
		s.restAfter(acct, http.StatusBadGateway, http.Header{}, []byte(cut))
		if _, ok := restOf(acct.restKey()); ok {
			t.Fatalf("a severed connection rested the account: %.60s", cut)
		}
	}
}

// The state the report saw was the account off and the provider gone,
// "no provider codex", a re-sign-in bringing it back. A rest is not that
// shape: a rest lives in the gateway's rests alone, and the login's On is
// only ever written by an account command of the user's (provider
// logins' SetLoginOn) — no rest and no failure classification writes it.
// The provider stays listed whatever the account was rested by, the backoff
// ladder included; the gone provider must have come from the sign-in's own
// state (auth.json unreadable or lapsed), not from the cut's rest.
func TestMidReplyCutLeavesTheProviderAndItsLogin(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: "https://relay.example/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Find("relay"); err != nil {
		t.Fatal(err)
	}
	s := New()
	acct := candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "me@example.com"}}, model: "gpt-6.1-sol", rest: "codex@me@example.com"}

	// the report's cut: a 12-minute reply ends under the 200 the vendor
	// had already sent, in magpie's words for the severed read
	s.restAfter(acct, http.StatusOK, http.Header{}, []byte("codex: connection lost mid-reply (unexpected EOF)"))
	if r, ok := restOf(acct.restKey()); ok {
		t.Fatalf("a mid-reply cut rested the account: %v by %s", time.Until(r.Until).Round(time.Second), r.By)
	}
	if _, err := provider.Find("relay"); err != nil {
		t.Fatalf("a provider went away with the cut: %v", err)
	}

	// and under the backoff the old code made of it: rests, however many
	// and however long, leave the providers listed
	for i := 0; i < 3; i++ {
		s.restAfter(acct, http.StatusBadGateway, http.Header{}, []byte(`{"error":{"message":"the model had an internal error"}}`))
	}
	if _, err := provider.Find("relay"); err != nil {
		t.Fatalf("a provider went away with the rests: %v", err)
	}
}
