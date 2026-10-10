package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// geminiPairingUpstream is a Gemini API that reads its histories as
// Google's does: every turn holding functionCall parts must be followed
// right away by a turn whose functionResponse parts answer each of those
// calls ("Please ensure that the number of function response parts is
// equal to the number of function call parts of the function call turn",
// ai.google.dev/gemini-api/docs/troubleshooting). One that isn't is turned
// away 400, the whole request with it. What it takes, it answers.
func geminiPairingUpstream(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.HasSuffix(r.URL.Path, ":streamGenerateContent") {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		// and as Vertex AI read #1533's: a user turn holding a
		// functionResponse beside text is refused too
		var m map[string]any
		json.Unmarshal(b, &m)
		if code, _ := vertexTurnsAway(0, m); code != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			io.WriteString(w, vertexModelLast)
			return
		}
		if !geminiAnswersEveryCall(b) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"code":400,"message":"Please ensure that the number of function response parts is equal to the number of function call parts of the function call turn.","status":"INVALID_ARGUMENT"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"paired-"}]}}],"modelVersion":"gm"}`,
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"up"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}}`,
		))
	}))
	t.Cleanup(up.Close)
	return up, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
}

// geminiAnswersEveryCall reports whether body's contents hold, right after
// every turn that calls functions, a turn answering each of those calls.
func geminiAnswersEveryCall(body []byte) bool {
	var q struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				FunctionCall *struct {
					Name string `json:"name"`
					ID   string `json:"id"`
				} `json:"functionCall"`
				FunctionResponse *struct {
					Name string `json:"name"`
					ID   string `json:"id"`
				} `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if json.Unmarshal(body, &q) != nil {
		return false
	}
	type call struct{ name, id string }
	var want []call
	for _, c := range q.Contents {
		if len(want) > 0 {
			// the turn right after the calls answers each of them
			got := map[string]bool{}
			for _, p := range c.Parts {
				if p.FunctionResponse == nil {
					continue
				}
				if p.FunctionResponse.ID != "" {
					got[p.FunctionResponse.ID] = true
				} else {
					got[p.FunctionResponse.Name] = true
				}
			}
			for _, w := range want {
				key := w.id
				if key == "" {
					key = w.name
				}
				if !got[key] {
					return false
				}
			}
			want = nil
		}
		for _, p := range c.Parts {
			if p.FunctionCall != nil {
				want = append(want, call{p.FunctionCall.Name, p.FunctionCall.ID})
			}
		}
	}
	return len(want) == 0
}

// A turn the agent stopped with its tool call still out (the user went on
// in the same conversation) is replayed by the client with the call and no
// result. Google turns a history whose call turn goes unanswered away with
// 400, every later turn with it, so the chat build answers such a call
// with a synthetic result (pairToolMessages, #1275). The Gemini build,
// which serves a Google sign-in, Vertex AI and a custom provider's Gemini
// API alike, sent the call as it was.
func TestGeminiInterruptedCallIsAnswered(t *testing.T) {
	up, bodies := geminiPairingUpstream(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "gem", Name: "Gem", Key: "AIza-test", Gemini: up.URL + "/v1beta", Models: []string{"gemini-2.5-pro"}}); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	res, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(
		`{"model":"gem/gemini-2.5-pro","stream":true,"messages":[
			{"role":"user","content":"look into it"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"date\"}"}}]},
			{"role":"user","content":"continue"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), "paired-") {
		t.Fatalf("status %d: %s", res.StatusCode, b)
	}
	bs := bodies()
	if len(bs) != 1 {
		t.Fatalf("upstream asked %d times", len(bs))
	}
	if !strings.Contains(gjson.Get(bs[0], "contents").Raw, "the turn was interrupted") {
		t.Fatalf("no synthetic result sent: %s", bs[0])
	}
}

// One call of a parallel turn answered, its mate left out by the stop: the
// turn that follows the calls answers both, the client's own beside the
// synthetic one.
func TestGeminiParallelCallsOneAnswered(t *testing.T) {
	up, bodies := geminiPairingUpstream(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "gem", Name: "Gem", Key: "AIza-test", Gemini: up.URL + "/v1beta", Models: []string{"gemini-2.5-pro"}}); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	res, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(
		`{"model":"gem/gemini-2.5-pro","stream":true,"messages":[
			{"role":"user","content":"look into it"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"A","arguments":"{}"}},{"id":"call_2","type":"function","function":{"name":"B","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"1"},
			{"role":"user","content":"continue"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), "paired-") {
		t.Fatalf("status %d: %s", res.StatusCode, b)
	}
	bs := bodies()
	if len(bs) != 1 {
		t.Fatalf("upstream asked %d times", len(bs))
	}
	if !strings.Contains(gjson.Get(bs[0], "contents").Raw, "the turn was interrupted") {
		t.Fatalf("no synthetic result sent: %s", bs[0])
	}
}

// The same interrupted turn from a Messages client (Claude Code's history
// holds the tool_use and no result).
func TestGeminiMessagesClientInterruptedCall(t *testing.T) {
	up, bodies := geminiPairingUpstream(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "gem", Name: "Gem", Key: "AIza-test", Gemini: up.URL + "/v1beta", Models: []string{"gemini-2.5-pro"}}); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	res, err := http.Post(gw.URL+"/v1/messages", "application/json", strings.NewReader(
		`{"model":"gem/gemini-2.5-pro","max_tokens":64,"stream":true,"messages":[
			{"role":"user","content":"look into it"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"date"}}]},
			{"role":"user","content":"continue"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), "paired-") {
		t.Fatalf("status %d: %s", res.StatusCode, b)
	}
	bs := bodies()
	if len(bs) != 1 {
		t.Fatalf("upstream asked %d times", len(bs))
	}
	if !strings.Contains(gjson.Get(bs[0], "contents").Raw, "the turn was interrupted") {
		t.Fatalf("no synthetic result sent: %s", bs[0])
	}
}

