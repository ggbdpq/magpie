package gateway

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

func deferredRequest(t *testing.T, body string) *Request {
	t.Helper()
	r, err := parseRequest(provider.Anthropic, []byte(body), false, false)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestGeminiDeferredPrefix(t *testing.T) {
	markedA := strings.ReplaceAll(tsCallA, "toolu_02", "toolu_02"+geminiDeferredID)
	bodies := []string{
		tsRequest([]string{tsTools0, tsPlaceholder}, tsTurn1),
		tsRequest([]string{tsTools0, tsReadA, tsPlaceholder}, tsTurn1, tsSearchA),
		tsRequest([]string{tsTools0, tsReadA, tsReadB, tsPlaceholder}, tsTurn1, tsSearchA, markedA, tsSearchB),
	}
	var prev gjson.Result
	for i, body := range bodies {
		r := deferredRequest(t, body)
		before, _ := json.Marshal(r)
		b, err := buildGemini(r, "gemini-4-carbon")
		if err != nil {
			t.Fatal(err)
		}
		after, _ := json.Marshal(r)
		if string(before) != string(after) {
			t.Fatal("builder mutated its input")
		}
		v := gjson.ParseBytes(b)
		if i > 0 {
			if v.Get("tools").Raw != prev.Get("tools").Raw || v.Get("systemInstruction").Raw != prev.Get("systemInstruction").Raw {
				t.Fatal("loaded tool changed declarations or system")
			}
			old, now := prev.Get("contents").Array(), v.Get("contents").Array()
			for j := range old {
				if old[j].Raw != now[j].Raw {
					t.Fatalf("request %d changed old content %d", i, j)
				}
			}
		}
		if got := len(v.Get("tools.0.functionDeclarations").Array()); got != 3 {
			t.Fatalf("declarations=%d, want Bash,ToolSearch,bridge", got)
		}
		if i == 2 {
			if !strings.Contains(v.Get("contents").Raw, "Read B") {
				t.Fatal("loaded schema absent")
			}
			var call, result bool
			for _, c := range v.Get("contents").Array() {
				for _, p := range c.Get("parts").Array() {
					if p.Get("functionCall.name").String() == geminiDeferredTool {
						call = p.Get("functionCall.args.name").String() == "mcp__probe__read_a" && p.Get("functionCall.args.arguments.path").String() == "a"
					}
					if p.Get("functionResponse.name").String() == geminiDeferredTool {
						result = true
					}
				}
			}
			if !call || !result {
				t.Fatal("marked history wasn't reconstructed")
			}
		}
		prev = v
	}
}

func TestGeminiDeferredDecodeAndSignature(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	body := tsRequest([]string{tsTools0, tsReadA, tsPlaceholder}, tsTurn1, tsSearchA)
	r := deferredRequest(t, body)
	d := &codeAssistDecoder{deferred: deferredForGemini(r)}
	var evs []Event
	chunk := `{"candidates":[{"content":{"parts":[{"functionCall":{"id":"call-a","name":"magpie_deferred_tool","args":{"name":"mcp__probe__read_a","arguments":{"path":"a"}}},"thoughtSignature":"signed-a"}]},"finishReason":"STOP"}]}`
	if err := d.decode(chunk, func(e Event) { evs = append(evs, e) }); err != nil {
		t.Fatal(err)
	}
	var call Event
	var args string
	for _, e := range evs {
		if e.Kind == KError {
			t.Fatalf("unexpected failure: %+v", e)
		}
		if e.Kind == KToolStart {
			call = e
		}
		if e.Kind == KToolArgs {
			args = e.Text
		}
	}
	id, sig := unsignedID(call.ID)
	if call.Name != "mcp__probe__read_a" || id != "call-a"+geminiDeferredID || sig != "signed-a" || args != `{"path":"a"}` {
		t.Fatalf("wrong restored call: %+v %s", call, args)
	}
	a, _ := json.Marshal(map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": json.RawMessage(args)}}})
	b, _ := json.Marshal(map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": call.ID, "content": "OK"}}})
	next := deferredRequest(t, tsRequest([]string{tsTools0, tsReadA, tsPlaceholder}, tsTurn1, tsSearchA, string(a), string(b)))
	out, err := buildGemini(next, "gemini-4-carbon")
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, c := range gjson.GetBytes(out, "contents").Array() {
		for _, p := range c.Get("parts").Array() {
			if p.Get("functionCall.name").String() == geminiDeferredTool {
				seen = true
				if p.Get("thoughtSignature").String() != "signed-a" || p.Get("functionCall.args.name").String() != call.Name {
					t.Fatal("signature or call changed on replay")
				}
			}
		}
	}
	if !seen {
		t.Fatal("no bridge call on replay")
	}
}

func TestGeminiDeferredRejectsInvalidCalls(t *testing.T) {
	r := deferredRequest(t, tsRequest([]string{tsTools0, tsReadA, tsReadB, tsPlaceholder}, tsTurn1, tsSearchA))
	for _, args := range []string{
		`{"name":"missing","arguments":{}}`,
		`{"name":"mcp__probe__read_b","arguments":{"path":"b"}}`,
		`{"name":"mcp__probe__read_a","arguments":null}`,
		`{"name":"mcp__probe__read_a","arguments":[]}`,
	} {
		t.Run(args, func(t *testing.T) {
			d := &codeAssistDecoder{deferred: deferredForGemini(r)}
			var events []Event
			chunk := fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"magpie_deferred_tool","args":%s}}]},"finishReason":"STOP"}]}`, args)
			d.decode(chunk, func(e Event) { events = append(events, e) })
			found := false
			for _, e := range events {
				if e.Kind == KToolStart || e.Kind == KStop {
					t.Fatal("invalid call escaped validation")
				}
				if e.Kind == KError {
					found = true
				}
			}
			if !found {
				t.Fatal("missing validation error")
			}
		})
	}
}

// Arguments that miss a loaded tool's schema reach the client as a call, as a
// native call's would: the client's validation error goes back to the model,
// which can call again. Refusing them in the gateway ended the turn instead.
func TestGeminiDeferredLeavesArgumentsToTheClient(t *testing.T) {
	r := deferredRequest(t, tsRequest([]string{tsTools0, tsReadA, tsReadB, tsPlaceholder}, tsTurn1, tsSearchA))
	d := &codeAssistDecoder{deferred: deferredForGemini(r)}
	var events []Event
	chunk := `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"magpie_deferred_tool","args":{"name":"mcp__probe__read_a","arguments":{"path":3}}}}]},"finishReason":"STOP"}]}`
	if err := d.decode(chunk, func(e Event) { events = append(events, e) }); err != nil {
		t.Fatal(err)
	}
	var name, args string
	for _, e := range events {
		switch e.Kind {
		case KError:
			t.Fatalf("a schema mismatch ended the turn: %+v", e)
		case KToolStart:
			name = e.Name
		case KToolArgs:
			args = e.Text
		}
	}
	if name != "mcp__probe__read_a" || args != `{"path":3}` {
		t.Fatalf("call not handed on as written: %q %s", name, args)
	}
}

func TestGeminiDeferredCompatibility(t *testing.T) {
	r := deferredRequest(t, tsRequest([]string{tsTools0, tsReadA, tsPlaceholder}, tsTurn1, tsSearchA, tsCallA))
	built, err := geminiDeferredRequest(r, "gemini-4-carbon")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(built.Tools)
	if !strings.Contains(string(raw), "mcp__probe__read_a") {
		t.Fatal("pre-bridge native tool removed")
	}
	claude, err := geminiDeferredRequest(r, "claude-sonnet-4-5")
	if err != nil || !reflect.DeepEqual(claude, r) {
		t.Fatal("Claude route changed")
	}
	r.DeferredTools = false
	same, err := geminiDeferredRequest(r, "gemini-4-carbon")
	if err != nil || same != r {
		t.Fatal("ordinary request changed")
	}
	r.DeferredTools = true
	r.Tools = append(r.Tools, Tool{Name: geminiDeferredTool})
	if _, err := geminiDeferredRequest(r, "gemini-4-carbon"); err == nil {
		t.Fatal("reserved-name collision accepted")
	}
}

func TestGeminiDeferredTextCall(t *testing.T) {
	r := deferredRequest(t, tsRequest([]string{tsTools0, tsReadA, tsPlaceholder}, tsTurn1, tsSearchA))
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			name := "mcp__probe__read_a"
			if !valid {
				name = "unavailable"
			}
			text := `call:default_api:magpie_deferred_tool{"name":"` + name + `","arguments":{"path":"a"}}`
			d := &codeAssistDecoder{deferred: deferredForGemini(r)}
			var events []Event
			for _, piece := range []string{text[:18], text[18:]} {
				raw, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": piece}}}}}})
				d.decode(string(raw), func(e Event) { events = append(events, e) })
			}
			d.decode(`{"candidates":[{"finishReason":"STOP"}]}`, func(e Event) { events = append(events, e) })
			calls, errors := 0, 0
			for _, e := range events {
				if e.Kind == KError {
					errors++
				}
				if e.Kind == KToolStart {
					calls++
					if e.Name != "mcp__probe__read_a" || !strings.HasSuffix(e.ID, geminiDeferredID) {
						t.Fatalf("text call not restored: %+v", e)
					}
				}
			}
			if valid && (calls != 1 || errors != 0) || !valid && (calls != 0 || errors != 1) {
				t.Fatalf("valid=%v calls=%d errors=%d", valid, calls, errors)
			}
		})
	}
}

func TestGeminiDeferredChoiceAndCompaction(t *testing.T) {
	r := deferredRequest(t, tsRequest([]string{tsTools0, tsReadA, tsReadB, tsPlaceholder}, tsTurn1, tsSearchA, tsSearchB))
	r.ToolChoice = "name:mcp__probe__read_a"
	out, err := buildGemini(r, "gemini-4-carbon")
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(out, "toolConfig.functionCallingConfig.allowedFunctionNames.0").String(); got != geminiDeferredTool {
		t.Fatalf("forced tool lost: %s", got)
	}
	if got := gjson.GetBytes(out, "tools.0.functionDeclarations.2.parametersJsonSchema.properties.name.enum.0").String(); got != "mcp__probe__read_a" {
		t.Fatalf("target constraint lost: %s", got)
	}
	if _, _, err := deferredForGemini(r).decode(json.RawMessage(`{"name":"mcp__probe__read_b","arguments":{"path":"b"}}`)); err == nil {
		t.Fatal("wrong forced target allowed")
	}
	r.ToolChoice = "none"
	out, err = buildGemini(r, "gemini-4-carbon")
	if err != nil || gjson.GetBytes(out, "tools").Exists() {
		t.Fatal("none enabled tools")
	}
	r = deferredRequest(t, tsRequest([]string{tsTools0, tsReadA, tsPlaceholder}, `{"role":"user","content":"compacted summary"}`))
	out, err = buildGemini(r, "gemini-4-carbon")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gjson.GetBytes(out, "tools").Raw, `mcp__probe__read_a`) {
		t.Fatal("compaction lost an available tool")
	}
	r = deferredRequest(t, tsRequest([]string{tsReadA, tsPlaceholder}, tsTurn1))
	same, err := geminiDeferredRequest(r, "gemini-4-carbon")
	if err != nil || same != r {
		t.Fatal("request without ToolSearch changed")
	}
}

func TestGeminiDeferredMarkedHistoryWithoutSearch(t *testing.T) {
	marked := strings.ReplaceAll(tsCallA, "toolu_02", "toolu_02"+geminiDeferredID)
	r := deferredRequest(t, tsRequest([]string{tsReadA}, tsTurn1, marked))
	r.DeferredTools = false
	out, err := buildGemini(r, "gemini-4-carbon")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gjson.GetBytes(out, "contents").Raw, `"name":"magpie_deferred_tool"`) {
		t.Fatal("bridge history lost when search was disabled")
	}
	r.Tools = append(r.Tools, Tool{Name: geminiDeferredTool})
	if _, err := buildGemini(r, "gemini-4-carbon"); err == nil {
		t.Fatal("history name collision accepted")
	}
}

func TestGeminiDeferredParallelValidation(t *testing.T) {
	r := deferredRequest(t, tsRequest([]string{tsTools0, tsReadA, tsReadB, tsPlaceholder}, tsTurn1, tsSearchA, tsSearchB))
	for _, valid := range []bool{true, false} {
		second := "mcp__probe__read_b"
		if !valid {
			second = "unavailable"
		}
		chunk := fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"magpie_deferred_tool","args":{"name":"mcp__probe__read_a","arguments":{"path":"a"}}}},{"functionCall":{"name":"magpie_deferred_tool","args":{"name":%q,"arguments":{"path":"b"}}}}]},"finishReason":"STOP"}]}`, second)
		d := &codeAssistDecoder{deferred: deferredForGemini(r)}
		calls, errors := 0, 0
		d.decode(chunk, func(e Event) {
			if e.Kind == KToolStart {
				calls++
			}
			if e.Kind == KError {
				errors++
			}
		})
		if valid && (calls != 2 || errors != 0) || !valid && (calls != 0 || errors != 1) {
			t.Fatalf("valid=%v calls=%d errors=%d", valid, calls, errors)
		}
	}
	g := deferredForGemini(r)
	tool := g.tools["mcp__probe__read_a"]
	tool.Schema = json.RawMessage(`{"$ref":"https://example.invalid/schema.json"}`)
	g.tools[tool.Name] = tool
	// the gateway fetches nothing and judges no schema: a tool whose schema
	// points elsewhere stays callable, its arguments the client's to check
	if _, _, err := g.decode(json.RawMessage(`{"name":"mcp__probe__read_a","arguments":{"path":"a"}}`)); err != nil {
		t.Fatalf("a tool with an external schema reference became uncallable: %v", err)
	}
}

func TestGeminiDeferredImageAndErrorHistory(t *testing.T) {
	marked := strings.ReplaceAll(tsCallA, "toolu_02", "toolu_02"+geminiDeferredID)
	r := deferredRequest(t, tsRequest([]string{tsTools0, tsReadA, tsPlaceholder}, tsTurn1, tsSearchA, marked))
	last := &r.Messages[len(r.Messages)-1].Parts[0]
	last.IsError = true
	last.Images = []Part{{Kind: Image, MediaType: "image/png", Data: "AQ=="}}
	out, err := buildGemini(r, "gemini-4-carbon")
	if err != nil {
		t.Fatal(err)
	}
	image, result := false, false
	for _, c := range gjson.GetBytes(out, "contents").Array() {
		for _, p := range c.Get("parts").Array() {
			image = image || p.Get("inlineData.data").String() == "AQ=="
			result = result || p.Get("functionResponse.name").String() == geminiDeferredTool && p.Get("functionResponse.response.error").String() == "A"
		}
	}
	if !image || !result {
		t.Fatal("tool image or error was lost on bridge replay")
	}
}
