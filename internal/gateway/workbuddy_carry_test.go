package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// WorkBuddy's signed reasoning across a tool loop (#8009). Its Chat
// replies carry the step's reasoning metadata beside the reasoning text,
// in extra_fields (its reasoning_detail, with the content signatures);
// the next request's assistant turn is to give it back, as CodeBuddy's
// sessions keep it. A Messages client keeps no such field, so before the
// carry each step of its tool loop reached WorkBuddy with none of it.
func TestWorkBuddyCarriesSignedReasoningToTheNextStep(t *testing.T) {
	detail := `{"reasoning_detail":[{"text":"Check the clock.","content_signature":"sig-a"},{"text":"Then answer.","content_signature":"sig-b"}]}`
	var bodies []string
	pid := besideFake(t, "workbuddy", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []string{
			`{"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"Check the clock. Then answer.","extra_fields":` + detail + `},"finish_reason":null}]}`,
			`{"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_time","arguments":"{}"}}]},"finish_reason":null}]}`,
			`{"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		} {
			fmt.Fprint(w, "data: "+c+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	srv := New()
	const tools = `"tools":[{"name":"get_time","input_schema":{"type":"object","properties":{}}}]`
	code, body := postTo(t, srv, "/v1/messages", `{"model":"`+pid+`/fake-1","max_tokens":200,"stream":true,`+tools+`,"messages":[{"role":"user","content":"What time is it?"}]}`)
	if code != 200 {
		t.Fatalf("%d first turn: %s", code, body)
	}
	marked := regexp.MustCompile(`"id":"(call_1[^"]*)"`)
	m := marked.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no tool_use id in the reply: %s", body)
	}
	// the client echoes the id it was given, with the tool's result
	code, body = postTo(t, srv, "/v1/messages", `{"model":"`+pid+`/fake-1","max_tokens":200,"stream":true,`+tools+`,"messages":[
		{"role":"user","content":"What time is it?"},
		{"role":"assistant","content":[{"type":"tool_use","id":"`+m[1]+`","name":"get_time","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"`+m[1]+`","content":"12:00"}]}]}`)
	if code != 200 {
		t.Fatalf("%d second turn: %s", code, body)
	}
	if len(bodies) != 2 {
		t.Fatalf("%d upstream calls, want 2", len(bodies))
	}
	ms := sentMessages(t, []byte(bodies[1]))
	var calls, result []any
	var am map[string]any
	for _, raw := range ms {
		var mm map[string]any
		if json.Unmarshal(raw, &mm) != nil {
			continue
		}
		switch mm["role"] {
		case "assistant":
			am = mm
			calls, _ = mm["tool_calls"].([]any)
		case "tool":
			result = append(result, mm)
		}
	}
	if am == nil || len(calls) != 1 || len(result) != 1 {
		t.Fatalf("second request has %d calls, %d results: %s", len(calls), len(result), bodies[1])
	}
	call := calls[0].(map[string]any)
	if call["id"] != "call_1" {
		t.Errorf("the call goes as its own id, got %v", call["id"])
	}
	if got := result[0].(map[string]any)["tool_call_id"]; got != "call_1" {
		t.Errorf("the result answers the call's own id, got %v", got)
	}
	ef, ok := am["extra_fields"]
	if !ok {
		t.Fatalf("the assistant turn carries no extra_fields: %s", bodies[1])
	}
	efBytes, err := json.Marshal(ef)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, efBytes, detail)
}

// A payload goes back only to the account and model that gave it: a step
// routed elsewhere goes as before (the reporter's negative check). A
// vendor's id is in the who, so another provider is covered by it too.
func TestWorkBuddyCarryStaysWithItsVendor(t *testing.T) {
	const detail = `{"reasoning_detail":[{"text":"s","content_signature":"x"}]}`
	wrap := wbWrap(detail, "wb1@team", "fake-1")
	if wrap == "" {
		t.Fatal("no wrapper")
	}
	payload := wbReplay(wrap, "wb1@team", "fake-1")
	if !strings.Contains(payload, "content_signature") {
		t.Fatalf("the sealing vendor lost its payload: %q", payload)
	}
	for name, scope := range map[string][2]string{
		"another account": {"wb2@team", "fake-1"},
		"another model":   {"wb1@team", "fake-2"},
		"another vendor":  {"wb2@team", "fake-2"},
	} {
		if wbReplay(wrap, scope[0], scope[1]) != "" {
			t.Errorf("%s was forwarded the payload", name)
		}
	}
}
