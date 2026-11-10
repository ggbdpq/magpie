package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #5833: a handover after the thinking began leaves the parked block with
// neither thinking nor signature (the try's stream failed mid-block, the
// next candidate answered in the same response), and the client sends that
// block back on every later turn. A relayed Messages request passes it to
// the vendor as the client keeps it, and Anthropic-type endpoints turn the
// whole request away for it — "each thinking block must contain thinking",
// wherever the block sits in the history, until the conversation is
// compacted. A block with nothing in it carries nothing for any vendor:
// the relay leaves it out, as anthropicBody does on the translated path.
func TestHandoverEmptyThinkingIsNotRelayed(t *testing.T) {
	f := &fake{reply: `{"id":"msg_ok","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":1}}`, ctype: "application/json"}
	setup(t, provider.Anthropic, f)
	ask := `{"model":"fake/m1","max_tokens":64,"stream":false,"messages":[` +
		`{"role":"user","content":"Hi there"},` +
		`{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"a plan","signature":"sig1"},` +
		`{"type":"thinking","thinking":"","signature":""},` +
		`{"type":"thinking","thinking":"a note the vendor never signed"},` +
		`{"type":"redacted_thinking","data":""},` +
		`{"type":"text","text":"Hello!"}]},` +
		`{"role":"user","content":"Reply with just: ok"}]}`
	code, body := post(t, "/v1/messages", ask)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatalf("the upstream's request isn't the JSON that went in: %v", err)
	}
	var assistant []map[string]any
	for _, m := range up.Messages {
		if m.Role != "assistant" {
			continue
		}
		if err := json.Unmarshal(m.Content, &assistant); err != nil {
			t.Fatalf("the assistant turn's content isn't a block list: %v", err)
		}
	}
	if len(assistant) == 0 {
		t.Fatalf("the assistant turn didn't reach the upstream: %s", f.got)
	}
	for _, b := range assistant {
		typ, _ := b["type"].(string)
		switch typ {
		case "thinking":
			th, _ := b["thinking"].(string)
			sig, _ := b["signature"].(string)
			if th == "" && sig == "" {
				t.Fatalf("the handover's empty unsigned thinking block reached the upstream: %v\n%s", b, f.got)
			}
		case "redacted_thinking":
			data, _ := b["data"].(string)
			if data == "" {
				t.Fatalf("an empty redacted_thinking reached the upstream: %v\n%s", b, f.got)
			}
		}
	}
	// the blocks that carry something stay, as the client keeps them
	if !strings.Contains(string(f.got), `"a plan"`) || !strings.Contains(string(f.got), `"sig1"`) {
		t.Errorf("the signed thinking block was dropped:\n%s", f.got)
	}
	if !strings.Contains(string(f.got), `"a note the vendor never signed"`) {
		t.Errorf("an unsigned thinking block with text was dropped:\n%s", f.got)
	}
	if !strings.Contains(string(f.got), "Hello!") {
		t.Errorf("the text block was dropped:\n%s", f.got)
	}
}
