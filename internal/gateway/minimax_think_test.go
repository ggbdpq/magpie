package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// miniMaxStream is a reply as MiniMax's Chat API streams it (its
// OpenAI-compatible docs for MiniMax-M2): the thinking at the head of the
// text between <think> tags, the tags split across chunks, then the answer.
const miniMaxStream = `data: {"id":"0581e2a0","choices":[{"index":0,"delta":{"content":"<th","role":"assistant","name":"MiniMax AI","audio_content":""}}],"created":1759900000,"model":"MiniMax-M2.7","object":"chat.completion.chunk"}

data: {"id":"0581e2a0","choices":[{"index":0,"delta":{"content":"ink>\nThe user greets me","role":"assistant","name":"MiniMax AI","audio_content":""}}],"created":1759900000,"model":"MiniMax-M2.7","object":"chat.completion.chunk"}

data: {"id":"0581e2a0","choices":[{"index":0,"delta":{"content":". Reply briefly.\n</thi","role":"assistant","name":"MiniMax AI","audio_content":""}}],"created":1759900000,"model":"MiniMax-M2.7","object":"chat.completion.chunk"}

data: {"id":"0581e2a0","choices":[{"index":0,"delta":{"content":"nk>\n\nHello!","role":"assistant","name":"MiniMax AI","audio_content":""}}],"created":1759900000,"model":"MiniMax-M2.7","object":"chat.completion.chunk"}

data: {"id":"0581e2a0","choices":[{"finish_reason":"stop","index":0,"delta":{"content":"","role":"assistant","name":"MiniMax AI","audio_content":""}}],"created":1759900000,"model":"MiniMax-M2.7","object":"chat.completion.chunk","usage":{"total_tokens":40,"prompt_tokens":20,"completion_tokens":20}}

data: [DONE]

`

func miniMaxSetup(t *testing.T, f *fake) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "minimax", Name: "MiniMax", Key: "k", Models: []string{"MiniMax-M2.7"}, Preset: "minimax", Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

// Codex asks MiniMax on its Chat API, which gives the thinking in the text
// between <think> tags. Codex folds it only as a reasoning item, so the
// tags' content goes to Codex as reasoning and the answer alone as the
// message (pluo, #1267: the thinking read as the answer's text).
func TestMiniMaxThinkTagsReachCodexAsReasoning(t *testing.T) {
	f := &fake{t: t, reply: miniMaxStream}
	miniMaxSetup(t, f)
	code, body := post(t, "/v1/responses", `{"model":"minimax/MiniMax-M2.7","stream":true,"input":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("code %d: %s", code, body)
	}
	var reasoning, text string
	var order []string
	for _, ev := range events(body) {
		if ev["type"] != "response.output_item.done" {
			continue
		}
		item, _ := ev["item"].(map[string]any)
		order = append(order, item["type"].(string))
		b, _ := json.Marshal(item)
		switch item["type"] {
		case "reasoning":
			reasoning += string(b)
		case "message":
			text += string(b)
		}
	}
	if !strings.Contains(reasoning, "The user greets me. Reply briefly.") {
		t.Errorf("no reasoning item with the thinking; items %v:\n%s", order, body)
	}
	if !strings.Contains(text, `"Hello!"`) || strings.Contains(text, "think") || strings.Contains(text, "greets") {
		t.Errorf("message %s, want the answer alone", text)
	}
	if len(order) != 2 || order[0] != "reasoning" || order[1] != "message" {
		t.Errorf("items %v, want reasoning then message", order)
	}
}

// The thinking goes back to MiniMax where it came from, in the assistant's
// text between <think> tags, as MiniMax asks for its interleaved thinking;
// a relay serving MiniMax's models, or another model on Chat, is sent no
// thinking, as before.
func TestMiniMaxThinkingGoesBackInTags(t *testing.T) {
	r := &Request{Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}},
		{Role: "assistant", Parts: []Part{{Kind: Thinking, Text: "\nThe user greets me.\n"}, {Kind: Text, Text: "Hello!"}}},
		{Role: "user", Parts: []Part{{Kind: Text, Text: "again"}}},
	}}
	for _, c := range []struct{ model, host, want string }{
		{"MiniMax-M2.7", "api.minimax.io", "<think>\nThe user greets me.\n</think>\n\nHello!"},
		{"MiniMax-M2.7", "api.minimaxi.com", "<think>\nThe user greets me.\n</think>\n\nHello!"},
		{"MiniMax-M2.7", "api.minimax.cn", "<think>\nThe user greets me.\n</think>\n\nHello!"},
		{"minimax/minimax-m3", "openrouter.ai", "Hello!"},
		{"kimi-k3", "api.moonshot.cn", "Hello!"},
	} {
		var q struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.Unmarshal(buildChat(r, c.model, c.host, false), &q); err != nil {
			t.Fatal(err)
		}
		if got := q.Messages[1]["content"]; got != c.want {
			t.Errorf("%s at %s: assistant content %q, want %q", c.model, c.host, got, c.want)
		}
	}
}

// A Chat client asking MiniMax on its Chat API (the preset's own form) has
// the leading <think> block taken out as reasoning_content and the answer
// alone in content, streamed or whole (#5805): the reply is translated, not
// relayed as it came, which handed the tags to the client as the answer's
// text, with no reasoning field to fold it by.
func TestMiniMaxChatClientGetsReasoningField(t *testing.T) {
	for _, c := range []struct{ name, body string }{
		{"streamed", `{"model":"minimax/MiniMax-M2.7","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"whole", `{"model":"minimax/MiniMax-M2.7","messages":[{"role":"user","content":"hi"}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{t: t, reply: miniMaxStream}
			miniMaxSetup(t, f)
			code, body := post(t, "/v1/chat/completions", c.body)
			if code != 200 {
				t.Fatalf("code %d: %s", code, body)
			}
			var reasoning, text string
			if c.name == "streamed" {
				for _, ev := range events(body) {
					chs, _ := ev["choices"].([]any)
					for _, raw := range chs {
						ch, _ := raw.(map[string]any)
						d, _ := ch["delta"].(map[string]any)
						if d == nil {
							continue
						}
						if v, _ := d["reasoning_content"].(string); v != "" {
							reasoning += v
						}
						if v, _ := d["content"].(string); v != "" {
							text += v
						}
					}
				}
			} else {
				var out struct {
					Choices []struct {
						Message struct {
							Content          string `json:"content"`
							ReasoningContent string `json:"reasoning_content"`
						} `json:"message"`
					} `json:"choices"`
				}
				if err := json.Unmarshal([]byte(body), &out); err != nil || len(out.Choices) != 1 {
					t.Fatalf("reply %s (%v)", body, err)
				}
				reasoning = out.Choices[0].Message.ReasoningContent
				text = out.Choices[0].Message.Content
			}
			if !strings.Contains(reasoning, "The user greets me. Reply briefly.") {
				t.Errorf("reasoning %q, want the thinking", reasoning)
			}
			if text != "Hello!" {
				t.Errorf("content %q, want the answer alone", text)
			}
		})
	}
}

