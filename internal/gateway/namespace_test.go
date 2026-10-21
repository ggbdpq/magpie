package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Codex offers some tools inside a namespace — spawn_agent is
// collaboration's — and names the namespace beside the tool in its calls.
const namespacedTools = `"tools":[
	{"type":"function","name":"exec_command","parameters":{"type":"object"}},
	{"type":"namespace","name":"collaboration","description":"Tools in the collaboration namespace.","tools":[
		{"type":"function","name":"spawn_agent","description":"Spawns an agent.","parameters":{"type":"object","properties":{"message":{"type":"string"}}}},
		{"type":"custom","name":"freeform","description":"no JSON schema"}
	]}
]`

func TestNamespacedToolsOffered(t *testing.T) {
	r, err := parseResponses([]byte(`{"model":"m","input":"hi",` + namespacedTools + `}`))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range r.Tools {
		names = append(names, tl.Name)
	}
	// a namespace tool no other tool claims by its own name goes to the
	// model under it (#1448), and still comes back by its namespace
	if got := strings.Join(names, ","); got != "exec_command,spawn_agent,freeform" {
		t.Fatalf("tools = %s", got)
	}
	if r.Tools[1].Description != "Spawns an agent." || !strings.Contains(string(r.Tools[1].Schema), "message") {
		t.Fatalf("spawn_agent lost its description or schema: %+v", r.Tools[1])
	}
	if q := r.Namespaced["spawn_agent"]; q != (nsTool{Namespace: "collaboration", Name: "spawn_agent"}) {
		t.Fatalf("namespaced = %+v", r.Namespaced)
	}
	if !r.Namespaced["freeform"].Custom {
		t.Fatalf("freeform not custom: %+v", r.Namespaced)
	}
	if _, ok := r.Namespaced["exec_command"]; ok {
		t.Fatal("a top-level tool was put in a namespace")
	}
}

