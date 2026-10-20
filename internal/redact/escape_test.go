package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

// A tool call's arguments ride in the request as a string that holds JSON
// of its own: the newline in the command in it is the two characters \
// and n, and what follows reads with the n for the start of a word. The
// EMAIL rule took n@mcp.tool for an address, and the placeholder went in
// where the n had been, the backslash left before it — \{{EMAIL_…}}, and
// the arguments no longer parsed. The vendor turned the whole request away
// with 400 "invalid tool call arguments", the history replayed, and the
// conversation stayed broken (#3022).
func TestMaskJSONKeepsArgumentsJSON(t *testing.T) {
	command := "print('before')\n@mcp.tool()\ndef demo():\n    return 1"
	inner, _ := json.Marshal(map[string]string{"command": command})
	args, _ := json.Marshal(string(inner)) // the string that carries the arguments
	body := `{"messages":[` +
		`{"role":"user","content":"write mara@quill.dev about the demo"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function",` +
		`"function":{"name":"pwsh","arguments":` + string(args) + `}}]},` +
		`{"role":"tool","tool_call_id":"call_1","content":"done"}]}`
	out, n := MaskJSON([]byte(body), Options{Personal: true})
	if !json.Valid(out) {
		t.Fatalf("body no longer JSON: %s", out)
	}
	if n != 1 { // the real email in the user's words, not the decorator
		t.Fatalf("%d masked, want 1: %s", n, out)
	}
	var v struct {
		Messages []struct {
			ToolCalls []struct {
				Function struct {
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("body: %v", err)
	}
	arguments := v.Messages[1].ToolCalls[0].Function.Arguments
	if !json.Valid([]byte(arguments)) {
		t.Fatalf("arguments no longer JSON: %s", arguments)
	}
	var got struct {
		Command string
	}
	if err := json.Unmarshal([]byte(arguments), &got); err != nil {
		t.Fatalf("arguments: %v", err)
	}
	if got.Command != command {
		t.Fatalf("command changed: %q", got.Command)
	}
	// and the email the same request really has still goes
	if !strings.Contains(string(out), "{{EMAIL_") || strings.Contains(unmarked(string(out)), "mara@quill.dev") {
		t.Fatalf("the address went through: %s", out)
	}

	// a string that is no JSON of its own is read after one unescaping, so
	// a Windows path in it goes as before
	body2 := `{"cwd":"C:\\Users\\rkato\\proj"}`
	out2, n2 := MaskJSON([]byte(body2), Options{Personal: true, Kinds: map[string]bool{"home": true}})
	if n2 != 1 || !strings.Contains(string(out2), `C:\\Users\\{{USERNAME_`) {
		t.Fatalf("%d: %s", n2, out2)
	}
}
