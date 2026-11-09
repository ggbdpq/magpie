package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// #5822: Codex's cua_repl js (the in-app browser) has an input schema with
// a oneOf at its root, and bridgeTools handed it to the bridge's Claude
// Code unchanged. Claude Code drops every MCP tool whose schema has an
// anyOf/oneOf/allOf at the root ("Skipping tool: its input schema uses
// top-level oneOf, which the Anthropic API does not accept"), so the run's
// model never saw the tool and could not drive the browser, while its
// sibling js_reset, whose root is a plain object, arrived. The Anthropic
// path has folded root unions all along (objectSchema, #646); the bridge
// now folds there too. A schema with no root union goes as it came.
func TestClaudeBridgeFoldsToolRootUnions(t *testing.T) {
	js := `{"type":"object","properties":{"code":{"type":"string","description":"JavaScript to run"},"completion_summary":{"type":"string","description":"What the code did"},"timeout_ms":{"type":"number","description":"Timeout"},"title":{"type":"string","description":"Short title"}},"additionalProperties":false,"oneOf":[{"type":"object","properties":{"code":{"type":"string"},"timeout_ms":{"type":"number"},"title":{"type":"string"}},"required":["code"],"additionalProperties":false},{"type":"object","properties":{"completion_summary":{"type":"string"}},"required":["completion_summary"],"additionalProperties":false}]}`
	jsReset := `{"type":"object","properties":{},"additionalProperties":false}`
	body := `{"model":"claude-opus-5-5","stream":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"open example.com in the browser"}]}],"tools":[{"type":"namespace","name":"mcp__cua_repl","tools":[` +
		`{"type":"function","name":"js","description":"Evaluate JavaScript in the page","parameters":` + js + `},` +
		`{"type":"function","name":"js_reset","description":"Reset the REPL","parameters":` + jsReset + `}]}]}`
	req, err := parseResponses([]byte(body))
	if err != nil {
		t.Fatalf("parseResponses: %v", err)
	}
	if len(req.Tools) != 2 || req.Tools[0].Name != "mcp__cua_repl__js" || req.Tools[1].Name != "mcp__cua_repl__js_reset" {
		t.Fatalf("parsed tools %v", req.Tools)
	}
	offered := map[string]string{}
	for _, bt := range bridgeTools(req) {
		offered[bt.Name] = string(bt.InputSchema)
	}
	if len(offered) != 2 {
		t.Fatalf("offered %d tools, want 2: %q", len(offered), offered)
	}
	got, ok := offered["mcp__cua_repl__js"]
	if !ok {
		t.Fatalf("mcp__cua_repl__js was never offered: %q", offered)
	}
	if strings.Contains(got, "oneOf") {
		t.Errorf("cua_repl js is offered with oneOf at its root, which Claude Code drops: %s", got)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("offered schema: %v", err)
	}
	if m["type"] != "object" {
		t.Errorf("folded root is %v, want object", m["type"])
	}
	props, _ := m["properties"].(map[string]any)
	for _, f := range []string{"code", "completion_summary", "timeout_ms", "title"} {
		if _, has := props[f]; !has {
			t.Errorf("folded schema lost the %q field: %s", f, got)
		}
	}
	if _, has := m["required"]; has {
		t.Errorf("folded schema keeps a root required, though no field is one everywhere: %s", got)
	}
	if got, want := offered["mcp__cua_repl__js_reset"], jsReset; got != want {
		t.Errorf("js_reset's schema was rewritten: %s, want as it came %s", got, want)
	}
}
