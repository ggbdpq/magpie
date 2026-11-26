package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// guardChatStream is an OpenAI Chat Completions SSE replacement returned with
// HTTP 200 by a relay's security guard on an Anthropic /v1/messages endpoint
// (#7525): non-empty delta text, finish_reason "stop", zero usage, and [DONE].
var guardChatStream = sse(
	`data: {"id":"chatcmpl-guard","object":"chat.completion.chunk","created":1763800000,"model":"from-security-guard","choices":[{"index":0,"delta":{"role":"assistant","content":"blocked by security guard"},"finish_reason":null}]}`,
	`data: {"id":"chatcmpl-guard","object":"chat.completion.chunk","created":1763800000,"model":"from-security-guard","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
	`data: [DONE]`,
)

// guardChatWhole is the non-streaming counterpart of guardChatStream: a 200
// Chat Completions JSON returned on /v1/messages instead of an Anthropic Message.
const guardChatWhole = `{"id":"chatcmpl-guard","object":"chat.completion","created":1763800000,"model":"from-security-guard","choices":[{"index":0,"message":{"role":"assistant","content":"blocked by security guard"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`

var wrongAnthropicStream = sse(
	`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_wrong","type":"message","role":"assistant","model":"from-security-guard","content":[],"usage":{"input_tokens":0,"output_tokens":0}}}`,
	`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"wrong protocol"}}`,
	`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
	`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":0}}`,
	`event: message_stop`+"\n"+`data: {"type":"message_stop"}`,
)

const wrongAnthropicWhole = `{"id":"msg_wrong","type":"message","role":"assistant","model":"from-security-guard","content":[{"type":"text","text":"wrong protocol"}],"stop_reason":"end_turn","usage":{"input_tokens":0,"output_tokens":0}}`

// A group member that answers HTTP 200 in another API's wire format (for
// example, a security guard returning Chat Completions JSON or SSE on an
// Anthropic /v1/messages endpoint, #7525) fails over to the next member
// without resting the provider, instead of forwarding an unparseable 200.
func TestWrongProtocolReplyFailsOver(t *testing.T) {
	cases := []struct {
		name   string
		proto  provider.Protocol
		stream bool
		bad    reply
		leak   string
	}{
		{"anthropic/stream/chat_guard", provider.Anthropic, true, reply{200, "text/event-stream", guardChatStream}, "chatcmpl-guard"},
		{"anthropic/whole/chat_guard", provider.Anthropic, false, reply{200, "application/json", guardChatWhole}, "chatcmpl-guard"},
		{"responses/stream/chat_guard", provider.Responses, true, reply{200, "text/event-stream", guardChatStream}, "chatcmpl-guard"},
		{"responses/whole/chat_guard", provider.Responses, false, reply{200, "application/json", guardChatWhole}, "chatcmpl-guard"},
		{"chat/stream/anthropic", provider.Chat, true, reply{200, "text/event-stream", wrongAnthropicStream}, "msg_wrong"},
		{"chat/whole/anthropic", provider.Chat, false, reply{200, "application/json", wrongAnthropicWhole}, "msg_wrong"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			a := &scripted{replies: []reply{tc.bad}}
			b := &scripted{replies: []reply{goodReply(tc.proto, tc.stream)}}
			notAPIOn(t, "a", tc.proto, a)
			notAPIOn(t, "b", tc.proto, b)
			if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			s := New()
			rec := askOn(s, tc.proto, "group/g", tc.stream)
			body := rec.Body.String()
			if rec.Code != http.StatusOK || !strings.Contains(body, "from b") || strings.Contains(body, tc.leak) {
				t.Fatalf("got %d %s (a=%d b=%d)", rec.Code, body, a.n, b.n)
			}
			if a.n != 1 || b.n != 1 {
				t.Fatalf("a asked %d, b asked %d; want 1, 1", a.n, b.n)
			}
			r := s.trace.routes[len(s.trace.routes)-1]
			if len(r.Tries) != 2 {
				t.Fatalf("tries = %d, want 2: %+v", len(r.Tries), r.Tries)
			}
			if r.Tries[0].Status != http.StatusBadGateway || r.Tries[0].Fail != failOther || r.Tries[0].Rest != nil {
				t.Fatalf("try[0] = %+v; want status 502, fail other, no rest", r.Tries[0])
			}
		})
	}
}

// When no other candidate is left (lone provider or all group members
// affected), a wrong-protocol 200 becomes a 502 error in the client's own
// protocol instead of forwarding a 200 payload the client cannot parse, and
// is not retried against the same provider.
func TestWrongProtocolReplyAloneReturnsBadGateway(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
		bad    reply
	}{
		{"stream", true, reply{200, "text/event-stream", guardChatStream}},
		{"whole", false, reply{200, "application/json", guardChatWhole}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			a := &scripted{replies: []reply{tc.bad, tc.bad, tc.bad}}
			notAPIOn(t, "a", provider.Anthropic, a)
			s := New()
			rec := askOn(s, provider.Anthropic, "a/m", tc.stream)
			body := rec.Body.String()
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502; body = %s", rec.Code, body)
			}
			if strings.Contains(body, "chat.completion") || strings.Contains(body, "chatcmpl-guard") {
				t.Fatalf("raw wrong-protocol payload leaked: %s", body)
			}
			var errObj struct {
				Type  string `json:"type"`
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(body), &errObj); err != nil || errObj.Type != "error" || !strings.Contains(errObj.Error.Message, "from-security-guard") {
				t.Fatalf("want Anthropic error naming from-security-guard, got: %s", body)
			}
			if a.n != 1 {
				t.Fatalf("a asked %d times, want 1 (no same-provider retry)", a.n)
			}
		})
	}
}
