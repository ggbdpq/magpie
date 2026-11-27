package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// #7401: agy-cli cannot turn a model's thinking off — its requestedEffort
// reads a disabled ask as none and its stream drops the thinking blocks a
// disabled ask names, so telling the plugin `thinking: disabled` on a
// request that never mentioned thinking only hides the thinking the model
// does anyway. The request to the plugin goes as it was sent.
func TestAgyPluginKeepsItsThinkingUnannounced(t *testing.T) {
	var mu sync.Mutex
	var sent string
	t.Setenv("FAKE_AGY", "1")
	pid := besideFake(t, "agy-cli", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/messages") {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			sent = string(b)
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"fake-hydrogen\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"+
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"+
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"+
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		w.WriteHeader(404)
	}))
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"`+pid+`/fake-hydrogen","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != 200 {
		t.Fatalf("%d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(sent, `"thinking":{"type":"disabled"}`) {
		t.Fatalf("the request to the plugin was told thinking disabled: %s", sent)
	}
}