// MiniMax's Chat API thinks in its text by preset or by host, its China
// host and preset beside the international ones; another vendor's does not.
func TestMiniMaxInlineThinkChat(t *testing.T) {
	for _, c := range []struct {
		p    provider.Provider
		want bool
	}{
		{provider.Provider{Preset: "minimax"}, true},
		{provider.Provider{Preset: "minimax-cn"}, true},
		{provider.Provider{Chat: "https://api.minimax.io/v1"}, true},
		{provider.Provider{Chat: "https://api.minimaxi.com/v1"}, true},
		{provider.Provider{Chat: "https://api.minimax.cn/v1"}, true},
		{provider.Provider{}, false},
		{provider.Provider{Preset: "qwen"}, false},
		{provider.Provider{Chat: "https://api.deepseek.com/v1"}, false},
		{provider.Provider{Chat: "https://api.minimaxi.com.example.net/v1"}, false},
	} {
		if got := inlineThinkChat(c.p); got != c.want {
			t.Errorf("inlineThinkChat(%+v) %t, want %t", c.p, got, c.want)
		}
	}
}

// A <think> block at the head of a Chat reply is thinking however the tags
// fall across chunks; one later in the text, or a tag that only starts
// like it, is the answer as it came.
func TestChatThinkTags(t *testing.T) {
	run := func(parts ...string) (think, text string) {
		var d chatDecoder
		for i, p := range parts {
			ch := map[string]any{"index": 0, "delta": map[string]any{"content": p}}
			if i == len(parts)-1 {
				ch["finish_reason"] = "stop"
			}
			b, _ := json.Marshal(map[string]any{"id": "x", "choices": []any{ch}})
			d.decode(string(b), func(ev Event) {
				switch ev.Kind {
				case KThink:
					think += ev.Text
				case KText:
					text += ev.Text
				}
			})
		}
		return
	}
	for _, c := range []struct {
		name        string
		parts       []string
		think, text string
	}{
		{"whole", []string{"<think>Greeting.</think>\n\nHello"}, "Greeting.", "Hello"},
		{"one byte a chunk", strings.Split("<think>ab</think>cd", ""), "ab", "cd"},
		{"a thought's close doesn't end it", []string{"<think>a</thought>b</think>c"}, "a</thought>b", "c"},
		{"a think tag later", []string{"Hi <think>no</think>"}, "", "Hi <think>no</think>"},
		{"<thin is text", []string{"<thin", "g>"}, "", "<thing>"},
		{"after a Windows line end", []string{"\r", "\n<think>a</think>\r\n", "b"}, "a", "b"},
		{"after a tab", []string{"\t<think>a</think>\n\nb"}, "a", "b"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if think, text := run(c.parts...); think != c.think || text != c.text {
				t.Errorf("think %q text %q, want %q %q", think, text, c.think, c.text)
			}
		})
	}
}
