package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// commandCodeInputRefusal is Command Code's answer, byte for byte as #5181
// and #5259 quote it, to a Responses request with an input item other than
// a message, a function call, its output or reasoning.
const commandCodeInputRefusal = `{"error":{"message":"input items may be messages, function calls, function call outputs and reasoning. Item references need stored responses, which are not kept.","type":"invalid_request_error","param":"input","code":"unsupported_input_item"}}`

// compactSubagentRequest is a Codex subagent's compaction, as #5259's: its
// task and a follow-up came as agent_message items, among calls Command
// Code doesn't take either.
const compactSubagentRequest = `{"model":"fake/m1","stream":true,"store":false,"prompt_cache_key":"subagent-1","input":[
  {"type":"agent_message","author":"/root","recipient":"/root/research","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nPayload:\nCompare Vault secret rotation with rollout restart timing."}]},
  {"type":"custom_tool_call","id":"ctc_1","call_id":"call_1","name":"apply_patch","input":"*** Begin Patch\n*** Add File: notes.md\n+rotation results\n*** End Patch"},
  {"type":"custom_tool_call_output","call_id":"call_1","output":"Success. Updated the following files:\nA notes.md"},
  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"The rotation results are in notes.md."}]},
  {"type":"agent_message","author":"/root","recipient":"/root/research","content":[{"type":"input_text","text":"Message Type: MESSAGE\nPayload:\nKeep the observed event order in the summary."}]},
  {"type":"compaction_trigger"}]}`

