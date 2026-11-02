package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// carriers records what a caller's correlation carriers reached the
// provider as: the query string, the headers and the raw body (#4899).
var head = map[string]string{"X-Cid": "abc123"}

type carriers struct {
	mu    sync.Mutex
	query string
	head  http.Header
	body  []byte
	calls int
	// refuseMetadata, when set, answers the first ask with it and serves
	// the next as chatReply
	refuseMetadata string
}

func (c *carriers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.query, c.head, c.body, c.calls = r.URL.RawQuery, r.Header, raw, c.calls+1
	refuse := c.refuseMetadata != "" && c.calls == 1
	c.mu.Unlock()
	if refuse {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, c.refuseMetadata)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, chatReply)
}

func (c *carriers) got() (string, http.Header, []byte, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.query, c.head, c.body, c.calls
}

// The caller's correlation carriers ride a translated request: the body's
// metadata object, the X-Cid header and the URL's query string. A provider
// configured by its URL alone speaks Chat Completions, so an Anthropic
// Messages or an OpenAI Responses request is translated — and none of the
// three reached the provider, which left a caller unable to pair its turns
// with what the provider served (#4899). A gateway key the caller gave in
// the query stays magpie's.
func TestTranslatedRequestKeepsCallerCarriers(t *testing.T) {
	ask := func(t *testing.T, path, body string, head map[string]string) (query, cid string, metadata gjson.Result, calls int) {
		t.Helper()
		fresh(t)
		up := &carriers{}
		hs := httptest.NewServer(up)
		t.Cleanup(hs.Close)
		if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"},
			Chat: hs.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
		code, reply := postWith(t, path, body, head)
		if code != 200 {
			t.Fatalf("status %d: %s", code, reply)
		}
		q, h, b, n := up.got()
		return q, h.Get("X-Cid"), gjson.GetBytes(b, "metadata"), n
	}
	const inbound = `{"model":"m1","max_tokens":100,"metadata":{"cid":"abc123"},"messages":[{"role":"user","content":"hi"}]}`

	t.Run("anthropic in", func(t *testing.T) {
		query, cid, metadata, calls := ask(t, "/v1/messages?cid=abc123&key=magpie-gateway-key", inbound, head)
		if !strings.Contains(query, "cid=abc123") {
			t.Errorf("the caller's query %q lost its cid", query)
		}
		if strings.Contains(query, "magpie-gateway-key") {
			t.Errorf("the caller's gateway key went to the provider: %q", query)
		}
		if cid != "abc123" {
			t.Errorf("X-Cid %q lost", cid)
		}
		if metadata.Get("cid").String() != "abc123" {
			t.Errorf("metadata %s lost", metadata.Raw)
		}
		if calls != 1 {
			t.Errorf("asked %d times", calls)
		}
	})

	t.Run("responses in", func(t *testing.T) {
		query, cid, metadata, calls := ask(t, "/v1/responses?cid=r456",
			`{"model":"m1","metadata":{"cid":"r456"},"input":"hi"}`, head)
		if !strings.Contains(query, "cid=r456") {
			t.Errorf("the caller's query %q lost its cid", query)
		}
		if cid != "abc123" {
			t.Errorf("X-Cid %q lost", cid)
		}
		if metadata.Get("cid").String() != "r456" {
			t.Errorf("metadata %s lost", metadata.Raw)
		}
		if calls != 1 {
			t.Errorf("asked %d times", calls)
		}
	})

	t.Run("anthropic in streamed", func(t *testing.T) {
		fresh(t)
		up := &carriers{}
		hs := httptest.NewServer(up)
		t.Cleanup(hs.Close)
		if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"},
			Chat: hs.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
		code, reply := postWith(t, "/v1/messages?cid=abc123", `{"model":"m1","max_tokens":100,"stream":true,"metadata":{"cid":"abc123"},"messages":[{"role":"user","content":"hi"}]}`, head)
		if code != 200 {
			t.Fatalf("status %d: %s", code, reply)
		}
		q, h, b, _ := up.got()
		if !strings.Contains(q, "cid=abc123") {
			t.Errorf("the caller's query %q lost its cid", q)
		}
		if h.Get("X-Cid") != "abc123" {
			t.Errorf("X-Cid %q lost", h.Get("X-Cid"))
		}
		if gjson.GetBytes(b, "metadata.cid").String() != "abc123" {
			t.Errorf("metadata %s lost", gjson.GetBytes(b, "metadata").Raw)
		}
	})

	t.Run("an upstream that turns the metadata away is asked without it", func(t *testing.T) {
		fresh(t)
		up := &carriers{refuseMetadata: `{"error":{"message":"Unrecognized request argument supplied: metadata","type":"invalid_request_error"}}`}
		hs := httptest.NewServer(up)
		t.Cleanup(hs.Close)
		if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"},
			Chat: hs.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
		code, reply := postWith(t, "/v1/messages?cid=abc123", inbound, head)
		if code != 200 {
			t.Fatalf("status %d: %s", code, reply)
		}
		_, _, b, calls := up.got()
		if calls != 2 {
			t.Fatalf("asked %d times, want a refusal and a turn without the metadata", calls)
		}
		if gjson.GetBytes(b, "metadata").Exists() {
			t.Errorf("the metadata came back after the refusal: %s", gjson.GetBytes(b, "metadata").Raw)
		}
	})
}

// The header rides a relayed request too: what the caller put on goes to
// the provider as the caller sent it, its key to magpie never leaving.
func TestRelayedRequestKeepsCallerCidHeader(t *testing.T) {
	fresh(t)
	up := &carriers{}
	hs := httptest.NewServer(up)
	t.Cleanup(hs.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"},
		Anthropic: hs.URL}); err != nil {
		t.Fatal(err)
	}
	code, reply := postWith(t, "/v1/messages", `{"model":"m1","max_tokens":100,"metadata":{"cid":"abc123"},"messages":[{"role":"user","content":"hi"}]}`, head)
	if code != 200 {
		t.Fatalf("status %d: %s", code, reply)
	}
	_, h, _, _ := up.got()
	if h.Get("X-Cid") != "abc123" {
		t.Errorf("X-Cid %q lost on the relay", h.Get("X-Cid"))
	}
}