func TestNamespacedBareNameClaimed(t *testing.T) {
	// a name a top-level tool has, or that two namespaces share, keeps the
	// flat form: one name, one tool (#1448)
	body := `{"model":"m","input":"hi","tools":[
		{"type":"function","name":"read_file","parameters":{"type":"object"}},
		{"type":"namespace","name":"muse","tools":[
			{"type":"function","name":"read_file","parameters":{"type":"object"}},
			{"type":"function","name":"bash","parameters":{"type":"object"}},
			{"type":"function","name":"list_dir","parameters":{"type":"object"}}]},
		{"type":"namespace","name":"other","tools":[
			{"type":"function","name":"bash","parameters":{"type":"object"}}]}],
		"input":[
		{"type":"message","role":"user","content":"go"},
		{"type":"function_call","call_id":"c1","name":"list_dir","namespace":"muse","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"a.txt"},
		{"type":"function_call","call_id":"c2","name":"read_file","namespace":"muse","arguments":"{}"},
		{"type":"function_call_output","call_id":"c2","output":"x"}]}`
	r, err := parseResponses([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range r.Tools {
		names = append(names, tl.Name)
	}
	if got := strings.Join(names, ","); got != "read_file,muse__read_file,muse__bash,list_dir,other__bash" {
		t.Fatalf("tools = %s", got)
	}
	if q := r.Namespaced["list_dir"]; q != (nsTool{Namespace: "muse", Name: "list_dir"}) {
		t.Fatalf("namespaced = %+v", r.Namespaced)
	}
	// the conversation reads the same map: an unclaimed name's earlier call
	// goes under it, a claimed one's keeps the flat name
	var calls []string
	for i := range r.Messages {
		for j := range r.Messages[i].Parts {
			if p := &r.Messages[i].Parts[j]; p.Kind == ToolCall {
				calls = append(calls, p.Name)
			}
		}
	}
	if got := strings.Join(calls, ","); got != "list_dir,muse__read_file" {
		t.Fatalf("history calls = %s", got)
	}
}

func TestNamespacedToolsForwardedBare(t *testing.T) {
	// a chat upstream reads the tools list: Muse Spark, trained on Muse's
	// own tool names, answered a muse__-flat one "undeclared_tool" (#1448)
	r, err := parseResponses([]byte(`{"model":"m","input":"hi",` + namespacedTools + `}`))
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(buildChat(r, "upstream", "example.com", false), &raw); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range raw.Tools {
		names = append(names, tl.Function.Name)
	}
	if got := strings.Join(names, ","); got != "exec_command,spawn_agent,freeform" {
		t.Fatalf("forwarded = %s", got)
	}
}

func TestNamespacedBareCallBack(t *testing.T) {
	r, err := parseResponses([]byte(`{"model":"m","input":"hi",` + namespacedTools + `}`))
	if err != nil {
		t.Fatal(err)
	}
	res := Result{Parts: []Part{{Kind: ToolCall, ID: "c1", Name: "spawn_agent", Args: json.RawMessage(`{}`)}}}
	var out struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(render(provider.Responses, res, r), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Output) != 1 || out.Output[0]["name"] != "spawn_agent" || out.Output[0]["namespace"] != "collaboration" {
		t.Fatalf("output = %v", out.Output)
	}
}

func TestNamespacedCallInHistory(t *testing.T) {
	r, err := parseResponses([]byte(`{"model":"m",` + namespacedTools + `,"input":[
		{"type":"message","role":"user","content":"go"},
		{"type":"function_call","call_id":"c1","name":"spawn_agent","namespace":"collaboration","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"ok"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var call *Part
	for i := range r.Messages {
		for j := range r.Messages[i].Parts {
			if p := &r.Messages[i].Parts[j]; p.Kind == ToolCall {
				call = p
			}
		}
	}
	if call == nil || call.Name != "spawn_agent" {
		t.Fatalf("history call = %+v", call)
	}
}

func TestLongNamespacedNamesFit(t *testing.T) {
	ns := "mcp__" + strings.Repeat("a", 60)
	short := strings.Repeat("b", 40) + "1"
	long := strings.Repeat("b", 40) + "2"
	body := `{"model":"m","input":"hi","tools":[
		{"type":"function","name":"` + short + `","parameters":{"type":"object"}},
		{"type":"namespace","name":"` + ns + `","tools":[
			{"type":"function","name":"` + short + `"},
			{"type":"function","name":"` + long + `"}]}]}`
	r, err := parseResponses([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tools) != 3 || r.Tools[0].Name != short {
		t.Fatalf("tools = %+v", r.Tools)
	}
	// the namespace's short, claimed by the top-level tool, goes flat and
	// is cut to fit; its sibling keeps its own name as it is
	flat, bare := r.Tools[1], r.Tools[2]
	if flat.Name == bare.Name || flat.Name == short || len(flat.Name) > 64 || bare.Name != long {
		t.Fatalf("names = %q, %q", flat.Name, bare.Name)
	}
	if q := r.Namespaced[flat.Name]; q.Namespace != ns || q.Name != short {
		t.Fatalf("%q maps to %+v", flat.Name, q)
	}
	if q := r.Namespaced[bare.Name]; q.Namespace != ns || q.Name != long {
		t.Fatalf("%q maps to %+v", bare.Name, q)
	}
}

func namespacedReq() *Request {
	return &Request{Model: "m", Namespaced: map[string]nsTool{"collaboration__spawn_agent": {Namespace: "collaboration", Name: "spawn_agent"}}}
}

func TestNamespacedCallStreamed(t *testing.T) {
	rec := httptest.NewRecorder()
	enc := encoder(provider.Responses, newSSEWriter(rec), namespacedReq())
	enc.event(Event{Kind: KToolStart, ID: "c1", Name: "collaboration__spawn_agent"})
	enc.event(Event{Kind: KToolArgs, Text: `{"message":"x"}`})
	enc.event(Event{Kind: KToolStart, ID: "c2", Name: "exec_command"})
	enc.event(Event{Kind: KToolArgs, Text: `{}`})
	enc.finish()
	var done []map[string]any
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(data), &m) == nil && (m["type"] == "response.output_item.added" || m["type"] == "response.output_item.done") {
			done = append(done, m["item"].(map[string]any))
		}
	}
	if len(done) != 4 {
		t.Fatalf("items = %v", done)
	}
	for _, it := range done {
		switch it["call_id"] {
		case "c1":
			if enc, ok := it["encrypted_function_args"].([]any); !ok || len(enc) != 0 || it["name"] != "spawn_agent" || it["namespace"] != "collaboration" {
				t.Fatalf("namespaced call = %v", it)
			}
		case "c2":
			_, sealed := it["encrypted_function_args"]
			if _, has := it["namespace"]; has || sealed || it["name"] != "exec_command" {
				t.Fatalf("top-level call = %v", it)
			}
		}
	}
}

func TestNamespacedCallRendered(t *testing.T) {
	res := Result{Parts: []Part{{Kind: ToolCall, ID: "c1", Name: "collaboration__spawn_agent", Args: json.RawMessage(`{}`)}}}
	var out struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(render(provider.Responses, res, namespacedReq()), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Output) != 1 || out.Output[0]["name"] != "spawn_agent" || out.Output[0]["namespace"] != "collaboration" {
		t.Fatalf("output = %v", out.Output)
	}
}
