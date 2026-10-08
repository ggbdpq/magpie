package gateway

import (
	"encoding/json"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #1341: an upstream that turns a relayed /responses into chat form checks
// the tool exchange's pairing — volcengine's coding plan answers a lone
// function_call with 400 "An assistant message with 'tool_calls' must be
// followed by tool messages responding to each 'tool_call_id'" — and the
// passthrough had no equivalent of the Chat path's pairToolMessages.
func TestPairToolItemsMendsUnpairedExchanges(t *testing.T) {
	const interrupted = "[The result of this tool call is unavailable: the turn was interrupted.]"
	// a lone call gets a synthetic result right after itself
	body := `{"model":"m","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},` +
		`{"type":"function_call","call_id":"call_lone","name":"echo","arguments":"{}"},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"again"}]}]}`
	var q struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(pairToolItems([]byte(body)), &q); err != nil {
		t.Fatal(err)
	}
	if len(q.Input) != 4 {
		t.Fatalf("items: %d", len(q.Input))
	}
	if q.Input[2]["type"] != "function_call_output" || q.Input[2]["call_id"] != "call_lone" || q.Input[2]["output"] != interrupted {
		t.Fatalf("no synthetic result after the lone call: %v", q.Input[2])
	}

	// a lone custom call gets the custom kind of result
	custom := `{"model":"m","input":[{"type":"custom_tool_call","call_id":"call_c","name":"patch","input":"x"}]}`
	q.Input = nil
	if err := json.Unmarshal(pairToolItems([]byte(custom)), &q); err != nil {
		t.Fatal(err)
	}
	if len(q.Input) != 2 || q.Input[1]["type"] != "custom_tool_call_output" || q.Input[1]["call_id"] != "call_c" {
		t.Fatalf("no synthetic custom result: %v", q.Input)
	}

	// a paired exchange, parallel calls answered out of order, and a
	// request with no tool items at all go unchanged, byte for byte
	for _, keep := range []string{
		`{"model":"m","input":[{"type":"function_call","call_id":"c1","name":"echo","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`,
		`{"model":"m","input":[{"type":"function_call","call_id":"c1","name":"echo","arguments":"{}"},{"type":"function_call","call_id":"c2","name":"echo","arguments":"{}"},{"type":"function_call_output","call_id":"c2","output":"second"},{"type":"function_call_output","call_id":"c1","output":"first"}]}`,
		`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
	} {
		if got := pairToolItems([]byte(keep)); string(got) != keep {
			t.Fatalf("paired exchange rewritten: %s", got)
		}
	}

	// a result naming a call this input doesn't carry stays as it is —
	// that mismatch is orphanedToolOutputs' alone, and a call id on a
	// result is never stripped
	kept := `{"model":"m","input":[{"type":"function_call_output","call_id":"call_gone","output":"late result"}]}`
	if got := pairToolItems([]byte(kept)); string(got) != kept {
		t.Fatalf("a result with a call id of its own rewritten: %s", got)
	}

	// the caller's buffer is untouched
	original := []byte(`{"model":"m","input":[{"type":"function_call","call_id":"call_lone","name":"echo","arguments":"{}"}]}`)
	before := append([]byte(nil), original...)
	pairToolItems(original)
	if string(original) != string(before) {
		t.Fatal("mutated caller's request buffer")
	}

	// no input, or an input that isn't items, goes back as it came
	for _, noop := range []string{`not json`, `{}`, `{"input":"hello"}`} {
		if got := pairToolItems([]byte(noop)); string(got) != noop {
			t.Fatalf("unchanged input rewritten: %s", got)
		}
	}
}

// The whole relay path mends a lone call, not only the function does.
func TestPassthroughRelayMendsALoneToolCall(t *testing.T) {
	f := &fake{t: t, reply: sse(`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`)}
	setup(t, provider.Responses, f)
	p, err := provider.Find("fake")
	if err != nil {
		t.Fatal(err)
	}
	p.Searches = true // a search-capable provider relays
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"fake/m1","stream":true,"input":[{"type":"function_call","call_id":"call_lone","name":"echo","arguments":"{}"}]}`
	if code, response := post(t, "/v1/responses", body); code != 200 {
		t.Fatalf("status %d: %s", code, response)
	}
	var sent struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(f.got, &sent); err != nil {
		t.Fatal(err)
	}
	for _, item := range sent.Input {
		if item["type"] == "function_call_output" && item["call_id"] == "call_lone" {
			return
		}
	}
	t.Fatalf("the lone call went through unanswered: %s", f.got)
}
