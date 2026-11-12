package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

const geminiDeferredTool = "magpie_deferred_tool"
const geminiDeferredID = "__gdb1"

// geminiDeferred keeps only request-local capabilities. A name in a tool result
// is not permission to call it unless the current request also supplies it.
type geminiDeferred struct {
	tools  map[string]Tool
	loaded map[string]bool
	native map[string]bool
	forced string
}

func deferredForGemini(r *Request) *geminiDeferred {
	if slices.ContainsFunc(r.Tools, func(t Tool) bool { return t.Name == geminiDeferredTool }) {
		return nil
	}
	marked := hasGeminiDeferredHistory(r)
	if !marked && (!r.DeferredTools || !slices.ContainsFunc(r.Tools, func(t Tool) bool { return t.Name == "ToolSearch" })) {
		return nil
	}
	g := &geminiDeferred{tools: map[string]Tool{}, loaded: map[string]bool{}, native: map[string]bool{}}
	for _, t := range r.Tools {
		if t.Deferred {
			g.tools[t.Name] = t
		}
	}
	for _, m := range r.Messages {
		for _, p := range m.Parts {
			for _, n := range p.Loads {
				g.loaded[n] = true
			}
			if p.Kind == ToolCall && !strings.HasSuffix(p.ID, geminiDeferredID) {
				g.native[p.Name] = true // preserve calls made before the bridge was enabled
			}
		}
	}
	// Compaction can remove the load result. Preserve those declarations
	// natively rather than making an already available tool unusable.
	for name := range g.tools {
		if !g.loaded[name] {
			g.native[name] = true
		}
	}
	if n, ok := strings.CutPrefix(r.ToolChoice, "name:"); ok {
		if _, deferred := g.tools[n]; deferred && !g.native[n] {
			g.forced = n
		}
	}
	return g
}

func (g *geminiDeferred) declaration() Tool {
	name := map[string]any{"type": "string"}
	if g.forced != "" {
		name["enum"] = []string{g.forced}
	}
	schema, _ := marshalPlain(map[string]any{"type": "object", "properties": map[string]any{
		"name": name, "arguments": map[string]any{"type": "object"},
	}, "required": []string{"name", "arguments"}})
	return Tool{Name: geminiDeferredTool, Description: "Call a deferred tool loaded by ToolSearch. Use its exact name and an arguments object matching the schema in the search result. Do not use this for tools that are already declared directly.", Schema: schema}
}

// geminiDeferredRequest leaves declarations and the old conversation prefix
// unchanged as tools load. Only the result that loaded a tool gains its schema.
func geminiDeferredRequest(r *Request, model string) (*Request, error) {
	if strings.Contains(strings.ToLower(model), "claude") {
		return r, nil
	}
	if slices.ContainsFunc(r.Tools, func(t Tool) bool { return t.Name == geminiDeferredTool }) && (r.DeferredTools || hasGeminiDeferredHistory(r)) {
		return nil, fmt.Errorf("tool name %q is reserved for Gemini deferred tool translation", geminiDeferredTool)
	}
	g := deferredForGemini(r)
	if g == nil {
		return r, nil
	}
	out := *r
	out.Tools = make([]Tool, 0, len(r.Tools)+1)
	for _, t := range r.Tools {
		if !t.Deferred || g.native[t.Name] {
			out.Tools = append(out.Tools, t)
		}
	}
	out.Tools = append(out.Tools, g.declaration())
	if g.forced != "" {
		out.ToolChoice = "name:" + geminiDeferredTool
	}
	out.Messages = slices.Clone(r.Messages)
	for i, m := range r.Messages {
		out.Messages[i].Parts = slices.Clone(m.Parts)
		for j, p := range m.Parts {
			if p.Kind == ToolCall && strings.HasSuffix(p.ID, geminiDeferredID) {
				args, err := marshalPlain(map[string]any{"name": p.Name, "arguments": json.RawMessage(argsOf(p))})
				if err != nil {
					return nil, fmt.Errorf("invalid deferred tool history: %w", err)
				}
				p.Name, p.Args = geminiDeferredTool, args
			}
			if p.Kind == ToolResult {
				for _, name := range p.Loads {
					t, ok := g.tools[name]
					if !ok || g.native[name] {
						continue
					}
					schema, err := marshalPlain(map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.Schema})
					if err != nil {
						return nil, fmt.Errorf("invalid schema for deferred tool %q: %w", name, err)
					}
					p.Text += "\nCall this tool through " + geminiDeferredTool + " with arguments matching this definition: " + string(schema)
				}
			}
			out.Messages[i].Parts[j] = p
		}
	}
	return &out, nil
}

// decode restores the deferred tool's own name and arguments. Only the name is
// judged here: a tool not loaded or not allowed by this request is refused.
// Arguments that miss the tool's schema go to the client as written, as a
// native call's would, so the client's validation error reaches the model and
// it can call again; refusing them here would end the whole turn.
func (g *geminiDeferred) decode(args json.RawMessage) (string, json.RawMessage, error) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(args, &call); err != nil {
		return "", nil, fmt.Errorf("invalid deferred tool envelope")
	}
	_, ok := g.tools[call.Name]
	if !ok || !g.loaded[call.Name] || g.native[call.Name] || (g.forced != "" && g.forced != call.Name) {
		return "", nil, fmt.Errorf("deferred tool is not loaded or allowed by this request")
	}
	raw := bytes.TrimSpace(call.Arguments)
	if len(raw) == 0 || raw[0] != '{' {
		return "", nil, fmt.Errorf("deferred tool arguments must be an object")
	}
	return call.Name, call.Arguments, nil
}

// Validate all bridge calls in a chunk before exposing any of that chunk's calls.
func (g *geminiDeferred) parts(parts []gPart) ([]gPart, error) {
	var out []gPart
	for i, p := range parts {
		if p.FunctionCall == nil || toolOfCall(p.FunctionCall.Name) != geminiDeferredTool {
			continue
		}
		call := *p.FunctionCall
		var err error
		call.Name, call.Args, err = g.decode(call.Args)
		if err != nil {
			return nil, err
		}
		if call.ID == "" {
			call.ID = "call_" + newID()
		}
		call.ID += geminiDeferredID
		if out == nil {
			out = slices.Clone(parts)
		}
		out[i].FunctionCall = &call
	}
	if out == nil {
		return parts, nil
	}
	return out, nil
}

func hasGeminiDeferredHistory(r *Request) bool {
	for _, m := range r.Messages {
		for _, p := range m.Parts {
			if p.Kind == ToolCall && strings.HasSuffix(p.ID, geminiDeferredID) {
				return true
			}
		}
	}
	return false
}
