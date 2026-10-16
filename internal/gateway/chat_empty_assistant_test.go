package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// dsRefuseEmptyAssistant answers as DeepSeek's and Kimi's Chat APIs do for
// a turn the agent cut off mid-reply (#2599): an assistant message with
// neither content nor tool_calls is invalid however much reasoning_content
// it carries — reasoning is not content — and the whole request is turned
// away, every later turn of the conversation with it.
func dsRefuseEmptyAssistant(body []byte) (int, string) {
	var q struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	json.Unmarshal(body, &q)
	for _, m := range q.Messages {
		if string(m["role"]) != `"assistant"` {
			continue
		}
		if _, ok := m["tool_calls"]; ok {
			continue
		}
		empty := false
		var s string
		if decodeJSON(m["content"], &s) == nil {
			empty = strings.TrimSpace(s) == ""
		} else if m["content"] == nil || string(m["content"]) == "null" {
			empty = true
		}
		if empty {
			return 400, `{"error":{"message":"Invalid assistant message: content or tool_calls must be set","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`
		}
	}
	return 0, ""
}

const emptyOKStream = `data: {"id":"c1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","content":"Yes."},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}` + "\n\ndata: [DONE]\n\n"

// A reply the agent cut off before it said anything — only its thinking,
// only an empty text, nothing at all — stays in the agent's history and is
// sent again with every later turn. Translated to a Chat upstream
// (Claude Code here) it went out with "content": "" and DeepSeek, Kimi and
// the like turned the whole request away. The turn is left out and the two
// user messages around it join into one, as toolAsUser's do (#2599).
func TestChatEmptyAssistantLeftOutFromAnthropic(t *testing.T) {
	f := &fake{t: t, reply: emptyOKStream, refuse: dsRefuseEmptyAssistant}
	up := setup(t, provider.Chat, f)
	chatProvider(t, up.URL, provider.Provider{ID: "ds", Models: []string{"deepseek-v4.1-flash"}})
	for name, turn := range map[string]string{
		"thinking":        `[{"type":"thinking","thinking":"mid-thought","signature":"sig"}]`,
		"empty text":      `[{"type":"text","text":""}]`,
		"thinking + text": `[{"type":"thinking","thinking":"mid-thought","signature":"sig"},{"type":"text","text":""}]`,
		"no blocks":       `[]`,
		"sealed only":     `[{"type":"redacted_thinking","data":"gAAAAABpZm9vYmFy"}]`,
		"empty string":    `""`,
	} {
		f.calls = 0
		code, body := postTo(t, New(), "/v1/messages", `{"model":"ds/deepseek-v4.1-flash","max_tokens":32,"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":`+turn+`},
			{"role":"user","content":"continue"}]}`)
		if code != 200 || f.calls != 1 {
			t.Errorf("%s: %d after %d calls, sent %s: %s", name, code, f.calls, f.got, body)
			continue
		}
		ms := sentMessages(t, f.got)
		if len(ms) != 1 {
			t.Errorf("%s: %d messages sent: %s", name, len(ms), f.got)
			continue
		}
		sameJSON(t, ms[0], `{"role":"user","content":"hi\n\ncontinue"}`)
	}
}

// The same from Codex: an isolated reasoning item of a cut-off reply
// becomes an assistant message with nothing on it, which went out the same
// way.
func TestChatEmptyAssistantLeftOutFromResponses(t *testing.T) {
	f := &fake{t: t, reply: emptyOKStream, refuse: dsRefuseEmptyAssistant}
	up := setup(t, provider.Chat, f)
	chatProvider(t, up.URL, provider.Provider{ID: "ds", Models: []string{"deepseek-v4.1-flash"}})
	code, body := postTo(t, New(), "/v1/responses", `{"model":"ds/deepseek-v4.1-flash","stream":false,"input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
		{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"mid-thought"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`)
	if code != 200 || f.calls != 1 {
		t.Fatalf("%d after %d calls, sent %s: %s", code, f.calls, f.got, body)
	}
	ms := sentMessages(t, f.got)
	if len(ms) != 1 {
		t.Fatalf("%d messages sent: %s", len(ms), f.got)
	}
	sameJSON(t, ms[0], `{"role":"user","content":"hi\n\ncontinue"}`)
}

// A Chat client's own empty assistant (a turn it cut off, which it keeps
// replaying) is left out of what any Chat upstream is sent, relayed or
// not; a request without one is relayed as it came.
func TestChatRelayLeavesOutEmptyAssistant(t *testing.T) {
	f := &fake{t: t, reply: emptyOKStream, refuse: dsRefuseEmptyAssistant}
	up := setup(t, provider.Chat, f)
	chatProvider(t, up.URL, provider.Provider{ID: "ds", Models: []string{"deepseek-v4.1-flash", "glm-5.2"}})
	srv := New()
	for _, model := range []string{"ds/deepseek-v4.1-flash", "ds/glm-5.2"} {
		f.calls = 0
		code, body := postTo(t, srv, "/v1/chat/completions", `{"model":"`+model+`","stream":false,"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":"","reasoning_content":"halfway"},
			{"role":"user","content":"continue"}]}`)
		if code != 200 || f.calls != 1 {
			t.Fatalf("%s: %d after %d calls, sent %s: %s", model, code, f.calls, f.got, body)
		}
		ms := sentMessages(t, f.got)
		if len(ms) != 1 {
			t.Fatalf("%s: %d messages sent: %s", model, len(ms), f.got)
		}
		sameJSON(t, ms[0], `{"role":"user","content":"hi\n\ncontinue"}`)
	}

	// a clean body loses nothing: an assistant with content stands, and no
	// message comes out emptied
	f.calls = 0
	code, body := postTo(t, srv, "/v1/chat/completions", `{"model":"ds/glm-5.2","stream":false,"messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"doing it"},
		{"role":"user","content":"go"}]}`)
	if code != 200 || f.calls != 1 {
		t.Fatalf("clean: %d after %d calls: %s", code, f.calls, body)
	}
	if strings.Contains(string(f.got), `"content":""`) || !strings.Contains(string(f.got), `"content":"doing it"`) {
		t.Errorf("a clean body was rewritten: %s", f.got)
	}
}
