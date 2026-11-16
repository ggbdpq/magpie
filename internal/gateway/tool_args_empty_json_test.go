package gateway

import (
	"bufio"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// anthropicToChat runs an Anthropic event stream through the decoder and the
// Chat Completions encoder, as a translated reply goes out, and returns
// every event written, as a client reading the stream sees them.
func anthropicToChat(t *testing.T, events ...string) []map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	enc := &chatEncoder{w: newSSEWriter(rec), model: "m"}
	var d = anthropicDecoder{server: map[int]bool{}, whole: map[int]bool{}}
	for _, e := range events {
		if err := d.decode(e, enc.event); err != nil {
			t.Fatal(err)
		}
	}
	enc.finish()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var m map[string]any
			if json.Unmarshal([]byte(data), &m) == nil {
				out = append(out, m)
			}
		}
	}
	return out
}

// streamedCall is a call as a Chat client accumulates it: its name, and the
// arguments its deltas add up to.
type streamedCall struct {
	name string
	args string
}

// chatCallsOf reads a translated stream the way a client does, by call
// index, and gives the reply's finish reason.
func chatCallsOf(t *testing.T, out []map[string]any) (calls []streamedCall, finish string) {
	t.Helper()
	for _, ev := range out {
		choices, _ := ev["choices"].([]any)
		for _, c := range choices {
			ch, _ := c.(map[string]any)
			if ch == nil {
				continue
			}
			if f, _ := ch["finish_reason"].(string); f != "" {
				finish = f
			}
			d, _ := ch["delta"].(map[string]any)
			if d == nil {
				continue
			}
			tcs, _ := d["tool_calls"].([]any)
			for _, tc := range tcs {
				m, _ := tc.(map[string]any)
				if m == nil {
					continue
				}
				idx := 0
				if f, _ := m["index"].(float64); f != 0 || m["index"] != nil {
					idx = int(f)
				}
				for len(calls) <= idx {
					calls = append(calls, streamedCall{})
				}
				if fn, _ := m["function"].(map[string]any); fn != nil {
					if n, _ := fn["name"].(string); n != "" {
						calls[idx].name = n
					}
					if a, _ := fn["arguments"].(string); a != "" {
						calls[idx].args += a
					}
				}
			}
		}
	}
	return calls, finish
}

// A tool_use whose start carries its input as an empty object, with no
// input_json_delta after it (the stream of #6832), is a call whose
// arguments the client never receives a delta of. Chat's calls begin as ""
// and grow by deltas, and "" is no JSON: a client that parses a call's
// arguments before it runs the tool (most do) rejects the call. The call
// ends with its arguments complete, "{}".
func TestChatStreamEmptyToolInputEndsAsEmptyObject(t *testing.T) {
	calls, finish := chatCallsOf(t, anthropicToChat(t,
		`{"type":"message_start","message":{"id":"msg_1","model":"m"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_example","name":"get_status","input":{}}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`,
	))
	if len(calls) != 1 {
		t.Fatalf("calls = %d; want 1", len(calls))
	}
	if calls[0].name != "get_status" {
		t.Fatalf("call name = %q; want get_status", calls[0].name)
	}
	if calls[0].args != "{}" {
		t.Fatalf("call arguments = %q; want {}", calls[0].args)
	}
	if finish != "tool_calls" {
		t.Fatalf("finish reason = %q; want tool_calls", finish)
	}
}

// The first of two calls, its input empty and no argument delta after,
// ends "{}" where the second call begins; the second's arguments are its
// own, nothing of the first's leaking into them.
func TestChatStreamCallWithoutArgumentsEndsAtTheNext(t *testing.T) {
	calls, _ := chatCallsOf(t, anthropicToChat(t,
		`{"type":"message_start","message":{"id":"msg_1","model":"m"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_status","input":{}}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_2","name":"run","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"ls\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`,
	))
	if len(calls) != 2 {
		t.Fatalf("calls = %d; want 2", len(calls))
	}
	if calls[0].args != "{}" {
		t.Fatalf("first call's arguments = %q; want {}", calls[0].args)
	}
	if calls[0].name != "get_status" {
		t.Fatalf("first call's name = %q; want get_status", calls[0].name)
	}
	if calls[1].args != `{"cmd":"ls"}` {
		t.Fatalf("second call's arguments = %q; want {\"cmd\":\"ls\"}", calls[1].args)
	}
	if calls[1].name != "run" {
		t.Fatalf("second call's name = %q; want run", calls[1].name)
	}
}

// Arguments a call does stream reach the client as they came: completing
// an argumentless call adds nothing to one that has arguments.
func TestChatStreamToolArgumentsStillAsStreamed(t *testing.T) {
	calls, _ := chatCallsOf(t, anthropicToChat(t,
		`{"type":"message_start","message":{"id":"msg_1","model":"m"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"run","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"go"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":" test\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`,
	))
	if len(calls) != 1 {
		t.Fatalf("calls = %d; want 1", len(calls))
	}
	if calls[0].args != `{"cmd":"go test"}` {
		t.Fatalf("call arguments = %q; want {\"cmd\":\"go test\"}", calls[0].args)
	}
}
