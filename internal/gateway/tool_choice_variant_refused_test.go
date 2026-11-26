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

// #7714: a DeepSeek-family Anthropic endpoint whose tool_choice deserializer
// knows the `function` variant alone turns a request carrying the
// protocol's own choices away: 422 "Failed to deserialize the JSON body
// into the target type: tool_choice: field `type`: unknown variant `auto`,
// expected `function`" (reported on zcode and Claude Code).
// Both relayed turns and translated turns that build tool_choice
// (parallel_tool_calls: false -> {"type":"auto","disable_parallel_tool_use":true})
// pass through forwardOnce/withoutRefusedShapes; once refused, the provider
// is asked again immediately without tool_choice and sent none thereafter.
func TestToolChoiceVariantRefusedLeftOut(t *testing.T) {
	const refusal = `{"type":"error","error":{"type":"invalid_request_error","message":"Failed to deserialize the JSON body into the target type: tool_choice: field ` + "`type`" + `: unknown variant ` + "`auto`" + `, expected ` + "`function`" + ` at line 1 column 38640 (request_id: 7042836d-cdba-478b-9737-e187dc00ef36)"}}`

	newRig := func(t *testing.T) (http.Handler, func() []map[string]any) {
		t.Helper()
		fresh(t)
		var mu sync.Mutex
		var bodies []map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			mu.Lock()
			bodies = append(bodies, m)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if _, ok := m["tool_choice"]; ok {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = io.WriteString(w, refusal)
				return
			}
			if m["stream"] == true {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, sse(
					`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1}}}`,
					`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
					`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
					`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
					`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
					`event: message_stop`+"\n"+`data: {"type":"message_stop"}`))
				return
			}
			_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
		}))
		t.Cleanup(srv.Close)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		if err := provider.Save(provider.Provider{
			ID:        "ds",
			Name:      "ds",
			Key:       "k",
			Anthropic: srv.URL,
			Models:    []string{"custom/deepseek-v4-flash", "claude-opus-5-5"},
		}); err != nil {
			t.Fatal(err)
		}
		all := func() []map[string]any {
			mu.Lock()
			defer mu.Unlock()
			return append([]map[string]any(nil), bodies...)
		}
		return New().Handler(), all
	}

	// Relayed Anthropic turn from an agent (zcode / Claude Code).
	t.Run("relayed", func(t *testing.T) {
		h, all := newRig(t)
		body := `{"model":"ds/custom/deepseek-v4-flash","max_tokens":64,` +
			`"tools":[{"name":"read","description":"Reads a file.","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],` +
			`"tool_choice":{"type":"auto","disable_parallel_tool_use":true},` +
			`"messages":[{"role":"user","content":[{"type":"text","text":"say pong"}]}]}`
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		sent := all()
		if len(sent) != 2 {
			t.Fatalf("asked %d times, want 2 (initial 422 + retry without tool_choice)", len(sent))
		}
		if _, ok := sent[0]["tool_choice"]; !ok {
			t.Fatalf("first ask had no tool_choice: %v", sent[0])
		}
		if _, ok := sent[1]["tool_choice"]; ok {
			t.Fatalf("retry still carried tool_choice: %v", sent[1])
		}
		if msgs, _ := sent[1]["messages"].([]any); len(msgs) != 1 {
			t.Fatalf("messages = %v", sent[1]["messages"])
		}

		// Learned: the next request goes without tool_choice on the first try.
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("second turn status %d: %s", rec.Code, rec.Body)
		}
		if sent = all(); len(sent) != 3 {
			t.Fatalf("asked %d times total, want 3", len(sent))
		}
		if _, ok := sent[2]["tool_choice"]; ok {
			t.Fatalf("learned provider still got tool_choice: %v", sent[2])
		}
	})

	// Translated Chat turn onto Messages API that builds tool_choice
	// when parallel_tool_calls is false.
	t.Run("built", func(t *testing.T) {
		h, all := newRig(t)
		body := `{"model":"ds/claude-opus-5-5","max_tokens":64,` +
			`"tools":[{"type":"function","function":{"name":"read","description":"Reads a file.","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}],` +
			`"tool_choice":"auto","parallel_tool_calls":false,` +
			`"messages":[{"role":"user","content":"say pong"}]}`
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		sent := all()
		if len(sent) != 2 {
			t.Fatalf("asked %d times, want 2 (built tool_choice refused, then retried without)", len(sent))
		}
		if _, ok := sent[0]["tool_choice"]; !ok {
			t.Fatalf("first built ask had no tool_choice: %v", sent[0])
		}
		if _, ok := sent[1]["tool_choice"]; ok {
			t.Fatalf("retried built request kept tool_choice: %v", sent[1]["tool_choice"])
		}

		// Learned: subsequent built request skips tool_choice up front.
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("second built turn status %d: %s", rec.Code, rec.Body)
		}
		if sent = all(); len(sent) != 3 {
			t.Fatalf("asked %d times total, want 3", len(sent))
		}
		if _, ok := sent[2]["tool_choice"]; ok {
			t.Fatalf("learned built request kept tool_choice: %v", sent[2]["tool_choice"])
		}
	})
}

// A provider that accepts tool_choice (Anthropic's API, or a DeepSeek endpoint
// supporting it) keeps receiving it; an unrelated 422 does not strip it.
func TestToolChoiceKeptWhereTaken(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{
		ID:        "ant",
		Name:      "Ant",
		Key:       "k",
		Anthropic: srv.URL,
		Models:    []string{"custom/deepseek-v4-flash"},
	}); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"ant/custom/deepseek-v4-flash","max_tokens":64,` +
		`"tools":[{"name":"read","description":"Reads a file.","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],` +
		`"tool_choice":{"type":"auto","disable_parallel_tool_use":true},` +
		`"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("asked %d times, want 1", len(got))
	}
	if _, ok := got[0]["tool_choice"]; !ok {
		t.Fatalf("tool_choice left out where accepted: %v", got[0])
	}
}
