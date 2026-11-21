package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A provider that asks for named unknown fields gets them on a
// cross-protocol build: a client that talks the Anthropic entry to an
// OpenAI-compatible upstream loses fields like chat_template_kwargs in
// the translation, and some vendors' reasoning toggles ride on exactly
// such a field (imxzt, #7376).
func TestPassFieldsCarryUnknownTopFields(t *testing.T) {
	fresh(t)
	var seen map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = map[string]any{}
		json.Unmarshal(body, &seen)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","model":"sol","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","model":"sol","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`,
			`data: [DONE]`))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{
		ID: "p1", Name: "P", Key: "k",
		Models:     []string{"sol"},
		Chat:       up.URL,
		PassFields: []string{"chat_template_kwargs", "my_custom_field"},
	}); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
		`{"model":"p1/sol","max_tokens":64,"stream":true,`+
			`"chat_template_kwargs":{"enable_thinking":false},`+
			`"my_custom_field":"here",`+
			`"messages":[{"role":"user","content":"hi"}]}`))
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := seen["chat_template_kwargs"]; !ok {
		t.Fatalf("the vendor's body lacks chat_template_kwargs though PassFields asked for it: keys=%v", jsonKeys(seen))
	}
	if _, ok := seen["my_custom_field"]; !ok {
		t.Fatalf("the vendor's body lacks my_custom_field though PassFields asked for it: keys=%v", jsonKeys(seen))
	}
	if seen["max_tokens"] != float64(64) {
		t.Errorf("the build's max_tokens was disturbed: %v", seen["max_tokens"])
	}
}

// Off, the default, a translated build carries only what it knows: the
// field the client sent goes no further, as before this change.
func TestPassFieldsOffDropsUnknownTopFields(t *testing.T) {
	fresh(t)
	var seen map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = map[string]any{}
		json.Unmarshal(body, &seen)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","model":"sol","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","model":"sol","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`,
			`data: [DONE]`))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{
		ID: "p1", Name: "P", Key: "k",
		Models: []string{"sol"},
		Chat:   up.URL,
	}); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
		`{"model":"p1/sol","max_tokens":64,"stream":true,`+
			`"chat_template_kwargs":{"enable_thinking":false},`+
			`"messages":[{"role":"user","content":"hi"}]}`))
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := seen["chat_template_kwargs"]; ok {
		t.Fatalf("with no PassFields the vendor's body carries the unknown field anyway: keys=%v", jsonKeys(seen))
	}
}

// A field the translation knows is the translation's to map: PassFields
// cannot overwrite what the build set from its own schema.
func TestPassFieldsNeverOverwriteKnownFields(t *testing.T) {
	fresh(t)
	var seen map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = map[string]any{}
		json.Unmarshal(body, &seen)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","model":"sol","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","model":"sol","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`,
			`data: [DONE]`))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{
		ID: "p1", Name: "P", Key: "k",
		Models:     []string{"sol"},
		Chat:       up.URL,
		PassFields: []string{"top_p"},
	}); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
		`{"model":"p1/sol","max_tokens":64,"stream":true,"top_p":0.9,`+
			`"messages":[{"role":"user","content":"hi"}]}`))
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if seen["top_p"] != 0.9 {
		t.Errorf("the build's own top_p was overwritten by the client's copy: %v", seen["top_p"])
	}
}

// The same carry works from the Chat entry to an Anthropic upstream:
// the capture is the entry parser's, not one path's.
func TestPassFieldsCarryFromChatEntryToAnthropicUpstream(t *testing.T) {
	fresh(t)
	var seen map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = map[string]any{}
		json.Unmarshal(body, &seen)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"sol","content":[],"usage":{"input_tokens":2,"output_tokens":0}}}`,
			`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}`,
			`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
			`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`event: message_stop`+"\n"+`data: {"type":"message_stop"}`))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{
		ID:         "p1",
		Name:       "P",
		Key:        "k",
		Models:     []string{"sol"},
		Anthropic:  up.URL,
		PassFields: []string{"my_custom_field"},
	}); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
		`{"model":"p1/sol","max_tokens":64,"stream":true,`+
			`"my_custom_field":"here",`+
			`"messages":[{"role":"user","content":"hi"}]}`))
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := seen["my_custom_field"]; !ok {
		t.Fatalf("the vendor's body lacks my_custom_field though PassFields asked for it: keys=%v", jsonKeys(seen))
	}
}

func jsonKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
