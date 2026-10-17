package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The DeepSeek preset at a fake endpoint: a chat client's reply length is
// asked as max_tokens, which DeepSeek's API alone takes (#2355) — one asked
// as max_completion_tokens, as OpenAI's newer name for it, goes renamed, an
// explicit max_tokens keeps the field, and one asked as both keeps
// max_tokens, the only of the two DeepSeek acts on. Another vendor's chat
// endpoint is sent max_completion_tokens as the client asked it.
func TestDeepSeekChatMaxTokens(t *testing.T) {
	fresh(t)
	f := &fake{reply: sse(
		`data: {"id":"test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
		`data: {"id":"test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	)}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	p.Key = "fixture-key"
	p.Chat = up.URL + "/v1"
	p.Models = []string{"deepseek-flash"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		body string
		want float64
	}{
		{"as max_completion_tokens",
			`{"model":"deepseek/deepseek-flash","stream":true,"max_completion_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, 8},
		{"as max_tokens",
			`{"model":"deepseek/deepseek-flash","stream":true,"max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`, 5},
		{"as both",
			`{"model":"deepseek/deepseek-flash","stream":true,"max_completion_tokens":8,"max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.got = nil
			code, reply := post(t, "/v1/chat/completions", tc.body)
			if code != 200 {
				t.Fatalf("status %d: %s", code, reply)
			}
			var sent map[string]any
			if err := json.Unmarshal(f.got, &sent); err != nil {
				t.Fatal(err)
			}
			if sent["max_tokens"] != tc.want {
				t.Fatalf("upstream asked max_tokens=%v, want %v: %s", sent["max_tokens"], tc.want, f.got)
			}
			if _, ok := sent["max_completion_tokens"]; ok {
				t.Fatalf("max_completion_tokens sent to DeepSeek: %s", f.got)
			}
		})
	}
}

// Another vendor's chat endpoint, a plain provider at a fake: the reply
// length asked as max_completion_tokens goes as it came, not renamed
// (#2355 asked for DeepSeek alone).
func TestChatMaxCompletionTokensPassedOnOtherVendors(t *testing.T) {
	fresh(t)
	f := &fake{reply: sse(
		`data: {"id":"test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
		`data: {"id":"test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	)}
	setup(t, provider.Chat, f)
	code, reply := post(t, "/v1/chat/completions",
		`{"model":"fake/m1","stream":true,"max_completion_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, reply)
	}
	var sent map[string]any
	if err := json.Unmarshal(f.got, &sent); err != nil {
		t.Fatal(err)
	}
	if sent["max_completion_tokens"] != float64(8) {
		t.Fatalf("max_completion_tokens not sent on as it came: %s", f.got)
	}
	if _, ok := sent["max_tokens"]; ok {
		t.Fatalf("renamed for another vendor: %s", f.got)
	}
}
