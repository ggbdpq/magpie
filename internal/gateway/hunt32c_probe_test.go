package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// hunt32-C probe: adversarial sequences through the whole gateway.
// Not for commit: these are throwaway probes.

// T1: the format-refusal loop's body drift. The upstream refuses the
// schema, then the json_object form; the third try carries no field at
// all. What did each try's body hold?
func TestProbeFormatLoopBodies(t *testing.T) {
	var bodies []string
	f := &fake{t: t}
	f.refuse = func(body []byte) (int, string) {
		bodies = append(bodies, string(body))
		if strings.Contains(string(body), `"response_format":{"type":"json_schema"`) {
			return 400, `{"error":{"message":"text.format type 'json_schema' is not supported, only 'text' and 'json_object' are allowed"}}`
		}
		if strings.Contains(string(body), `response_format`) {
			return 400, `{"error":{"message":"response_format is not supported by this model"}}`
		}
		return 0, ""
	}
	f.reply = sse(`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)
	setup(t, provider.Chat, f)
	body := `{"model":"fake/m1","stream":true,"response_format":{"type":"json_schema","json_schema":{"name":"out","schema":{"type":"object","properties":{"a":{"type":"string"}}}}},"messages":[{"role":"user","content":"give me json"}]}`
	code, out := post(t, "/v1/chat/completions", body)
	t.Logf("status=%d out=%s", code, out)
	for i, b := range bodies {
		t.Logf("try %d: %s", i, b)
	}
	if len(bodies) < 2 {
		t.Fatalf("the format loop did not retry: %d tries", len(bodies))
	}
}

// T2: a Chat upstream whose one chunk carries two tool_calls deltas of the
// same index, the second a continuation of the first's arguments, with the
// id repeated (some relays do). The client must see one call.
func TestProbeSameIndexRepeatedID(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"echo","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"arguments":"{\"x\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"arguments":"1}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Anthropic, f)
	body := `{"model":"fake/m1","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"run"}]}`
	code, out := post(t, "/v1/messages", body)
	t.Logf("status=%d out=%s", code, out)
	starts := strings.Count(out, `"type":"tool_use"`)
	if starts != 1 {
		t.Fatalf("client saw %d tool_use starts, want 1: %s", starts, out)
	}
}

// T3: a Responses relay whose upstream ends with response.failed after a
// message's output_item.done. The message held for its phase is released
// as commentary, never final_answer.
func TestProbeFailedEndPhase(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant"}}`,
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"working on it"}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"working on it"}]}}`,
		`data: {"type":"response.failed","response":{"id":"r1","status":"failed","error":{"code":"server_error","message":"boom"},"output":[]}}`)}
	setup(t, provider.Responses, f)
	body := `{"model":"fake/m1","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]}]}`
	code, out := post(t, "/v1/responses", body)
	t.Logf("status=%d out=%s", code, out)
	if strings.Contains(out, `"phase":"final_answer"`) {
		t.Fatalf("a failed reply's message was told final_answer: %s", out)
	}
}

// T5: an Anthropic upstream sends a redacted_thinking block to a Chat
// client. What does the client get, streaming and whole?
func TestProbeSealedToChat(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-x","usage":{"input_tokens":1,"output_tokens":1}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"ENC"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	body := `{"model":"fake/m1","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	code, out := post(t, "/v1/chat/completions", body)
	t.Logf("stream status=%d out=%s", code, out)

	f2 := &fake{t: t, reply: sse(
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-x","usage":{"input_tokens":1,"output_tokens":1}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"ENC"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f2)
	body2 := `{"model":"fake/m1","stream":false,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	code2, out2 := post(t, "/v1/chat/completions", body2)
	t.Logf("whole status=%d out=%s", code2, out2)
	var whole map[string]any
	json.Unmarshal([]byte(out2), &whole)
	msg, _ := whole["choices"].([]any)
	if len(msg) > 0 {
		m := msg[0].(map[string]any)["message"].(map[string]any)
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		t.Logf("whole message keys: %v content=%v", ks, m["content"])
	}
}

// T6: an Anthropic client's request whose history answers one tool_use
// twice (a replayed approval), translated to a Chat upstream. The Chat
// build drops the second answer (pairToolMessages). Which pair of ids came
// back?
func TestProbeAnthropicDoubleAnswerToChat(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	body := `{"model":"fake/m1","stream":true,"max_tokens":100,"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"run"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"call_a","name":"echo","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_a","content":"first"},{"type":"tool_result","tool_use_id":"call_a","content":"second"}]},` +
		`{"role":"user","content":[{"type":"text","text":"go on"}]}]}`
	code, out := post(t, "/v1/messages", body)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	n := strings.Count(string(f.got), `"call_a"`)
	t.Logf("upstream body: %s", f.got)
	t.Logf("call_a mentions: %d", n)
}

var _ = fmt.Sprintf