// A history whose every call is answered goes as it was.
func TestGeminiAnsweredCallsUnchanged(t *testing.T) {
	up, bodies := geminiPairingUpstream(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "gem", Name: "Gem", Key: "AIza-test", Gemini: up.URL + "/v1beta", Models: []string{"gemini-2.5-pro"}}); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	res, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(
		`{"model":"gem/gemini-2.5-pro","stream":true,"messages":[
			{"role":"user","content":"look into it"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"date\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"Thu Oct 8"},
			{"role":"assistant","content":"done"},
			{"role":"user","content":"thanks"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), "paired-") {
		t.Fatalf("status %d: %s", res.StatusCode, b)
	}
	bs := bodies()
	if len(bs) != 1 {
		t.Fatalf("upstream asked %d times", len(bs))
	}
	if strings.Contains(gjson.Get(bs[0], "contents").Raw, "the turn was interrupted") {
		t.Fatalf("synthetic result sent beside the client's own: %s", bs[0])
	}
}

// The synthetic result of a call the user went on past goes in a turn of
// its own, before what the user then said: a user turn mixing a
// functionResponse with text is refused as #1533's was ("Requests ending
// with a model turn are not supported"). A Claude model on Antigravity
// keeps it in the one tool_result turn Anthropic reads.
func TestGeminiInterruptedCallsResultInATurnOfItsOwn(t *testing.T) {
	r := &Request{Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "look into it"}}},
		{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "call_1", Name: "Bash", Args: json.RawMessage(`{"command":"date"}`)}}},
		{Role: "user", Parts: []Part{{Kind: Text, Text: "continue"}}},
	}}
	type turn struct {
		Role  string           `json:"role"`
		Parts []map[string]any `json:"parts"`
	}
	contentsOf := func(b []byte) []turn {
		var q struct {
			Contents []turn                    `json:"contents"`
			Request  struct{ Contents []turn } `json:"request"`
		}
		json.Unmarshal(b, &q)
		if q.Contents == nil {
			return q.Request.Contents
		}
		return q.Contents
	}
	sent := map[string][]byte{}
	sent["vertex"], _ = buildVertex(r, "gemini-3.8-flash")
	sent["gemini"], _ = buildGemini(r, "gemini-3.8-flash")
	sent["code assist gemini"] = buildCodeAssist(r, "gemini-3.8-flash", "gemini")
	sent["code assist antigravity"] = buildCodeAssist(r, "gemini-3.8-flash", "antigravity")
	for name, b := range sent {
		cs := contentsOf(b)
		if len(cs) != 4 {
			t.Errorf("%s: want 4 turns, got %s", name, b)
			continue
		}
		res, said := cs[2], cs[3]
		if res.Role != "user" || len(res.Parts) != 1 || res.Parts[0]["functionResponse"] == nil {
			t.Errorf("%s: the synthetic result's turn is %v", name, res)
		}
		if said.Role != "user" || len(said.Parts) != 1 || said.Parts[0]["text"] != "continue" {
			t.Errorf("%s: the user's turn after it is %v", name, said)
		}
	}
	cs := contentsOf(buildCodeAssist(r, "claude-sonnet-4-5", "antigravity"))
	if len(cs) != 3 || len(cs[2].Parts) != 2 || cs[2].Parts[0]["functionResponse"] == nil || cs[2].Parts[1]["text"] != "continue" {
		t.Errorf("Antigravity's Claude: want the result beside the text in one turn, got %v", cs)
	}
}

// A call that came with no id, answered by name in the turn after it, is
// answered once, not given a synthetic result beside its own.
func TestGeminiCallWithNoIDAnsweredOnce(t *testing.T) {
	contents := []map[string]any{
		{"role": "user", "parts": []map[string]any{{"text": "look into it"}}},
		{"role": "model", "parts": []map[string]any{{"functionCall": map[string]any{"name": "Bash", "args": map[string]any{}}}}},
		{"role": "user", "parts": []map[string]any{{"functionResponse": map[string]any{"name": "Bash", "response": map[string]any{"output": "Thu Oct 8"}}}}},
	}
	out := pairFunctionTurns(contents, false, false)
	if len(out) != 3 || len(out[2]["parts"].([]map[string]any)) != 1 {
		t.Fatalf("an answered call with no id was answered again: %v", out)
	}
}
