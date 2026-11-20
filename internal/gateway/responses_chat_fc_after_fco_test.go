package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// A Responses function_call that follows a function_call_output, or that
// has no assistant text before it, or that follows an assistant message
// whose text is empty, is one assistant turn that calls a tool and says
// nothing. Translated to Chat it went out as
// {"role":"assistant","tool_calls":[…]} with content left out, and a
// strict Chat upstream refused the request, 400 "Invalid message at index
// N: missing role or content", on every later turn (#7307). The message
// carries content "" beside its tool_calls. A turn that said something
// keeps that text. Parallel calls in one step stay on the message that
// has the text. A reasoning item alone, and a function_call_output with
// no call, stay as they were. DeepSeek still gets reasoning_content.
func TestResponsesChatFunctionCallAfterOutput(t *testing.T) {
	const (
		model = "m"
		host  = "api.example.com"
	)
	user := `{"content":"go","role":"user"}`
	step1 := assistantCalls("step 1", execCall(1))
	step1Two := assistantCalls("step 1", execCall(1), execCall(2))
	step2 := assistantCalls("step 2", execCall(2))
	step2Call3 := assistantCalls("step 2", execCall(3))
	blank := assistantCalls("", execCall(1))
	blank2 := assistantCalls("", execCall(2))
	cases := []struct {
		name, model, host, input string
		want                     []string
	}{
		{
			name: "text then one call", model: model, host: host,
			input: joinItems(msg("user", "go"), msg("assistant", "step 1"), fc(1), fco(1)),
			want:  []string{user, step1, toolHi(1)},
		},
		{
			name: "parallel calls", model: model, host: host,
			input: joinItems(msg("user", "go"), msg("assistant", "step 1"), fc(1), fc(2), fco(1), fco(2)),
			want:  []string{user, step1Two, toolHi(1), toolHi(2)},
		},
		{
			name: "call after output", model: model, host: host,
			input: joinItems(msg("user", "go"), msg("assistant", "step 1"), fc(1), fco(1), fc(2), fco(2)),
			want:  []string{user, step1, toolHi(1), blank2, toolHi(2)},
		},
		{
			name: "text between calls", model: model, host: host,
			input: joinItems(msg("user", "go"), msg("assistant", "step 1"), fc(1), fco(1), msg("assistant", "step 2"), fc(2), fco(2)),
			want:  []string{user, step1, toolHi(1), step2, toolHi(2)},
		},
		{
			name: "parallel then text", model: model, host: host,
			input: joinItems(msg("user", "go"), msg("assistant", "step 1"), fc(1), fc(2), fco(1), fco(2), msg("assistant", "step 2"), fc(3), fco(3)),
			want:  []string{user, step1Two, toolHi(1), toolHi(2), step2Call3, toolHi(3)},
		},
		{
			name: "call with no text", model: model, host: host,
			input: joinItems(msg("user", "go"), fc(1), fco(1)),
			want:  []string{user, blank, toolHi(1)},
		},
		{
			name: "empty text then call", model: model, host: host,
			input: joinItems(msg("user", "go"), msg("assistant", ""), fc(1)),
			want:  []string{user, blank, interruptedToolMessage("call_1")},
		},
		{
			name: "reasoning then call", model: model, host: host,
			input: joinItems(msg("user", "go"), reasoningInput("think"), fc(1), fco(1)),
			want:  []string{user, blank, toolHi(1)},
		},
		{
			name: "reasoning only", model: model, host: host,
			input: reasoningInput("think"),
			want:  nil,
		},
		{
			name: "output only", model: model, host: host,
			input: fco(1),
			want:  []string{`{"content":"hi","role":"user"}`},
		},
		{
			name: "custom tool call with no text", model: model, host: host,
			input: joinItems(msg("user", "go"), customCall("call_c", "apply_patch", "print(1)"), customOut("call_c", "hi")),
			want:  []string{user, assistantCalls("", customCallJSON("call_c", "apply_patch", "print(1)")), toolMessage("call_c", "hi")},
		},
		{
			name: "deepseek text keeps reasoning", model: "deepseek-chat", host: "api.deepseek.com",
			input: joinItems(msg("user", "go"), msg("assistant", "step 1"), fc(1), fco(1)),
			want:  []string{user, assistantReason("step 1", " ", execCall(1)), toolHi(1)},
		},
		{
			name: "deepseek call after output", model: "deepseek-chat", host: "api.deepseek.com",
			input: joinItems(msg("user", "go"), msg("assistant", "step 1"), fc(1), fco(1), fc(2), fco(2)),
			want: []string{
				user,
				assistantReason("step 1", " ", execCall(1)),
				toolHi(1),
				assistantReason("", " ", execCall(2)),
				toolHi(2),
			},
		},
		{
			name: "deepseek reasoning then call", model: "deepseek-chat", host: "api.deepseek.com",
			input: joinItems(msg("user", "go"), reasoningInput("think"), fc(1), fco(1)),
			want:  []string{user, assistantReason("", "think", execCall(1)), toolHi(1)},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := `{"model":"` + c.model + `","stream":false,"max_output_tokens":16,"input":[` + c.input + `]}`
			r, err := parseResponses([]byte(body))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := chatMessageBytes(t, buildChat(r, c.model, c.host, false))
			if len(got) != len(c.want) {
				t.Fatalf("got %d messages, want %d:\n%s", len(got), len(c.want), strings.Join(got, "\n"))
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("message %d\n got %s\nwant %s", i, got[i], c.want[i])
				}
			}
		})
	}
}

