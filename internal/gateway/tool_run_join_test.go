package gateway

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// itemsOf is a Responses request's input items as "type:call_id" strings.
func itemsOf(t *testing.T, body []byte) []string {
	t.Helper()
	var q struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(body, &q); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	var out []string
	for _, it := range q.Input {
		typ, _ := it["type"].(string)
		id, _ := it["call_id"].(string)
		out = append(out, typ+":"+id)
	}
	return out
}

func sameOrder(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("items %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("items %v, want %v", got, want)
		}
	}
}

// Codex writes an item between one batch's tool results itself: when a tool
// returned an image it resized, its <image_resize_notice> goes between the
// two results of one assistant step (yetone/magpie#1752). A strict upstream
// reads the batch on the chat form it turns a relayed request into and
// refuses the item sitting inside it, naming a result that is in the
// request: Volcengine's coding plan answers 400 "An assistant message with
// 'tool_calls' must be followed by tool messages responding to each
// 'tool_call_id'". The relay hands the batch over whole, the notice after
// it, like pairToolMessages has mended the Chat build since #1275.
func TestResponsesRelayJoinsABatchsToolRun(t *testing.T) {
	f := &fake{t: t, reply: sse(`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`)}
	f.refuse = func(body []byte) (int, string) {
		// the upstream's own check, on the chat form it builds: no item may
		// sit between a batch's calls and one of their results
		var sent struct {
			Input []struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
			} `json:"input"`
		}
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Fatal(err)
		}
		open := map[string]bool{}
		for _, it := range sent.Input {
			switch it.Type {
			case "function_call":
				open[it.CallID] = true
			case "function_call_output":
				delete(open, it.CallID)
			default:
				if len(open) > 0 {
					return 400, `{"error":{"code":null,"message":"Fake: An assistant message with 'tool_calls' must be followed by tool messages responding to each 'tool_call_id'. (insufficient tool messages following tool_calls message)"}}`
				}
			}
		}
		return 0, ""
	}
	setup(t, provider.Responses, f)
	body := `{"model":"fake/m1","stream":true,"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"look"}]},` +
		`{"type":"function_call","call_id":"call_00","name":"view_image","arguments":"{}"},` +
		`{"type":"function_call","call_id":"call_01","name":"exec_command","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_00","output":"resized image"},` +
		`{"type":"message","role":"developer","content":[{"type":"input_text","text":"<image_resize_notice>Image 1 of 1 was resized.</image_resize_notice>"}]},` +
		`{"type":"function_call_output","call_id":"call_01","output":"PIL 11.3.0"}]}`
	if code, out := post(t, "/v1/responses", body); code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	if f.path != "/v1/responses" {
		t.Fatalf("not relayed as Responses: %s", f.path)
	}
	sameOrder(t, itemsOf(t, f.got), []string{
		"message:",
		"function_call:call_00",
		"function_call:call_01",
		"function_call_output:call_00",
		"function_call_output:call_01",
		"message:",
	})
}

