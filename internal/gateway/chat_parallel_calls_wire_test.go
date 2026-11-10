package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// Antigravity's app, wired with wireProtocol "openai", asks
// /v1/chat/completions streamed (#5908). The model's parallel calls —
// three functionCall parts in one turn from a custom Gemini-compatible
// provider — have to reach it as three tool_calls its OpenAI-style SDK
// keeps apart: an index and an id of their own each, so it runs all three
// and answers each by id. Merged into one, the turn's correspondence of
// ids, arguments and results is lost and the app retries for good.

// chatParallelUpstream is a Gemini API whose model answers a prompt for
// parallel calls with three functionCall parts in one chunk.
func chatParallelUpstream(t *testing.T) (*httptest.Server, func() []string) {
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
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read_file","args":{"path":"a"}}},{"functionCall":{"name":"read_file","args":{"path":"b"}}},{"functionCall":{"name":"read_file","args":{"path":"c"}}}]}}],"modelVersion":"gemini-4-argon"}`,
			`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}}`,
		))
	}))
	t.Cleanup(up.Close)
	return up, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
}

// streamedToolCalls collects the tool_calls deltas of a Chat Completions
// SSE reply: the fragments per index, and the finish reason.
func streamedToolCalls(t *testing.T, body string) (map[string]string, map[string]string, string) {
	t.Helper()
	args := map[string]string{} // index -> arguments as they add up
	ids := map[string]string{}  // index -> id
	finish := ""
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue
		}
		if !gjson.Get(data, "choices").Exists() {
			continue
		}
		if f := gjson.Get(data, "choices.0.finish_reason"); f.Exists() && f.Type == gjson.String && f.String() != "" {
			finish = f.String()
		}
		for _, tc := range gjson.Get(data, "choices.0.delta.tool_calls").Array() {
			idx := tc.Get("index").String()
			if id := tc.Get("id"); id.Exists() && id.String() != "" {
				ids[idx] = id.String()
			}
			args[idx] += tc.Get("function.arguments").String()
		}
	}
	return ids, args, finish
}

