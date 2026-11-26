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

// mistralThinkingStream is GLM on api.mistral.ai as it streams (#483):
// after the role chunk, content is typed parts, the thinking a list of
// text parts, and the last of the thinking and the answer in one delta.
func mistralThinkingStream() string {
	chunk := func(delta, finish string) string {
		return `data: {"id":"31c3","object":"chat.completion.chunk","created":1790887322,"model":"zai-glm-5-3","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + finish + `,"logprobs":null}]}`
	}
	return sse(
		chunk(`{"role":"assistant","content":""}`, `null`),
		chunk(`{"index":0,"content":[{"type":"thinking","thinking":[{"type":"text","text":"The user has"}],"closed":true}]}`, `null`),
		chunk(`{"index":0,"content":[{"type":"thinking","thinking":[{"type":"text","text":" asked for ok."}],"closed":true},{"type":"text","text":"o"}]}`, `null`),
		chunk(`{"index":0,"content":[{"type":"text","text":"k"}]}`, `null`),
		chunk(`{"content":""}`, `"stop"`),
		`data: [DONE]`)
}

// A Chat client gets Mistral's thinking in reasoning_content and its text
// as a string content, on every chunk: OpenCode's AI SDK turned the first
// array away ("Invalid … stream event") and the request was given up.
func TestMistralPartsStreamFlattened(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "text/event-stream", mistralThinkingStream()}}}
	scriptedOn(t, "a", provider.Chat, a)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"a/m","stream":true,"messages":[{"role":"user","content":"Reply with just: ok"}]}`)))
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, body)
	}
	var think, text string
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var ch struct {
			Choices []struct {
				Delta map[string]json.RawMessage `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			t.Fatalf("%v: %s", err, data)
		}
		for _, c := range ch.Choices {
			if raw, ok := c.Delta["content"]; ok {
				var s *string
				if err := json.Unmarshal(raw, &s); err != nil {
					t.Fatalf("content is not a string: %s", data)
				}
				if s != nil {
					text += *s
				}
			}
			var r string
			json.Unmarshal(c.Delta["reasoning_content"], &r)
			think += r
		}
	}
	if think != "The user has asked for ok." || text != "ok" {
		t.Fatalf("think %q text %q\n%s", think, text, body)
	}
	if !strings.Contains(body, `"reasoning_content":" asked for ok."`) || !strings.Contains(body, `"content":"o"`) ||
		!strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Fatal(body)
	}
}

// A reply that isn't streamed has the same shape in message.content.
func TestMistralPartsWholeFlattened(t *testing.T) {
	fresh(t)
	up := `{"id":"31c3","object":"chat.completion","model":"zai-glm-5-3","choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"The user has asked for ok."}],"closed":true},{"type":"text","text":"ok"}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":7,"total_tokens":16}}`
	a := &scripted{replies: []reply{{200, "application/json", up}}}
	scriptedOn(t, "a", provider.Chat, a)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"a/m","messages":[{"role":"user","content":"Reply with just: ok"}]}`)))
	var got struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || len(got.Choices) != 1 ||
		got.Choices[0].Message.Content != "ok" || got.Choices[0].Message.ReasoningContent != "The user has asked for ok." ||
		got.Choices[0].FinishReason != "stop" || got.Usage.TotalTokens != 16 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// Replies of other upstreams pass byte for byte, streamed or not.
