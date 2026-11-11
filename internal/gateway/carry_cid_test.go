package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"

	"sync"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// cidStub records what reached it — the query string, the headers and the
// raw body — and answers an SSE stream, as Chat Completions or, when told,
// as Anthropic (#4899).
type cidStub struct {
	mu          sync.Mutex
	query       string
	head        http.Header
	body        []byte
	asAnthropic bool
}

func (c *cidStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.query, c.head, c.body = r.URL.RawQuery, r.Header, raw
	anthropic := c.asAnthropic
	c.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	if anthropic {
		io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\",\"type\":\"message\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
		io.WriteString(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"OK\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		io.WriteString(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
		io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
		return
	}
	io.WriteString(w, chatReply)
}

func (c *cidStub) got() (string, http.Header, []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.query, c.head, c.body
}

// The caller's correlation carriers against the review's named faces: a
// request to a translated provider carries Claude Code's own metadata
// (`user_id`, the account uuid and session) in the body, an `X-Cid` header
// and a `cid` query — as the report asked for. None of the three may leave
// unless the user marked that one provider as taking the header, and even
// then only the header goes, only to a translated endpoint that is not a
// signed-in account's: no metadata (it would hand the account uuid to the
// vendor), no query (it would add ?beta=true upstream, and a filter of key
// names misses the ones it doesn't know), never a subscription's vendor
// (it could fingerprint magpie by the header) (#4899).
const cidBody = `{"model":"fake/m1","max_tokens":100,"metadata":{"user_id":"user_aa11bb22_account_uuid_session_cc33"},"messages":[{"role":"user","content":"hi"}]}`

func saveCidProvider(t *testing.T, url, field string) {
	t.Helper()
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}}
	switch field {
	case "chat":
		p.Chat = url + "/v1"
	case "chat-carry":
		p.Chat = url + "/v1"
		p.CarryCid = true
	case "relay-carry":
		p.Anthropic = url
		p.CarryCid = true
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// A provider the user marked carry-cid=yes: a translated request of its
// carries the caller's X-Cid header, and nothing else of the three — no
// metadata, no query string (#4899).
func TestTranslatedRequestCarriesOnlyTheOptedInCidHeader(t *testing.T) {
	fresh(t)
	up := &cidStub{}
	hs := httptest.NewServer(up)
	t.Cleanup(hs.Close)
	saveCidProvider(t, hs.URL, "chat-carry")
	head := map[string]string{"X-Cid": "abc123"}
	code, reply := postWith(t, "/v1/messages?cid=abc123", cidBody, head)
	if code != 200 {
		t.Fatalf("status %d: %s", code, reply)
	}
	q, h, b := up.got()
	if h.Get("X-Cid") != "abc123" {
		t.Errorf("the opted-in provider's upstream got X-Cid %q, want the caller's", h.Get("X-Cid"))
	}
	if q != "" {
		t.Errorf("the caller's query %q went upstream; the query stays dropped", q)
	}
	if gjson.GetBytes(b, "metadata").Exists() {
		t.Errorf("metadata went upstream (Claude Code's user_id in it): %s", gjson.GetBytes(b, "metadata").Raw)
	}
}

// Off, the default, nothing changes: a provider not marked carries none of
// the three, as current main is (#4899).
func TestTranslatedRequestCarriesNothingByDefault(t *testing.T) {
	fresh(t)
	up := &cidStub{}
	hs := httptest.NewServer(up)
	t.Cleanup(hs.Close)
	saveCidProvider(t, hs.URL, "chat")
	head := map[string]string{"X-Cid": "abc123"}
	code, reply := postWith(t, "/v1/messages?cid=abc123", cidBody, head)
	if code != 200 {
		t.Fatalf("status %d: %s", code, reply)
	}
	q, h, b := up.got()
	if h.Get("X-Cid") != "" {
		t.Errorf("an unmarked provider's upstream got X-Cid %q", h.Get("X-Cid"))
	}
	if q != "" {
		t.Errorf("the caller's query %q went upstream", q)
	}
	if gjson.GetBytes(b, "metadata").Exists() {
		t.Errorf("metadata went upstream: %s", gjson.GetBytes(b, "metadata").Raw)
	}
}

// A relayed request carries none, however marked: the opt-in is the
// translated route's alone, and a vendor the caller reaches as itself —
// most of all a signed-in account's — is never handed the header (#4899).
func TestRelayedRequestCarriesNoCallerCidEvenWhenOptedIn(t *testing.T) {
	fresh(t)
	up := &cidStub{asAnthropic: true}
	hs := httptest.NewServer(up)
	t.Cleanup(hs.Close)
	saveCidProvider(t, hs.URL, "relay-carry")
	head := map[string]string{"X-Cid": "abc123"}
	code, reply := postWith(t, "/v1/messages", cidBody, head)
	if code != 200 {
		t.Fatalf("status %d: %s", code, reply)
	}
	_, h, _ := up.got()
	if h.Get("X-Cid") != "" {
		t.Errorf("the relay's upstream got X-Cid %q; the relay stays as it is", h.Get("X-Cid"))
	}
}

// Marked, but the caller sent no X-Cid: none is invented for it (#4899).
func TestOptedInProviderInventsNoCidHeader(t *testing.T) {
	fresh(t)
	up := &cidStub{}
	hs := httptest.NewServer(up)
	t.Cleanup(hs.Close)
	saveCidProvider(t, hs.URL, "chat-carry")
	code, reply := postWith(t, "/v1/messages", cidBody, nil)
	if code != 200 {
		t.Fatalf("status %d: %s", code, reply)
	}
	_, h, _ := up.got()
	if h.Get("X-Cid") != "" {
		t.Errorf("the upstream got an invented X-Cid %q", h.Get("X-Cid"))
	}
}
