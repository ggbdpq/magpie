package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// An OpenAI-compatible endpoint whose model takes a reply's length as
// max_completion_tokens alone (#4261: gpt-6.1-sol on api.openai.com): the
// test, which names it max_tokens, is asked again under the name it takes,
// and a refusal over anything else stands as it came.
func TestProbeAsksCompletionTokensAgain(t *testing.T) {
	detectHome(t)
	const refusal = `{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","param":"max_tokens","code":"unsupported_parameter"}}`
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			io.WriteString(w, `{"data":[{"id":"gpt-6.1-sol"}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(body, &m)
		mu.Lock()
		bodies = append(bodies, m)
		mu.Unlock()
		if _, ok := m["max_tokens"]; ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, refusal)
			return
		}
		io.WriteString(w, `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	p := Provider{ID: "openai", Key: "sk-test", Chat: srv.URL + "/v1", Models: []string{"gpt-6.1-sol"}}
	rs := p.Test(context.Background())
	if len(rs) != 1 || !rs[0].OK {
		t.Fatalf("the test wasn't answered again: %+v", rs)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("asked %d times", len(bodies))
	}
	if _, ok := bodies[0]["max_tokens"]; !ok {
		t.Fatalf("the first ask named no max_tokens: %v", bodies[0])
	}
	if _, ok := bodies[1]["max_tokens"]; ok || bodies[1]["max_completion_tokens"] != float64(16) {
		t.Fatalf("asked again as: %v", bodies[1])
	}
}

func TestProbeOtherRefusalStands(t *testing.T) {
	detectHome(t)
	var mu sync.Mutex
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			io.WriteString(w, `{"data":[{"id":"gpt-6.1-sol"}]}`)
			return
		}
		io.ReadAll(r.Body)
		mu.Lock()
		asked++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"Invalid 'max_tokens': integer below minimum value.","param":"max_tokens","code":"integer_below_min"}}`)
	}))
	defer srv.Close()
	p := Provider{ID: "openai", Key: "sk-test", Chat: srv.URL + "/v1", Models: []string{"gpt-6.1-sol"}}
	rs := p.Test(context.Background())
	if len(rs) != 1 || rs[0].OK || !strings.Contains(rs[0].Error, "integer below minimum value") {
		t.Fatalf("the vendor's refusal wasn't the test's: %+v", rs)
	}
	mu.Lock()
	defer mu.Unlock()
	if asked != 1 {
		t.Fatalf("asked %d times", asked)
	}
}
