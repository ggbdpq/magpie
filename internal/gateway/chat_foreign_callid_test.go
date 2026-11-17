package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// runyuxu (#6941): a Chat upstream that validates a tool call's id at 64
// characters refused a request whose history carried a longer one another
// provider had made — Cursor's `call-<uuid>-<n>__fc_<uuid>_0` is 87–88
// characters, and the Responses→Chat translation wrote call ids as they
// came (`Invalid 'input[11].call_id': string too long … 88`). Only the
// direct Codex paths bound them. The translation bounds an overlong id to
// the same 64 hex characters every time — the call and its result still
// pair, and the next turn sends the same ids byte for byte.
func TestChatBoundsAForeignCallID(t *testing.T) {
	long := "call-3f2b8c1a-9d4e-4f6a-8b7c-1a2b3c4d5e6f-7__fc_5e8f2a1b-4c3d-4e9f-8a7b-c9d0e1f2a3b4_0"
	if len(long) <= 64 {
		t.Fatalf("the fixture id is only %d characters", len(long))
	}
	r := &Request{Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "run it"}}},
		{Role: "assistant", Parts: []Part{call(long)}},
		{Role: "user", Parts: []Part{result(long, "done")}},
	}}
	var q struct {
		Messages []struct {
			Role      string `json:"role"`
			ToolCalls []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(buildChat(r, "m", "", false), &q); err != nil {
		t.Fatal(err)
	}
	var callID, answerID string
	for _, m := range q.Messages {
		if len(m.ToolCalls) > 0 {
			callID = m.ToolCalls[0].ID
		}
		if m.ToolCallID != "" {
			answerID = m.ToolCallID
		}
	}
	if len(callID) != 64 {
		t.Errorf("the call's id is %d characters: %q", len(callID), callID)
	}
	if answerID != callID {
		t.Errorf("the result answers %q, want the bound call id %q", answerID, callID)
	}
	if strings.Contains(callID+answerID, long[:32]) {
		t.Errorf("the foreign id leaked through: %q", callID)
	}
	// the same history bounds to the same ids, so the next turn sends the
	// same request and the upstream's prompt cache holds
	again := buildChat(r, "m", "", false)
	if !strings.Contains(string(again), callID) {
		t.Errorf("a second build bounds differently:\n%s", again)
	}

	// a call whose id is fine stays as it is
	short := &Request{Messages: []Message{
		{Role: "assistant", Parts: []Part{call("call_short")}},
		{Role: "user", Parts: []Part{result("call_short", "done")}},
	}}
	var q2 struct {
		Messages []struct {
			ToolCalls []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(buildChat(short, "m", "", false), &q2); err != nil {
		t.Fatal(err)
	}
	if len(q2.Messages) == 0 || q2.Messages[0].ToolCalls[0].ID != "call_short" || q2.Messages[1].ToolCallID != "call_short" {
		t.Fatalf("a short id was rewritten: %+v", q2.Messages)
	}
}