func chatMessageBytes(t *testing.T, body []byte) []string {
	t.Helper()
	var q struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &q); err != nil {
		t.Fatalf("body: %v\n%s", err, body)
	}
	out := make([]string, len(q.Messages))
	for i, m := range q.Messages {
		out[i] = string(m)
	}
	return out
}

func joinItems(items ...string) string { return strings.Join(items, ",") }

func msg(role, text string) string {
	kind := "input_text"
	if role == "assistant" {
		kind = "output_text"
	}
	quoted, _ := json.Marshal(text)
	return `{"type":"message","role":"` + role + `","content":[{"type":"` + kind + `","text":` + string(quoted) + `}]}`
}

func fc(n int) string {
	return fmt.Sprintf(`{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"echo hi\"}","call_id":"call_%d"}`, n)
}

func fco(n int) string {
	return fmt.Sprintf(`{"type":"function_call_output","call_id":"call_%d","output":"hi"}`, n)
}

func reasoningInput(text string) string {
	quoted, _ := json.Marshal(text)
	return `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":` + string(quoted) + `}]}`
}

func customCall(id, name, input string) string {
	return `{"type":"custom_tool_call","name":"` + name + `","input":"` + input + `","call_id":"` + id + `"}`
}

func customOut(id, output string) string {
	return `{"type":"custom_tool_call_output","call_id":"` + id + `","output":"` + output + `"}`
}

func execCall(n int) string {
	return `{"function":{"arguments":"{\"cmd\":\"echo hi\"}","name":"exec_command"},"id":"call_` + fmt.Sprint(n) + `","type":"function"}`
}

func customCallJSON(id, name, input string) string {
	args, _ := json.Marshal(map[string]string{"input": input})
	quoted, _ := json.Marshal(string(args))
	return `{"function":{"arguments":` + string(quoted) + `,"name":"` + name + `"},"id":"` + id + `","type":"function"}`
}

func assistantCalls(content string, calls ...string) string {
	quoted, _ := json.Marshal(content)
	return `{"content":` + string(quoted) + `,"role":"assistant","tool_calls":[` + strings.Join(calls, ",") + `]}`
}

func assistantReason(content, reason string, calls ...string) string {
	c, _ := json.Marshal(content)
	r, _ := json.Marshal(reason)
	return `{"content":` + string(c) + `,"reasoning_content":` + string(r) + `,"role":"assistant","tool_calls":[` + strings.Join(calls, ",") + `]}`
}

func toolHi(n int) string { return toolMessage(fmt.Sprintf("call_%d", n), "hi") }

func toolMessage(id, content string) string {
	quoted, _ := json.Marshal(content)
	return `{"content":` + string(quoted) + `,"role":"tool","tool_call_id":"` + id + `"}`
}

func interruptedToolMessage(id string) string {
	return toolMessage(id, "[The result of this tool call is unavailable: the turn was interrupted.]")
}