// commandCodeTakes is whether Command Code's /responses takes a request's
// input as its refusal says: messages, function calls, their outputs and
// reasoning, nothing else.
func commandCodeTakes(t *testing.T, body []byte) bool {
	t.Helper()
	var q struct {
		Input []struct {
			Type string `json:"type"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &q); err != nil {
		t.Fatalf("summary request: %v: %s", err, body)
	}
	for _, it := range q.Input {
		switch it.Type {
		case "", "message", "function_call", "function_call_output", "reasoning":
		default:
			return false
		}
	}
	return true
}

// #5181, #5259: Command Code turned a Codex compaction away for an input
// item it doesn't take, and magpie passed the 400 to Codex, three times,
// until the guard held the conversation for 5 minutes. Refused so — as
// the 400 itself, through another magpie in front of it (as a 400 or its
// 502), or in the summary's stream — the summary is asked again with the
// conversation as text, which Command Code takes, and the subagent's task
// is in it.
func TestCodexCompactUnsupportedInputItemAsksAgainAsText(t *testing.T) {
	remote := `{"error":{"message":` + quote("Remote magpie: Command Code: "+commandCodeInputRefusal) + `,"type":"invalid_request_error"}}`
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"400", 400, commandCodeInputRefusal},
		{"another magpie's 400", 400, remote},
		{"another magpie's 502", 502, remote},
		{"stream error", 200, sse(`data: {"type":"error",` + strings.TrimPrefix(commandCodeInputRefusal, "{"))},
		{"failed response", 200, sse(`data: {"type":"response.failed","response":{"id":"r1","status":"failed",` + strings.TrimPrefix(commandCodeInputRefusal, "{") + `}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{t: t}
			f.refuse = func(b []byte) (int, string) {
				f.reply = wholeSummary(provider.Responses)
				if !commandCodeTakes(t, b) {
					if tc.status != 200 {
						return tc.status, tc.body
					}
					f.reply = tc.body
				}
				return 0, ""
			}
			setup(t, provider.Responses, f)
			s := New()
			// more than the guard's count: none of them is held
			for i := range compactFailsMost + 1 {
				rec := codexPostTo(s, compactSubagentRequest)
				if rec.Code != 200 {
					t.Fatalf("try %d: %d %s", i, rec.Code, rec.Body)
				}
				if got := compactedText(t, rec.Body.String()); got != "SUMMARY" {
					t.Fatalf("try %d: summary %q, want the model's", i, got)
				}
			}
			if !commandCodeTakes(t, f.got) {
				t.Errorf("asked again with what Command Code refuses: %s", f.got)
			}
			for _, want := range []string{"Compare Vault secret rotation", "Keep the observed event order", "The rotation results are in notes.md"} {
				if !strings.Contains(string(f.got), want) {
					t.Errorf("the text lacks %q: %s", want, f.got)
				}
			}
		})
	}
}

// Refused as text too, the summary is magpie's own, the subagent's task
// and follow-up in it as what was asked, and Codex goes on.
func TestCodexCompactUnsupportedInputItemCompactsLocally(t *testing.T) {
	f := &fake{t: t, refuse: func([]byte) (int, string) { return 400, commandCodeInputRefusal }}
	setup(t, provider.Responses, f)
	code, body := codexPost(t, compactSubagentRequest)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	got := compactedText(t, body)
	for _, want := range []string{"without a model", "input items may be messages", "Compare Vault secret rotation", "Keep the observed event order", "The rotation results are in notes.md."} {
		if !strings.Contains(got, want) {
			t.Errorf("local summary lacks %q: %s", want, got)
		}
	}
	if f.calls != 2 {
		t.Errorf("asked %d times, want the request and its text", f.calls)
	}
}

// The same words from a key or account refusal, or in a reply that
// didn't fail, are no refusal of the input: a 401, 403 or 429 still goes
// back to Codex as it came, never asked as text.
func TestCodexCompactUnsupportedInputWordsOnAnAccountFailureStay(t *testing.T) {
	for _, status := range []int{401, 403, 429} {
		f := &fake{t: t, refuse: func([]byte) (int, string) { return status, commandCodeInputRefusal }}
		setup(t, provider.Responses, f)
		code, body := codexPost(t, compactSubagentRequest)
		if code != status || strings.Contains(string(f.got), "This is the conversation so far") {
			t.Errorf("%d: %d %s (last asked %s)", status, code, body, f.got)
		}
	}
}

// hy4InputTypeRefusal is hy4-openai's answer, as #8008 quotes it, to a
// Responses request whose input has an item type its whitelist leaves
// out: no "unsupported_input_item", no param, another code — only
// "input[N].type must be one of" and the allowed types say what refused.
const hy4InputTypeRefusal = `{"id":"err_1","error":{"message":"hy4-openai: input[261].type must be one of: message, function_call, function_call_output, web_search_call, item_reference, reasoning, tool_search_call, tool_search_output (HTTP 400, invalid_input, request id req_1)","type":"invalid_input","param":"","code":""}}`

// compactApplyPatchRequest is a Codex compaction after apply_patch ran
// with the catalog's freeform apply_patch (#8008): the replayed history
// carries custom_tool_call pairs, hy4's whitelist takes none of them,
// and every later turn's compaction failed the same way, the thread
// stuck.
const compactApplyPatchRequest = `{"model":"fake/m1","stream":true,"store":false,"prompt_cache_key":"apply-patch-1","input":[
  {"type":"message","role":"user","content":[{"type":"input_text","text":"Split the retry helper out of send.go."}]},
  {"type":"custom_tool_call","id":"ctc_1","call_id":"call_1","name":"apply_patch","input":"*** Begin Patch\n*** Update File: send.go\n@@\n-func send() {\n+func sendWithRetry() {\n*** End Patch"},
  {"type":"custom_tool_call_output","call_id":"call_1","output":"Success. Updated the following files:\nM send.go"},
  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"The retry helper is in place."}]},
  {"type":"compaction_trigger"}]}`

// hy4Takes is whether hy4's /responses takes a request's input as its
// refusal's whitelist says.
func hy4Takes(t *testing.T, body []byte) bool {
	t.Helper()
	var q struct {
		Input []struct {
			Type string `json:"type"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &q); err != nil {
		t.Fatalf("summary request: %v: %s", err, body)
	}
	for _, it := range q.Input {
		switch it.Type {
		case "", "message", "function_call", "function_call_output", "web_search_call", "item_reference", "reasoning", "tool_search_call", "tool_search_output":
		default:
			return false
		}
	}
	return true
}

// #8008: hy4-openai turned a compaction away for the custom_tool_call
// items its input whitelist leaves out — "input[261].type must be one
// of: …", another code, no param, a wording none of magpie's refusal
// readers knew — and the 400 went back to Codex, so every later turn
// asked the same compaction and failed the same way. Refused so — as the
// 400 itself, through another magpie in front of it, or in the summary's
// stream — the summary is asked again with the conversation as text,
// which the whitelist takes, the patch call among it, and never counts
// toward the guard's three.
func TestCodexCompactInputTypeWhitelistRefusalAsksAgainAsText(t *testing.T) {
	remote := `{"error":{"message":` + quote("Remote magpie: fake: "+hy4InputTypeRefusal) + `,"type":"invalid_input"}}`
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"400", 400, hy4InputTypeRefusal},
		{"another magpie's 502", 502, remote},
		{"stream error", 200, sse(`data: {"type":"error",` + strings.TrimPrefix(hy4InputTypeRefusal, "{"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{t: t}
			f.refuse = func(b []byte) (int, string) {
				f.reply = wholeSummary(provider.Responses)
				if !hy4Takes(t, b) {
					if tc.status != 200 {
						return tc.status, tc.body
					}
					f.reply = tc.body
				}
				return 0, ""
			}
			setup(t, provider.Responses, f)
			s := New()
			// more than the guard's count: none of them is held
			for i := range compactFailsMost + 1 {
				rec := codexPostTo(s, compactApplyPatchRequest)
				if rec.Code != 200 {
					t.Fatalf("try %d: %d %s", i, rec.Code, rec.Body)
				}
				if got := compactedText(t, rec.Body.String()); got != "SUMMARY" {
					t.Fatalf("try %d: summary %q, want the model's", i, got)
				}
			}
			if !hy4Takes(t, f.got) {
				t.Errorf("asked again with what the whitelist refuses: %s", f.got)
			}
			for _, want := range []string{"Split the retry helper", "apply_patch", "Begin Patch", "The retry helper is in place"} {
				if !strings.Contains(string(f.got), want) {
					t.Errorf("the text lacks %q: %s", want, f.got)
				}
			}
		})
	}
}

// Refused as text too, the summary is magpie's own, what was asked and
// the last reply in it, and Codex goes on.
func TestCodexCompactInputTypeWhitelistRefusalCompactsLocally(t *testing.T) {
	f := &fake{t: t, refuse: func([]byte) (int, string) { return 400, hy4InputTypeRefusal }}
	setup(t, provider.Responses, f)
	code, body := codexPost(t, compactApplyPatchRequest)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	got := compactedText(t, body)
	for _, want := range []string{"without a model", "input[261].type must be one of", "Split the retry helper", "The retry helper is in place."} {
		if !strings.Contains(got, want) {
			t.Errorf("local summary lacks %q: %s", want, got)
		}
	}
	if f.calls != 2 {
		t.Errorf("asked %d times, want the request and its text", f.calls)
	}
}