func TestChatRelayWithoutPartsUntouched(t *testing.T) {
	line := `data: {"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}` + "\n"
	if got := string(tidyLine([]byte(line))); got != line {
		t.Fatalf("%s", got)
	}
	up := `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"} ,"finish_reason":"stop"}]}`
	fresh(t)
	a := &scripted{replies: []reply{{200, "application/json", up}}}
	scriptedOn(t, "a", provider.Chat, a)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != 200 || rec.Body.String() != up {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// Spoken to Claude Code's API or to Codex's, the same stream is thinking
// and then the text: a chunk of parts was not read at all, so both were
// lost.
func TestMistralPartsTranslated(t *testing.T) {
	for _, c := range []struct{ path, body, think string }{
		{"/v1/messages", `{"model":"a/m","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			`"type":"thinking_delta"`},
		{"/v1/responses", `{"model":"a/m","stream":true,"input":"hi"}`,
			`"type":"reasoning"`},
	} {
		fresh(t)
		a := &scripted{replies: []reply{{200, "text/event-stream", mistralThinkingStream()}}}
		scriptedOn(t, "a", provider.Chat, a)
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", c.path, strings.NewReader(c.body)))
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, c.think) || !strings.Contains(body, "The user has") ||
			!strings.Contains(body, " asked for ok.") || !strings.Contains(body, `"o"`) || !strings.Contains(body, `"k"`) {
			t.Fatalf("%s: %d %s", c.path, rec.Code, body)
		}
		if i, j := strings.Index(body, "The user has"), strings.Index(body, `"o"`); i > j {
			t.Fatalf("%s: text before thinking: %s", c.path, body)
		}
	}
}

// #7639: CodeBuddy injects history turns carrying a message-level skipRun
// field and untyped assistant content parts ({"thinking":[{"text":...}]}).
// Mistral answers both with 422: extra_forbidden on skipRun and
// union_tag_not_found on the untyped parts. A message-level extra_forbidden
// field is now dropped, retried, and remembered per provider; untyped
// assistant parts sent to Mistral get the type tags Mistral requires.
func TestMistralCodeBuddySkipRunAndUntypedParts(t *testing.T) {
	// Guard: refusedInMessages never drops core fields or nested content fields.
	if got := refusedInMessages(422, []byte(`{"detail":[{"type":"extra_forbidden","loc":["body","messages",0,"user","content"]}]}`), []byte(`{"messages":[{"role":"user","content":"hi"}]}`)); len(got) != 0 {
		t.Fatalf("refusedInMessages matched core field: %v", got)
	}
	if got := refusedInMessages(422, []byte(`{"detail":[{"type":"extra_forbidden","loc":["body","messages",0,"assistant","content",0,"foo"]}]}`), []byte(`{"messages":[{"role":"assistant","content":[{"foo":"bar"}]}]}`)); len(got) != 0 {
		t.Fatalf("refusedInMessages matched nested content field: %v", got)
	}
	// Already-typed Mistral parts pass byte for byte.
	alreadyTyped := `{"model":"m","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"ok"}]},{"type":"text","text":"hi"}]}]}`
	if got := string(mistralThinking([]byte(alreadyTyped))); got != alreadyTyped {
		t.Fatalf("already-typed parts altered: %s", got)
	}

	fresh(t)
	var mu sync.Mutex
	var sent []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var q struct {
			Messages []map[string]json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(b, &q); err != nil {
			w.WriteHeader(400)
			return
		}
		var detail []map[string]any
		for i, m := range q.Messages {
			var role string
			json.Unmarshal(m["role"], &role)
			if _, ok := m["skipRun"]; ok {
				detail = append(detail, map[string]any{
					"type":  "extra_forbidden",
					"loc":   []any{"body", "messages", i, role, "skipRun"},
					"msg":   "Extra inputs are not permitted",
					"input": true,
				})
			}
			if role == "assistant" {
				var parts []map[string]json.RawMessage
				if json.Unmarshal(m["content"], &parts) == nil {
					for j, p := range parts {
						var typ string
						json.Unmarshal(p["type"], &typ)
						if typ == "" {
							detail = append(detail, map[string]any{
								"type": "union_tag_not_found",
								"loc":  []any{"body", "messages", i, "assistant", "content", "list[tagged-union]", j},
								"msg":  "Unable to extract tag using discriminator 'type'",
							})
						}
						if typ == "thinking" {
							var inner []map[string]json.RawMessage
							if json.Unmarshal(p["thinking"], &inner) != nil {
								detail = append(detail, map[string]any{"type": "list_type", "loc": []any{"body", "messages", i, "assistant", "content", j, "thinking"}})
							} else {
								for k, in := range inner {
									var it string
									json.Unmarshal(in["type"], &it)
									if it == "" {
										detail = append(detail, map[string]any{
											"type": "union_tag_not_found",
											"loc":  []any{"body", "messages", i, "assistant", "content", j, "thinking", k},
										})
									}
								}
							}
						}
					}
				}
			}
		}
		if len(detail) > 0 {
			w.WriteHeader(422)
			json.NewEncoder(w).Encode(map[string]any{"detail": detail})
			return
		}
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"deepseek-v4-1-flash-1017","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{
		ID:     "mistral",
		Name:   "Mistral",
		Preset: "mistral",
		Key:    "k",
		Chat:   up.URL + "/v1",
		Models: []string{"deepseek-v4-1-flash-1017"},
	}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()

	// Combined CodeBuddy turn: skipRun on user + untyped thinking & text parts on assistant.
	combined := `{"model":"mistral/deepseek-v4-1-flash-1017","max_tokens":8,"messages":[` +
		`{"role":"user","content":"hi","skipRun":true},` +
		`{"role":"assistant","content":[{"thinking":[{"text":"the user greets"}]},{"text":"hello"}]},` +
		`{"role":"user","content":"next"}]}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(combined)))
	mu.Lock()
	got := append([]string(nil), sent...)
	mu.Unlock()
	if rec.Code != 200 {
		t.Fatalf("combined turn status %d (calls %d): %s\nlast sent: %s", rec.Code, len(got), rec.Body.String(), got[len(got)-1])
	}
	if len(got) != 2 {
		t.Fatalf("combined turn made %d upstream calls, want 2 (initial 422 on skipRun + retry without it)", len(got))
	}
	if strings.Contains(got[1], `"skipRun"`) {
		t.Errorf("retry still sent skipRun: %s", got[1])
	}
	var parsed struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(got[1]), &parsed); err != nil || len(parsed.Messages) != 3 {
		t.Fatalf("parse retry body: %v (%s)", err, got[1])
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"thinking"`
	}
	if err := json.Unmarshal(parsed.Messages[1].Content, &parts); err != nil || len(parts) != 2 ||
		parts[0].Type != "thinking" || len(parts[0].Thinking) != 1 || parts[0].Thinking[0].Type != "text" || parts[0].Thinking[0].Text != "the user greets" ||
		parts[1].Type != "text" || parts[1].Text != "hello" {
		t.Errorf("untyped parts were not tagged for Mistral: %s", got[1])
	}

	// Subsequent turn on same server: skipRun is remembered and stripped up front (1 call).
	before := len(got)
	next := `{"model":"mistral/deepseek-v4-1-flash-1017","max_tokens":8,"messages":[` +
		`{"role":"user","content":"hi","skipRun":true},` +
		`{"role":"assistant","content":[{"thinking":[{"text":"thinking only"}]}]},` +
		`{"role":"user","content":"again"}]}`
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(next)))
	if rec.Code != 200 {
		t.Fatalf("second turn status %d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	got = append([]string(nil), sent...)
	mu.Unlock()
	if len(got)-before != 1 {
		t.Fatalf("second turn made %d calls, want 1 (skipRun remembered unfit)", len(got)-before)
	}
	if strings.Contains(got[len(got)-1], `"skipRun"`) {
		t.Errorf("second turn still sent skipRun: %s", got[len(got)-1])
	}
}
