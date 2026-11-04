package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// glmRefuseThinkingBack plays a GLM-family relay's Anthropic endpoint
// (tokenrhythm.studio, #4165): thinking on, an assistant tool turn with
// no thinking block turns the whole request away, word for word as the
// reporter's 400 said. Unlike deepseekRefuseAnthropic it takes neither a
// redacted_thinking nor a bare tool_use, and a plain reply is taken.
func glmRefuseThinkingBack(body []byte) (int, string) {
	var q struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal(body, &q)
	afterTool := false
	for _, m := range q.Messages {
		var blocks []struct {
			Type string `json:"type"`
		}
		json.Unmarshal(m.Content, &blocks)
		after := afterTool
		afterTool = m.Role == "user" && len(blocks) > 0
		thought, called := false, false
		for _, b := range blocks {
			afterTool = afterTool && b.Type == "tool_result"
			switch b.Type {
			case "thinking":
				thought = true
			case "tool_use":
				called = true
			}
		}
		if m.Role == "assistant" && (called || after) && !thought {
			return 400, `{"error":{"message":"基元: The ` + "`content[].thinking`" + ` in the thinking mode must be passed back to the API. (request_id: 3f0b7c0c-66a3-4a4e-8f1d-561ae82608e2)","type":"invalid_request_error"}}`
		}
	}
	return 0, ""
}

// glmToolTurnNoThinking is Claude Code's continuation of a tool round it
// has no thinking for: the turn another model made (a routing group's
// fall, a retry after a broken stream, a compact), Anthropic's protocol
// allowing the history back with no thinking block. The model is named
// as the client named it, which says neither DeepSeek nor a vendor's
// anything: the wire, if any, names it upstream alone.
const glmToolTurnNoThinking = `{"model":"%s","max_tokens":4096,"stream":true,"thinking":{"type":"adaptive"},
	"tools":[{"name":"Read","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}}}],
	"messages":[
	{"role":"user","content":[{"type":"text","text":"read a.go"}]},
	{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a.go"}}]},
	{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"package a"}]}]}`

// The reporter's own direction (#4165): an Anthropic client's tool round
// relayed as it came (passthrough) to a GLM-family relay which asks the
// assistant tool turn's thinking back and turns the whole request away
// without it. A model whose name says DeepSeek is filled at once
// (anthropicReplaysThinking, #1611); one whose name and host say nothing
// is learned from the refusal (thinking_back.go, qisishiqi), asked once
// more with a space for it, and from then on sent it at once; a provider
// that never asked is sent none.
func TestThinkingBackLearnedOnPassthrough(t *testing.T) {
	for _, c := range []struct {
		model string
		calls int // the first request's: filled at once, or learned on the second
	}{
		{"deepseek-v4.1-flash-1017", 1},
		{"claude-sonnet-4-6", 2},
		{"glm-5.3-flash", 2},
	} {
		t.Run(c.model, func(t *testing.T) {
			f := &fake{t: t, reply: thinkingBackStream, refuse: glmRefuseThinkingBack}
			up := setup(t, provider.Anthropic, f)
			p := provider.Provider{ID: "glmrelay", Name: "GLM Relay", Models: []string{c.model}}
			p.Name, p.Key, p.Anthropic = p.ID, "k", up.URL
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			srv := New()
			code, body := postTo(t, srv, "/v1/messages", strings.Replace(glmToolTurnNoThinking, "%s", "glmrelay/"+c.model, 1))
			if code != 200 || f.calls != c.calls {
				t.Fatalf("%s: %d after %d calls, want 200 on the %dth: %s", c.model, code, f.calls, c.calls, body)
			}
			turn := gjson.GetBytes(f.got, `messages.#(role=="assistant")`)
			if th := turn.Get(`content.#(type=="thinking")`); th.Get("thinking").String() != noReasoning || turn.Get("content.0.type").String() != "thinking" {
				t.Errorf("%s: the tool turn went as %s, want a thinking block first", c.model, turn.Raw)
			}

			// learned: the next request is taken the first time
			f.calls = 0
			if code, body = postTo(t, srv, "/v1/messages", strings.Replace(glmToolTurnNoThinking, "%s", "glmrelay/"+c.model, 1)); code != 200 || f.calls != 1 {
				t.Fatalf("%s: %d after %d calls, want 200 on the first: %s", c.model, code, f.calls, body)
			}
		})
	}
}