func TestChatWireKeepsParallelCallsApart(t *testing.T) {
	up, _ := chatParallelUpstream(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "gem", Name: "Gem", Key: "AIza-test", Gemini: up.URL + "/v1beta", Models: []string{"gemini-4-argon"}}); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	res, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(
		`{"model":"gem/gemini-4-argon","stream":true,"messages":[{"role":"user","content":"call three tools in parallel"}],
		  "tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d: %s", res.StatusCode, b)
	}
	ids, args, finish := streamedToolCalls(t, string(b))
	if len(ids) != 3 {
		t.Fatalf("the reply carried %d calls, want 3: %s", len(ids), b)
	}
	want := map[string]string{"0": `{"path":"a"}`, "1": `{"path":"b"}`, "2": `{"path":"c"}`}
	seen := map[string]bool{}
	for idx, w := range want {
		if ids[idx] == "" {
			t.Fatalf("no call at index %s: %s", idx, b)
		}
		if seen[ids[idx]] {
			t.Fatalf("two calls share id %s: %s", ids[idx], b)
		}
		seen[ids[idx]] = true
		if strings.Join(strings.Fields(args[idx]), "") != w {
			t.Fatalf("call %s arguments %q, want %s: %s", idx, args[idx], w, b)
		}
	}
	if finish != "tool_calls" {
		t.Fatalf("finish_reason %q, want tool_calls: %s", finish, b)
	}
}

// The turn after the calls: the app answers each by its id, and the
// history it replays — the assistant turn with all three tool_calls and
// the three answers — reaches the Gemini upstream with every call and
// every response, each paired by id.
func TestChatWireReplaysParallelCallsWhole(t *testing.T) {
	up, bodies := chatParallelUpstream(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "gem", Name: "Gem", Key: "AIza-test", Gemini: up.URL + "/v1beta", Models: []string{"gemini-4-argon"}}); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	res, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(
		`{"model":"gem/gemini-4-argon","stream":true,"messages":[
			{"role":"user","content":"call three tools in parallel"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a\"}"}},
				{"id":"call_2","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"b\"}"}},
				{"id":"call_3","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"c\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"a-file"},
			{"role":"tool","tool_call_id":"call_2","content":"b-file"},
			{"role":"tool","tool_call_id":"call_3","content":"c-file"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d: %s", res.StatusCode, b)
	}
	bs := bodies()
	if len(bs) != 1 {
		t.Fatalf("upstream asked %d times: %s", len(bs), b)
	}
	raw := gjson.Get(bs[0], "contents").Raw
	for _, s := range []string{`"path":"a"`, `"path":"b"`, `"path":"c"`, "a-file", "b-file", "c-file"} {
		if !strings.Contains(raw, s) {
			t.Fatalf("upstream history lost %s: %s", s, raw)
		}
	}
	calls := gjson.Get(bs[0], "contents.#(role==model).parts")
	n := 0
	for _, p := range calls.Array() {
		if p.Get("functionCall").Exists() {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("the call turn holds %d functionCalls, want 3: %s", n, raw)
	}
}

// The same turn in the shapes a Gemini-compatible provider streams it:
// one call a chunk, ids of the upstream's own, a thought signature riding
// each call, thinking between the calls — and the reply asked whole. All
// of them reach the app as three calls kept apart.
func TestChatWireParallelCallShapes(t *testing.T) {
	cases := []struct {
		name   string
		frames []string
	}{
		{"one call a chunk", []string{
			`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read_file","args":{"path":"a"}}}]}}],"modelVersion":"m"}`,
			`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read_file","args":{"path":"b"}}}]}}],"modelVersion":"m"}`,
			`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read_file","args":{"path":"c"}}}]}}],"modelVersion":"m"}`,
			`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}}`,
		}},
		{"ids of the upstream's own", []string{
			`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"fc_1","name":"read_file","args":{"path":"a"}}},{"functionCall":{"id":"fc_2","name":"read_file","args":{"path":"b"}}},{"functionCall":{"id":"fc_3","name":"read_file","args":{"path":"c"}}}]}}],"modelVersion":"m"}`,
			`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}}`,
		}},
		{"thought signatures", []string{
			`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read_file","args":{"path":"a"}},"thoughtSignature":"c2lnX2E="},{"functionCall":{"name":"read_file","args":{"path":"b"}},"thoughtSignature":"c2lnX2I="},{"functionCall":{"name":"read_file","args":{"path":"c"}},"thoughtSignature":"c2lnX2M="}]}}],"modelVersion":"m"}`,
			`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}}`,
		}},
		{"thinking between the calls", []string{
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"plan","thought":true},{"functionCall":{"name":"read_file","args":{"path":"a"}}}]}}],"modelVersion":"m"}`,
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"more","thought":true},{"functionCall":{"name":"read_file","args":{"path":"b"}}},{"functionCall":{"name":"read_file","args":{"path":"c"}}}]}}],"modelVersion":"m"}`,
			`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}}`,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up, _ := chatParallelUpstream(t)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			if err := provider.Save(provider.Provider{ID: "gem", Name: "Gem", Key: "AIza-test", Gemini: up.URL + "/v1beta", Models: []string{"m"}}); err != nil {
				t.Fatal(err)
			}
			gw := httptest.NewServer(New().Handler())
			defer gw.Close()
			res, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(
				`{"model":"gem/m","stream":true,"messages":[{"role":"user","content":"x"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			ids, _, finish := streamedToolCalls(t, string(b))
			if res.StatusCode != 200 || len(ids) != 3 || finish != "tool_calls" {
				t.Fatalf("status %d, %d calls, finish %q: %s", res.StatusCode, len(ids), finish, b)
			}
		})
	}
}

// A reply asked whole carries the three calls in one tool_calls array.
func TestChatWireParallelCallsWholeReply(t *testing.T) {
	up, _ := chatParallelUpstream(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "gem", Name: "Gem", Key: "AIza-test", Gemini: up.URL + "/v1beta", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	res, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(
		`{"model":"gem/m","stream":false,"messages":[{"role":"user","content":"x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if n := len(gjson.Get(string(b), "choices.0.message.tool_calls").Array()); res.StatusCode != 200 || n != 3 {
		t.Fatalf("status %d, %d calls: %s", res.StatusCode, n, b)
	}
}

// The app's own genai wire, for the same chain: the calls reach a Gemini
// client whole, one part each, and the replayed history keeps every call
// and every answer.
func TestGenaiWireParallelCalls(t *testing.T) {
	up, bodies := chatParallelUpstream(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "gem", Name: "Gem", Key: "AIza-test", Gemini: up.URL + "/v1beta", Models: []string{"gemini-4-argon"}}); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	res, err := http.Post(gw.URL+"/v1beta/models/gemini-4-argon:streamGenerateContent?alt=sse", "application/json", strings.NewReader(
		`{"contents":[{"role":"user","parts":[{"text":"call three tools in parallel"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d: %s", res.StatusCode, b)
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		for _, p := range gjson.Get(strings.TrimPrefix(line, "data: "), "candidates.0.content.parts").Array() {
			if p.Get("functionCall").Exists() {
				n++
			}
		}
	}
	if n != 3 {
		t.Fatalf("the genai wire reply carries %d functionCalls: %s", n, b)
	}
	res2, err := http.Post(gw.URL+"/v1beta/models/gemini-4-argon:streamGenerateContent?alt=sse", "application/json", strings.NewReader(
		`{"contents":[{"role":"user","parts":[{"text":"go"}]},
		  {"role":"model","parts":[{"functionCall":{"id":"call_1","name":"read_file","args":{"path":"a"}}},{"functionCall":{"id":"call_2","name":"read_file","args":{"path":"b"}}},{"functionCall":{"id":"call_3","name":"read_file","args":{"path":"c"}}}]},
		  {"role":"user","parts":[{"functionResponse":{"id":"call_1","name":"read_file","response":{"output":"a-file"}}},{"functionResponse":{"id":"call_2","name":"read_file","response":{"output":"b-file"}}},{"functionResponse":{"id":"call_3","name":"read_file","response":{"output":"c-file"}}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(res2.Body)
	res2.Body.Close()
	if res2.StatusCode != 200 {
		t.Fatalf("follow-up status %d: %s", res2.StatusCode, b2)
	}
	bs := bodies()
	if len(bs) != 2 {
		t.Fatalf("upstream asked %d times", len(bs))
	}
	last := gjson.Get(bs[1], "contents").Raw
	for _, s := range []string{"a-file", "b-file", "c-file", `"path":"a"`, `"path":"b"`, `"path":"c"`} {
		if !strings.Contains(last, s) {
			t.Fatalf("the genai replay lost %s: %s", s, last)
		}
	}
}
