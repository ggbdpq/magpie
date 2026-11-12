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
	if r := s.restAfter(acct, http.StatusOK, http.Header{}, []byte(cut)); r.By == "off" && r.Until.Before(time.Now()) {
		// said off, nothing kept — checked again below through restOf
		_ = r
	}
	if r, ok := restOf(acct.restKey()); ok {
		t.Fatalf("a mid-reply cut rested the account: %v by %s (%s)", time.Until(r.Until).Round(time.Second), r.By, r.Error)
	}

	// the same for a 502 whose body is the read that failed, magpie's
	// wrapper around a stream severed before it said anything
	if _, ok := restOf(acct.restKey()); ok {
		t.Fatal("still resting before the next try")
	}
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
