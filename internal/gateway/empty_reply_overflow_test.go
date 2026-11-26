package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #7601: when an upstream answers HTTP 200 with an empty reply (reasoning only,
// no text or tool call) and its own reported prompt token count exceeds the
// model's context window, treat it as an overflow (HTTP 400 "prompt is too
// long") immediately — no in-place empty retry, no outer retry, skip mates of
// the same provider+model via withRoom, and record behindOverflow for the
// conversation. Sub-window empty replies keep the existing retry behavior.
func TestGeminiEmptyReplyOverWindowIsOverflow(t *testing.T) {
	geminiEmpty := func(promptTokens, cachedTokens int) string {
		// Gemini SSE: reasoning-only thought part + STOP + usageMetadata.
		ev1 := `{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"thinking..."}]}}],"modelVersion":"gemini-4-hydrogen"}`
		ev2 := `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":` +
			itoa(promptTokens) + `,"cachedContentTokenCount":` + itoa(cachedTokens) + `,"candidatesTokenCount":4,"thoughtsTokenCount":212,"totalTokenCount":` + itoa(promptTokens+216) + `},"modelVersion":"gemini-4-hydrogen"}`
		return "data: " + ev1 + "\n\ndata: " + ev2 + "\n\n"
	}
	geminiOK := func(text string) string {
		ev := `{"candidates":[{"content":{"role":"model","parts":[{"text":"` + text + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":5,"totalTokenCount":105},"modelVersion":"gemini-4-hydrogen"}`
		return "data: " + ev + "\n\n"
	}

	t.Run("translated_streaming_over_window", func(t *testing.T) {
		fresh(t)
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			calls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, geminiEmpty(1549512, 1544443))
		}))
		t.Cleanup(srv.Close)
		if err := provider.Save(provider.Provider{
			ID:       "goog",
			Name:     "Google",
			Key:      "k",
			Gemini:   srv.URL,
			Models:   []string{"models/gemini-4-hydrogen"},
			Contexts: map[string]int{"models/gemini-4-hydrogen": 1048576},
		}); err != nil {
			t.Fatal(err)
		}
		h := New().Handler()
		body := `{"model":"goog/models/gemini-4-hydrogen","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		// a stream that began (the reasoning was held and told) ends with
		// the failure said in it: the overflow's words and code, not the
		// empty reply's "try again", and no ask again of any account
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (the stream's failure said in it); body = %s", rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "prompt is too long") || strings.Contains(rec.Body.String(), "try again") {
			t.Fatalf("body = %s, want the overflow said, not the empty reply's ask again", rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "context_length_exceeded") {
			t.Fatalf("body = %s, want the code an agent compacts on", rec.Body)
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("upstream called %d times, want 1 (no empty-reply retry on overflow)", got)
		}
	})

	t.Run("translated_streaming_sub_window_still_retries", func(t *testing.T) {
		fresh(t)
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			n := calls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			if n == 1 {
				_, _ = io.WriteString(w, geminiEmpty(338316, 300000))
				return
			}
			_, _ = io.WriteString(w, geminiOK("recovered"))
		}))
		t.Cleanup(srv.Close)
		if err := provider.Save(provider.Provider{
			ID:       "goog",
			Name:     "Google",
			Key:      "k",
			Gemini:   srv.URL,
			Models:   []string{"models/gemini-4-hydrogen"},
			Contexts: map[string]int{"models/gemini-4-hydrogen": 1048576},
		}); err != nil {
			t.Fatal(err)
		}
		h := New().Handler()
		body := `{"model":"goog/models/gemini-4-hydrogen","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "recovered") {
			t.Fatalf("body = %s, want recovered", rec.Body)
		}
		if got := calls.Load(); got != 2 {
			t.Fatalf("upstream called %d times, want 2", got)
		}
	})

	t.Run("group_failover_and_behind_overflow", func(t *testing.T) {
		fresh(t)
		var gCalls, dsCalls atomic.Int32
		gSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			gCalls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, geminiEmpty(1549512, 1544443))
		}))
		t.Cleanup(gSrv.Close)
		dsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			dsCalls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, sse(
				`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_ds","type":"message","role":"assistant","model":"deepseek-v4.1-flash","content":[],"usage":{"input_tokens":10}}}`,
				`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from-deepseek"}}`,
				`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
				`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
				`event: message_stop`+"\n"+`data: {"type":"message_stop"}`))
		}))
		t.Cleanup(dsSrv.Close)
		if err := provider.Save(provider.Provider{
			ID:       "goog",
			Name:     "Google",
			Key:      "k1",
			Keys:     []provider.KeyAccount{{Name: "k1", Key: "k1"}, {Name: "k2", Key: "k2"}},
			Gemini:   gSrv.URL,
			Models:   []string{"models/gemini-4-hydrogen"},
			Contexts: map[string]int{"models/gemini-4-hydrogen": 1048576},
		}); err != nil {
			t.Fatal(err)
		}
		if err := provider.Save(provider.Provider{
			ID:        "ds",
			Name:      "DeepSeek",
			Key:       "k",
			Anthropic: dsSrv.URL,
			Models:    []string{"deepseek-v4.1-flash"},
			Contexts:  map[string]int{"deepseek-v4.1-flash": 1048576},
		}); err != nil {
			t.Fatal(err)
		}
		if err := provider.SaveGroup(provider.Group{
			ID:      "mix",
			Name:    "mix",
			Members: []string{"goog/models/gemini-4-hydrogen", "ds/deepseek-v4.1-flash"},
			Routing: provider.Ordered,
		}); err != nil {
			t.Fatal(err)
		}
		h := New().Handler()
		body := `{"model":"group/mix","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"long conversation"}]}`

		// Turn 1: Google key 1 overflows on empty reply -> skips key 2, fails over to DeepSeek.
		req1 := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req1.Header.Set("X-Session-Id", "sess-7601")
		rec1 := httptest.NewRecorder()
		h.ServeHTTP(rec1, req1)
		if rec1.Code != http.StatusOK || !strings.Contains(rec1.Body.String(), "from-deepseek") {
			t.Fatalf("turn 1: status %d body %s", rec1.Code, rec1.Body)
		}
		if got := gCalls.Load(); got != 1 {
			t.Fatalf("turn 1: google called %d times, want 1 (key 2 skipped via withRoom)", got)
		}
		if got := dsCalls.Load(); got != 1 {
			t.Fatalf("turn 1: deepseek called %d times, want 1", got)
		}

		// Turn 2: behindOverflow puts Google behind DeepSeek for the same conversation.
		req2 := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req2.Header.Set("X-Session-Id", "sess-7601")
		rec2 := httptest.NewRecorder()
		h.ServeHTTP(rec2, req2)
		if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "from-deepseek") {
			t.Fatalf("turn 2: status %d body %s", rec2.Code, rec2.Body)
		}
		if got := gCalls.Load(); got != 1 {
			t.Fatalf("turn 2: google called %d times total, want 1 (behindOverflow should go straight to deepseek)", got)
		}
		if got := dsCalls.Load(); got != 2 {
			t.Fatalf("turn 2: deepseek called %d times, want 2", got)
		}
	})

	t.Run("translated_non_streaming_over_window", func(t *testing.T) {
		fresh(t)
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			calls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, geminiEmpty(1549512, 1544443))
		}))
		t.Cleanup(srv.Close)
		if err := provider.Save(provider.Provider{
			ID:       "goog",
			Name:     "Google",
			Key:      "k",
			Gemini:   srv.URL,
			Models:   []string{"models/gemini-4-hydrogen"},
			Contexts: map[string]int{"models/gemini-4-hydrogen": 1048576},
		}); err != nil {
			t.Fatal(err)
		}
		h := New().Handler()
		body := `{"model":"goog/models/gemini-4-hydrogen","max_tokens":64,"stream":false,"messages":[{"role":"user","content":"hi"}]}`
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body)
		}
		if !strings.Contains(strings.ToLower(rec.Body.String()), "prompt is too long") {
			t.Fatalf("body = %s, want prompt is too long", rec.Body)
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("upstream called %d times, want 1", got)
		}
	})

	t.Run("relayed_chat_sse_over_window", func(t *testing.T) {
		fresh(t)
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			calls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			// Copilot-style Chat SSE empty reply: usage only (1.28M > 983K window), no content or finish_reason.
			_, _ = io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":1288460,"completion_tokens":32,"completion_tokens_details":{"reasoning_tokens":24}}}`+"\n\ndata: [DONE]\n\n")
		}))
		t.Cleanup(srv.Close)
		if err := provider.Save(provider.Provider{
			ID:       "cop",
			Name:     "Copilot",
			Key:      "k",
			Chat:     srv.URL,
			Models:   []string{"gemini-4-hydrogen"},
			Contexts: map[string]int{"gemini-4-hydrogen": 983040},
		}); err != nil {
			t.Fatal(err)
		}
		h := New().Handler()
		body := `{"model":"cop/gemini-4-hydrogen","stream":true,"messages":[{"role":"user","content":"hi"}]}`
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		// a relayed stream fails as an error event of it, held back till
		// now: the overflow's words, not the empty reply's "try again",
		// and no ask again of any account
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (the stream's failure said in it); body = %s", rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "prompt is too long") || strings.Contains(rec.Body.String(), "try again") {
			t.Fatalf("body = %s, want the overflow said, not the empty reply's ask again", rec.Body)
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("upstream called %d times, want 1", got)
		}
	})
}