// The notice goes after the batch it broke; whole exchanges, a call nothing
// answers, and non-Responses bodies go byte for byte.
func TestJoinToolRuns(t *testing.T) {
	// the reporter's shape: two parallel calls, an image result, Codex's
	// resize notice between the results, the second result (#1752)
	const batch = `{"model":"m","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"look"}]},` +
		`{"type":"function_call","call_id":"call_00","name":"view_image","arguments":"{}"},` +
		`{"type":"function_call","call_id":"call_01","name":"exec_command","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_00","output":"resized image"},` +
		`{"type":"message","role":"developer","content":[{"type":"input_text","text":"<image_resize_notice>resized</image_resize_notice>"}]},` +
		`{"type":"function_call_output","call_id":"call_01","output":"PIL 11.3.0"}]}`
	original := []byte(batch)
	fixed := joinToolRuns(original)
	if !bytes.Equal(original, []byte(batch)) {
		t.Fatal("mutated the caller's request buffer")
	}
	sameOrder(t, itemsOf(t, fixed), []string{
		"message:",
		"function_call:call_00",
		"function_call:call_01",
		"function_call_output:call_00",
		"function_call_output:call_01",
		"message:",
	})
	// the moved notice and both results keep their bytes
	for _, keep := range []string{"image_resize_notice", "PIL 11.3.0", "resized image"} {
		if !bytes.Contains(fixed, []byte(keep)) {
			t.Fatalf("lost %q: %s", keep, fixed)
		}
	}
	// run again and nothing moves
	if again := joinToolRuns(fixed); !bytes.Equal(again, fixed) {
		t.Fatalf("not idempotent:\n%s\n%s", fixed, again)
	}

	// an item between a call and the result that answers it moves after it
	in := inputOf(t, joinToolRuns([]byte(`{"model":"m","input":[`+
		`{"type":"function_call","call_id":"call_a","name":"echo","arguments":"{}"},`+
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"meanwhile"}]},`+
		`{"type":"function_call_output","call_id":"call_a","output":"a"}]}`)))
	if len(in) != 3 || in[1]["type"] != "function_call_output" || in[2]["type"] != "message" {
		t.Fatalf("the item stayed inside the run: %v", in)
	}

	// results out of a batch's order, a notice between them: both results
	// go ahead of it
	outOfOrder := joinToolRuns([]byte(`{"model":"m","input":[` +
		`{"type":"function_call","call_id":"call_a","name":"echo","arguments":"{}"},` +
		`{"type":"function_call","call_id":"call_b","name":"echo","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_b","output":"b"},` +
		`{"type":"message","role":"developer","content":[{"type":"input_text","text":"note"}]},` +
		`{"type":"function_call_output","call_id":"call_a","output":"a"}]}`))
	sameOrder(t, itemsOf(t, outOfOrder), []string{
		"function_call:call_a", "function_call:call_b",
		"function_call_output:call_b", "function_call_output:call_a", "message:",
	})

	// a result with no call id inside a run moves after it, where
	// orphanedToolOutputs turns it into a user message past the results
	standalone := joinToolRuns([]byte(`{"model":"m","input":[` +
		`{"type":"function_call","call_id":"call_a","name":"echo","arguments":"{}"},` +
		`{"type":"function_call_output","output":"standalone"},` +
		`{"type":"function_call_output","call_id":"call_a","output":"a"}]}`))
	sameOrder(t, itemsOf(t, standalone), []string{
		"function_call:call_a", "function_call_output:call_a", "function_call_output:",
	})

	// exchanges that are each whole, an item between them, a call nothing
	// answers (pairToolItems sets its result after it), a result naming a
	// call an earlier request carried, and bodies that are not Responses
	// requests go byte for byte
	for _, keep := range []string{
		`{"model":"m","input":[` +
			`{"type":"function_call","call_id":"call_a","name":"echo","arguments":"{}"},` +
			`{"type":"function_call_output","call_id":"call_a","output":"a"},` +
			`{"type":"message","role":"developer","content":[{"type":"input_text","text":"a note"}]},` +
			`{"type":"function_call","call_id":"call_b","name":"echo","arguments":"{}"},` +
			`{"type":"function_call_output","call_id":"call_b","output":"b"}]}`,
		`{"model":"m","input":[` +
			`{"type":"function_call","call_id":"call_a","name":"echo","arguments":"{}"},` +
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"never mind"}]}]}`,
		`{"model":"m","input":[` +
			`{"type":"function_call","call_id":"call_a","name":"echo","arguments":"{}"},` +
			`{"type":"function_call","call_id":"call_b","name":"echo","arguments":"{}"},` +
			`{"type":"function_call_output","call_id":"call_b","output":"b"},` +
			`{"type":"function_call_output","call_id":"call_a","output":"a"}]}`,
		`{"model":"m","previous_response_id":"resp_1","input":[` +
			`{"type":"function_call","call_id":"call_a","name":"echo","arguments":"{}"},` +
			`{"type":"function_call_output","call_id":"call_gone","output":"late"},` +
			`{"type":"function_call_output","call_id":"call_a","output":"a"}]}`,
		`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
		`not json`, `{}`, `{"input":"hello"}`,
	} {
		if got := joinToolRuns([]byte(keep)); string(got) != keep {
			t.Fatalf("rewritten:\n%s\nto\n%s", keep, got)
		}
	}
}
