package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Claude Code 2.1.296 and later send their environment as a message with
// role "system" inside messages (Request.envInTurns). The Chat and
// Anthropic builds fold such a message into the system prompt
// (responseInstructions); buildResponses wrote the role as it came, and a
// relay in front of the ChatGPT backend — which takes user, assistant and
// developer alone — answered every translated turn 400 "Upstream request
// failed", and the 400 failed no one over (#4632). A translated request's
// system message is the harness's, not an instruction item of a Responses
// client's own: it is said the way the Chat and Anthropic builds say it.
// A Responses client's own instruction items keep their place (#2532,
// TestResponsesInstructionRolesThroughSearch).
func TestClaudeSystemTurnSaidInTheInstructionsOnResponses(t *testing.T) {
	srv, seen := relayOfEvery(t)
	fresh(t)
	if err := provider.Save(provider.Provider{
		ID: "responses-relay", Name: "Responses relay", Key: "k",
		Responses: srv.URL + "/v1", Models: []string{"example-model"},
	}); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"responses-relay/example-model","max_tokens":64,"stream":true,` +
		`"system":"You are Claude Code.",` +
		`"messages":[` +
		`{"role":"system","content":"env: working dir /home/me"},` +
		`{"role":"user","content":"hi"},` +
		`{"role":"assistant","content":"hello"},` +
		`{"role":"system","content":"the user switched rooms"}]}`
	if code, out := post(t, "/v1/messages", body); code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	paths, bodies := seen()
	if len(paths) != 1 || paths[0] != "/v1/responses" {
		t.Fatalf("upstream paths %v, want one Responses request", paths)
	}
	var got struct {
		Instructions string `json:"instructions"`
		Input        []struct {
			Type    string
			Role    string
			Content json.RawMessage
		} `json:"input"`
	}
	if err := json.Unmarshal([]byte(bodies[0]), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Instructions, "You are Claude Code.") ||
		!strings.Contains(got.Instructions, "env: working dir /home/me") {
		t.Fatalf("the leading system message was to join the instructions: %q", got.Instructions)
	}
	var reminded bool
	for i, m := range got.Input {
		switch {
		case m.Role == "system" || m.Role == "developer":
			t.Errorf("input[%d]: role %q went as it came; the ChatGPT backend behind the relay takes user, assistant and developer alone: %s", i, m.Role, bodies[0])
		case m.Role == "user" && strings.Contains(text(responsesParts(m.Content)), "<system-reminder>"):
			if !strings.Contains(text(responsesParts(m.Content)), "the user switched rooms") {
				t.Errorf("input[%d]: a reminder without the late system message: %s", i, bodies[0])
			}
			reminded = true
		}
	}
	if !reminded {
		t.Fatalf("the system message after a reply was to be told as a reminder: %s", bodies[0])
	}
}

// #7204: ZCode sends a Chat Completions request whose later tool rounds
// append role:"system" reminders. readChat previously hoisted every system
// and developer message into r.System, rewriting the cached top-level
// Anthropic system block on every turn. Leading system prompts stay in
// top-level system (byte-stable across turns), while mid-conversation
// reminders after a reply stay in place as <system-reminder>.
func TestChatToAnthropicKeepsCachedSystemPrefixOnLateReminder(t *testing.T) {
	srv, seen := relayOfEvery(t)
	fresh(t)
	if err := provider.Save(provider.Provider{
		ID: "anth-relay", Name: "Anthropic relay", Key: "k",
		Anthropic: srv.URL, Models: []string{"claude-fable-5.2"},
	}); err != nil {
		t.Fatal(err)
	}
	prefix := "Reply with OK. Synthetic leading prompt."
	reqA := `{"model":"anth-relay/claude-fable-5.2","stream":true,"max_completion_tokens":32,"messages":[` +
		`{"role":"system","content":"` + prefix + `"},` +
		`{"role":"user","content":"Reply with OK."}]}`
	reqB := `{"model":"anth-relay/claude-fable-5.2","stream":true,"max_completion_tokens":32,"messages":[` +
		`{"role":"system","content":"` + prefix + `"},` +
		`{"role":"user","content":"Reply with OK."},` +
		`{"role":"assistant","content":"OK"},` +
		`{"role":"user","content":"Reply with OK once more."},` +
		`{"role":"system","content":"Reminder for this turn: use a one-word reply."}]}`
	reqC := `{"model":"anth-relay/claude-fable-5.2","stream":true,"max_completion_tokens":32,"messages":[` +
		`{"role":"system","content":"` + prefix + `"},` +
		`{"role":"user","content":"Read README"},` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"call_1","content":"file contents"},` +
		`{"role":"system","content":"Tool-round reminder."}]}`

	for i, body := range []string{reqA, reqB, reqC} {
		if code, out := post(t, "/v1/chat/completions", body); code != 200 {
			t.Fatalf("req %d: %d %s", i, code, out)
		}
	}
	paths, bodies := seen()
	if len(paths) != 3 || paths[0] != "/v1/messages" || paths[1] != "/v1/messages" || paths[2] != "/v1/messages" {
		t.Fatalf("upstream paths %v, want three /v1/messages requests", paths)
	}
	type anthReq struct {
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string `json:"type"`
				Text      string `json:"text"`
				ToolUseID string `json:"tool_use_id"`
			} `json:"content"`
		} `json:"messages"`
	}
	var got [3]anthReq
	for i := range got {
		if err := json.Unmarshal([]byte(bodies[i]), &got[i]); err != nil {
			t.Fatalf("parse body %d: %v", i, err)
		}
	}
	// Leading cached system block must remain byte-for-byte identical across all three requests.
	if string(got[1].System) != string(got[0].System) {
		t.Errorf("reqB rewrote cached system prefix:\n reqA: %s\n reqB: %s", got[0].System, got[1].System)
	}
	if string(got[2].System) != string(got[0].System) {
		t.Errorf("reqC rewrote cached system prefix:\n reqA: %s\n reqC: %s", got[0].System, got[2].System)
	}
	// reqB keeps the late reminder in the trailing user message.
	lastB := got[1].Messages[len(got[1].Messages)-1]
	if lastB.Role != "user" {
		t.Fatalf("reqB last message role %q, want user", lastB.Role)
	}
	foundB := false
	for _, b := range lastB.Content {
		if b.Type == "text" && strings.Contains(b.Text, "<system-reminder>") && strings.Contains(b.Text, "Reminder for this turn: use a one-word reply.") {
			foundB = true
		}
	}
	if !foundB {
		t.Errorf("reqB lost late reminder in trailing message: %s", bodies[1])
	}
	// reqC keeps tool_result first and late reminder after it in the trailing user message.
	lastC := got[2].Messages[len(got[2].Messages)-1]
	if lastC.Role != "user" || len(lastC.Content) < 2 || lastC.Content[0].Type != "tool_result" || lastC.Content[0].ToolUseID != "call_1" ||
		!strings.Contains(lastC.Content[1].Text, "Tool-round reminder.") {
		t.Errorf("reqC did not keep tool_result followed by late reminder: %s", bodies[2])
	}
}
