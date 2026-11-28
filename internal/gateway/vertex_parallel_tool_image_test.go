package gateway

import (
	"encoding/json"
	"testing"
)

// A Vertex request with 2+ parallel tool calls where a non-last tool result
// carries an image must still put both functionResponse parts in the single
// user turn right after the model turn: Vertex 400s a call turn whose next
// user turn has fewer functionResponse parts than the model had calls
// (#6799). The image goes in a following user turn.
func TestVertexParallelToolImageKeepsAllResponsesTogether(t *testing.T) {
	const body = `{"model":"vertex/gemini-4-hydrogen","messages":[
		{"role":"user","content":"Inspect the image and run a shell check."},
		{"role":"assistant","content":null,"tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"vision_analyze","arguments":"{\"path\":\"a.png\"}"}},
			{"id":"call_2","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"pwd\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":[
			{"type":"text","text":"Image loaded."},
			{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="}}]},
		{"role":"tool","tool_call_id":"call_2","content":"/tmp"}
	]}`
	r, err := parseChat([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	req, err := geminiRequest(r, "gemini-4-hydrogen", "vertex")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(req)
	var env struct {
		Contents []struct {
			Role  string                           `json:"role"`
			Parts []map[string]json.RawMessage     `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	// Find the model turn that carries the two function calls, then the
	// user turn immediately after it.
	modelAt := -1
	for i, c := range env.Contents {
		if c.Role != "model" {
			continue
		}
		n := 0
		for _, p := range c.Parts {
			if p["functionCall"] != nil {
				n++
			}
		}
		if n == 2 {
			modelAt = i
			break
		}
	}
	if modelAt < 0 {
		t.Fatalf("no model turn with 2 function calls in %s", raw)
	}
	if modelAt+1 >= len(env.Contents) {
		t.Fatalf("no user turn after the calls in %s", raw)
	}
	next := env.Contents[modelAt+1]
	if next.Role != "user" {
		t.Fatalf("turn after the calls is %q, want user", next.Role)
	}
	var calls, responses, images int
	for _, p := range next.Parts {
		switch {
		case p["functionResponse"] != nil:
			responses++
		case p["inlineData"] != nil, p["fileData"] != nil:
			images++
		}
	}
	_ = calls
	if responses != 2 {
		t.Errorf("the user turn after the 2 calls has %d functionResponse parts, want 2; an image split the responses (body: %s)", responses, raw)
	}
	if images != 0 {
		t.Errorf("the response turn also holds %d image part(s), want them in a following turn (body: %s)", images, raw)
	}
	// A later user turn must carry the image, and no functionResponse may
	// be stranded after it.
	imageTurn := -1
	for i := modelAt + 2; i < len(env.Contents); i++ {
		for _, p := range env.Contents[i].Parts {
			if p["inlineData"] != nil || p["fileData"] != nil {
				imageTurn = i
				break
			}
		}
		if imageTurn >= 0 {
			break
		}
	}
	if imageTurn < 0 {
		t.Fatalf("no image turn after the responses in %s", raw)
	}
	for i := imageTurn; i < len(env.Contents); i++ {
		for _, p := range env.Contents[i].Parts {
			if p["functionResponse"] != nil {
				t.Errorf("functionResponse stranded in turn %d after the image at turn %d (body: %s)", i, imageTurn, raw)
			}
		}
	}
}

// The same fix covers a tool's audio on a non-last parallel call (#6799's
// sibling): it travels through the same toolImage/said split as images.
func TestVertexParallelToolAudioKeepsAllResponsesTogether(t *testing.T) {
	const body = `{"model":"vertex/gemini-4-hydrogen","messages":[
		{"role":"user","content":"check both"},
		{"role":"assistant","content":null,"tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"record","arguments":"{}"}},
			{"id":"call_2","type":"function","function":{"name":"terminal","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":[
			{"type":"text","text":"recorded"},
			{"type":"input_audio","input_audio":{"data":"UklGRiQAAABXQVZFZm10IBAAAAABAAEAQB8AAIA+AAACABAAZGF0YQAAAAA=","format":"wav"}}]},
		{"role":"tool","tool_call_id":"call_2","content":"/tmp"}
	]}`
	r, err := parseChat([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	req, err := geminiRequest(r, "gemini-4-hydrogen", "vertex")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(req)
	firstResponse := -1
	for i, c := range req["contents"].([]any) {
		turn := c.(map[string]any)
		if turn["role"] != "user" {
			continue
		}
		n := 0
		for _, p := range turn["parts"].([]any) {
			if p.(map[string]any)["functionResponse"] != nil {
				n++
			}
		}
		if n > 0 {
			firstResponse = i
			break
		}
	}
	if firstResponse < 0 {
		t.Fatalf("no response turn in %s", raw)
	}
	turn := req["contents"].([]any)[firstResponse].(map[string]any)
	n := 0
	for _, p := range turn["parts"].([]any) {
		if p.(map[string]any)["functionResponse"] != nil {
			n++
		}
	}
	if n != 2 {
		t.Errorf("the user turn after the calls has %d functionResponses, want 2 (body: %s)", n, raw)
	}
}
