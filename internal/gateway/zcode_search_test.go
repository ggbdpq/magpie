package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A ZCode-shaped plugin (GLM-5.3 on Anthropic's API) whose upstream runs a
// web search tool itself: bigmodel's Anthropic API answers
// web_search_20250305 with server_tool_use and web_search_tool_result
// blocks — a custom GLM provider with search=yes had it searching on the
// same machine (#4402). The user says so with
// `magpie provider set zcode search=yes`; the CLI took the word and Save
// dropped it, as accountSettings keeps none of a signed-in account's
// Searches. The client's web_search was then stripped from the translated
// request, the vendor was offered no search tool, and the request still
// said 200 — the model answered it had no search tool.
func TestZCodePluginSearchYesKeepsTheTool(t *testing.T) {
	t.Setenv("FAKE_ZCODE", "1")
	var mu sync.Mutex
	var sent []map[string]any
	movedFake(t, "zcode", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var q map[string]any
		json.Unmarshal(b, &q)
		mu.Lock()
		sent = append(sent, q)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"GLM-5.3","content":[],"usage":{"input_tokens":2,"output_tokens":0}}}`,
			`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}`,
			`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
			`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`event: message_stop`+"\n"+`data: {"type":"message_stop"}`))
	}))
	p, err := provider.Find("zcode")
	if err != nil {
		t.Fatal(err)
	}
	if p.Searches {
		t.Fatal("the plugin already says it searches; the test's word adds nothing")
	}
	q := *p
	q.Searches = true // magpie provider set zcode search=yes
	if err := provider.Save(q); err != nil {
		t.Fatal(err)
	}
	if got, err := provider.Find("zcode"); err != nil {
		t.Fatal(err)
	} else if !got.Searches {
		t.Errorf("search=yes was not kept on the plugin's provider")
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"zcode/GLM-5.3","max_tokens":100,"stream":false,"messages":[{"role":"user","content":"search the web for the latest Go release"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1}]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 {
		t.Fatalf("asked %d times", len(sent))
	}
	tools, _ := json.Marshal(sent[0]["tools"])
	if !strings.Contains(string(tools), "web_search") {
		t.Fatalf("the vendor was offered %s", tools)
	}
}
