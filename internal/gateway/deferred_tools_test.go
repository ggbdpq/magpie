package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// deferredVendor answers as Kimi Code's and DeepSeek's Anthropic endpoints
// answered the real requests (10-14): Kimi refuses any message holding a
// tool_addition, a tool_removal or a tool_reference with a 400 that names
// nothing; DeepSeek takes a tool_addition naming a tool, and refuses one
// defining it, a tool_removal and a message's tool_reference with a 422.
// Both take a tool_reference inside a tool_result.
type deferredVendor struct {
	deepseek bool
	mu       sync.Mutex
	bodies   []string
}

func (v *deferredVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	v.mu.Lock()
	v.bodies = append(v.bodies, string(b))
	v.mu.Unlock()
	var m struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal(b, &m)
	refused := ""
	for _, msg := range m.Messages {
		var blocks []struct {
			Type string `json:"type"`
			Tool struct {
				Type string `json:"type"`
			} `json:"tool"`
		}
		json.Unmarshal(msg.Content, &blocks)
		for _, bl := range blocks {
			switch {
			case bl.Type == "tool_addition" && v.deepseek && bl.Tool.Type == "tool_definition":
				refused = "tool_definition"
			case bl.Type == "tool_addition" && v.deepseek:
			case bl.Type == "tool_addition", bl.Type == "tool_removal", bl.Type == "tool_reference":
				refused = bl.Type
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case refused != "" && v.deepseek:
		w.WriteHeader(422)
		io.WriteString(w, `{"error":{"message":"Failed to deserialize the JSON body into the target type: messages[1].content: unknown variant `+"`"+refused+"`"+`, expected one of `+"`text`, `image`, `document`, `thinking`, `server_tool_use`, `tool_use`, `tool_result`, `web_search_tool_result`, `tool_addition`"+` at line 1 column 233 (request_id: 160c0348-740b-41e4-8a7f-6f4dc596c741)","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`)
	case refused != "":
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"Invalid request Error"},"type":"error"}`)
	default:
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}
}

func (v *deferredVendor) sent() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.bodies...)
}

// Claude Code's deferred-tool blocks, as 2.1.296 builds them and as the
// reporter captured them.
const (
	ccAddRef = `{"type":"tool_addition","tool":{"type":"tool_reference","name":"Read"}}`
	ccAddDef = `{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"Read","description":"read a file","input_schema":{"type":"object"}}}}`
	ccRemove = `{"type":"tool_removal","tool":{"type":"tool_reference","name":"Read"}}`
	// the ToolSearch answer: stays wherever it goes
	ccSearchResult = `{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"ToolSearch","input":{"query":"select:Read"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":[{"type":"tool_reference","tool_name":"Read"}]}]}`
)

func deferredBody(model, block string) string {
	return `{"model":"` + model + `","max_tokens":32,"stream":false,"tools":[{"name":"Read","description":"read a file","input_schema":{"type":"object"}}],"messages":[` +
		`{"role":"user","content":"hi"},` + ccSearchResult + `,` +
		`{"role":"system","content":[{"type":"text","text":"t"},` + block + `]},` +
		`{"role":"system","content":[` + block + `]},` +
		`{"role":"user","content":"ok?"}]}`
}

// #2042 (davisjiahao): once Claude Code's ToolSearch put a tool_addition
// in the conversation, every request to Kimi Code's Anthropic endpoint was
// 400 "Invalid request Error". Kimi is sent no deferred-tool block, a
// message left empty goes, and the rest (the system text, the ToolSearch
// answer's tool_reference) stays.
func TestKimiIsSentNoDeferredToolBlocks(t *testing.T) {
	fresh(t)
	up := &deferredVendor{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "kimi-code", Name: "Kimi Code", Preset: "kimi-code", Key: "k", Anthropic: srv.URL,
		Models: []string{"k3-256k"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	reporter := `{"model":"kimi-code/k3-256k","max_tokens":32,"messages":[{"role":"user","content":"hi"},` +
		`{"role":"system","content":[{"type":"text","text":"t"},{"tool":"Read","type":"tool_addition"}]},` +
		`{"role":"user","content":"ok?"}],"thinking":{"type":"disabled"}}`
	cases := map[string]string{
		"reporter's bytes":         reporter,
		"addition by name":         deferredBody("kimi-code/k3-256k", ccAddRef),
		"addition defined":         deferredBody("kimi-code/k3-256k", ccAddDef),
		"removal":                  deferredBody("kimi-code/k3-256k", ccRemove),
		"message's tool_reference": deferredBody("kimi-code/k3-256k", `{"type":"tool_reference","tool_name":"Read"}`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			n := len(up.sent())
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			sent := up.sent()[n:]
			if len(sent) != 1 {
				t.Fatalf("Kimi was asked %d times, want once", len(sent))
			}
			got := sent[0]
			for _, b := range []string{`"tool_addition"`, `"tool_removal"`} {
				if strings.Contains(got, b) {
					t.Errorf("Kimi was sent %s: %s", b, got)
				}
			}
			if !strings.Contains(got, `"text":"t"`) || !strings.Contains(got, `"text":"ok?"`) && !strings.Contains(got, `"content":"ok?"`) {
				t.Errorf("the conversation's text was lost: %s", got)
			}
			if name != "reporter's bytes" && !strings.Contains(got, `"tool_name":"Read"}]}`) {
				t.Errorf("the ToolSearch answer's tool_reference was dropped: %s", got)
			}
			var m struct {
				Messages []json.RawMessage `json:"messages"`
			}
			json.Unmarshal([]byte(got), &m)
			for _, x := range m.Messages {
				if string(x) == `{"role":"system","content":[]}` || strings.Contains(string(x), `"content":[]`) {
					t.Errorf("a message left empty was sent: %s", x)
				}
			}
		})
	}
}

// DeepSeek's Anthropic endpoint takes a tool_addition naming a tool but
// refuses a tool_removal (422 "unknown variant `tool_removal`"): it is
// asked again at once without the blocks, and from then on sent none. A
// provider that takes them is sent them as Claude Code wrote them.
func TestDeferredToolBlocksRefusedOnceLeftOut(t *testing.T) {
	fresh(t)
	ds := &deferredVendor{deepseek: true}
	dsSrv := httptest.NewServer(ds)
	t.Cleanup(dsSrv.Close)
	takes := &deferredVendor{deepseek: true}
	tkSrv := httptest.NewServer(takes)
	t.Cleanup(tkSrv.Close)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Key: "k", Anthropic: dsSrv.URL,
		Models: []string{"deepseek-chat"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Key: "k", Anthropic: tkSrv.URL,
		Models: []string{"deepseek-chat"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	post := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
	}
	post(deferredBody("deepseek/deepseek-chat", ccRemove))
	sent := ds.sent()
	if len(sent) != 2 || !strings.Contains(sent[0], `"tool_removal"`) || strings.Contains(sent[1], `"tool_removal"`) {
		t.Fatalf("DeepSeek was asked %d times, want the removal once and then without it: %q", len(sent), sent)
	}
	// learned: the next request goes without them from the start, an
	// addition DeepSeek would take included
	post(deferredBody("deepseek/deepseek-chat", ccAddRef))
	if sent = ds.sent(); len(sent) != 3 || strings.Contains(sent[2], `"tool_addition"`) {
		t.Fatalf("after the refusal DeepSeek was sent %q", sent[2:])
	}
	// a provider that took them keeps getting them
	post(deferredBody("other/deepseek-chat", ccAddRef))
	if got := takes.sent(); len(got) != 1 || !strings.Contains(got[0], `"tool_addition"`) {
		t.Fatalf("a provider that takes tool_addition was sent %q", got)
	}
}

// resultDefVendor answers as an Anthropic endpoint that takes Claude
// Code's deferred-tool blocks but refuses the definition a newer
// ToolSearch answer carries in its tool_result (#5404, api.deepseek.com's
// answer of 2026-11-05), and a tool_reference naming a tool the request's
// tools list lacks.
type resultDefVendor struct {
	mu     sync.Mutex
	bodies []string
}

func (v *resultDefVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	v.mu.Lock()
	v.bodies = append(v.bodies, string(b))
	v.mu.Unlock()
	var m struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Messages []struct {
			Content []struct {
				Type    string `json:"type"`
				Content []struct {
					Type     string `json:"type"`
					ToolName string `json:"tool_name"`
				} `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal(b, &m)
	declared := map[string]bool{}
	for _, t := range m.Tools {
		declared[t.Name] = true
	}
	refused := ""
	for _, msg := range m.Messages {
		for _, bl := range msg.Content {
			if bl.Type != "tool_result" {
				continue
			}
			for _, it := range bl.Content {
				switch {
				case it.Type == "tool_definition":
					refused = "unknown variant `tool_definition`, expected `tool_reference`"
				case it.Type == "tool_reference" && !declared[it.ToolName]:
					refused = "tool reference not found in available tools"
				}
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if refused != "" {
		w.WriteHeader(422)
		io.WriteString(w, `{"error":{"message":"Failed to deserialize the JSON body into the target type: messages[1].content: `+refused+` at line 1 column 473","type":"invalid_request_error"}}`)
		return
	}
	io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
}

func (v *resultDefVendor) sent() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.bodies...)
}

// nestedDefBody is a conversation whose ToolSearch answer carries the
// loaded tool's definition itself in its tool_result, as newer Claude Code
// builds send it (#5404), with no deferred-tool block at the top level of
// any message.
const nestedDefBody = `{"model":"relay/deepseek-chat","max_tokens":32,"stream":false,` +
	`"tools":[{"name":"ToolSearch","description":"find tools","input_schema":{"type":"object"}}],` +
	`"messages":[` +
	`{"role":"user","content":"find a reader"},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"ToolSearch","input":{"query":"read"}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":[{"type":"tool_definition","definition":{"name":"Read","description":"read a file","input_schema":{"type":"object"}}}]}]},` +
	`{"role":"user","content":"go"}` + `]}`

// #5404: a ToolSearch answer's tool_definition survives a learned retry.
// The endpoint refuses it with 422 "unknown variant `tool_definition`,
// expected `tool_reference`"; the learned retry stripped the top-level
// blocks alone (none here), so the same variant failed again. Now the
// definition joins the tools and the answer goes as the reference the
// endpoint takes — the reference only once the tool is declared, which is
// what the hoisting is for.
func TestToolResultDefinitionRefusedOnceBecomesReference(t *testing.T) {
	fresh(t)
	up := &resultDefVendor{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: srv.URL,
		Models: []string{"deepseek-chat"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(nestedDefBody)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	sent := up.sent()
	if len(sent) != 2 {
		t.Fatalf("the relay was asked %d times, want the refusal once and then a body it takes: %d", len(sent), len(sent))
	}
	if !strings.Contains(sent[0], `"tool_definition"`) {
		t.Fatalf("the first ask came already reshaped: %s", sent[0])
	}
	if strings.Contains(sent[1], `"tool_definition"`) {
		t.Fatalf("the retry still carried the definition: %s", sent[1])
	}
	if !strings.Contains(sent[1], `"tool_name":"Read"`) {
		t.Fatalf("the retry lost the ToolSearch answer's reference: %s", sent[1])
	}
	if !strings.Contains(sent[1], `"name":"Read","description":"read a file"`) {
		t.Fatalf("the definition did not join the retry's tools: %s", sent[1])
	}
	if !strings.Contains(sent[1], `"tool_use_id":"toolu_01"`) {
		t.Fatalf("the retry lost the answer's pairing: %s", sent[1])
	}
}

// The reshaping, on the bytes: a definition in a tool_result joins the
// tools once, the block goes as the named reference, the rest of the
// result (a reference already there, text) stays; a body with only the
// nested definition is still one that gets worked, and one whose result
// can't be read goes as it came.
func TestNestedToolResultDefinitionBecomesReference(t *testing.T) {
	body := `{"model":"m","max_tokens":8,"stream":false,` +
		`"tools":[{"name":"ToolSearch","description":"find tools","input_schema":{"type":"object"}},{"name":"Read","description":"read a file","input_schema":{"type":"object"}}],` +
		`"messages":[` +
		`{"role":"user","content":"find readers"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"ToolSearch","input":{"query":"read"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":[{"type":"tool_reference","tool_name":"Read"},{"type":"tool_definition","definition":{"name":"Grep","description":"search files","input_schema":{"type":"object"}}},{"type":"text","text":"2 tools found"}]}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_02","name":"ToolSearch","input":{"query":"grep"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_02","content":[{"type":"tool_definition","definition":{"name":"Grep","description":"search files","input_schema":{"type":"object"}}}]}]},` +
		`{"role":"user","content":"go"}]}`
	if !hasDeferredTools([]byte(body)) {
		t.Fatal("a nested tool_definition was not seen as a deferred tool")
	}
	out := string(withoutDeferredTools([]byte(body)))
	if strings.Contains(out, `"tool_definition"`) {
		t.Fatalf("the body still carried a tool_definition: %s", out)
	}
	for _, want := range []string{`"tool_name":"Read"`, `"tool_name":"Grep"`, `"text":"2 tools found"`, `"tool_use_id":"toolu_01"`, `"tool_use_id":"toolu_02"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("the reshaped body lost %s: %s", want, out)
		}
	}
	if n := strings.Count(out, `"name":"Grep","description":"search files"`); n != 1 {
		t.Fatalf("Grep's definition joined the tools %d times, want once: %s", n, out)
	}
	if n := strings.Count(out, `"name":"Read","description":"read a file"`); n != 1 {
		t.Fatalf("Read's definition on the tools list %d times, want once: %s", n, out)
	}
	// a result whose content is no array of blocks goes as it came
	unreadable := `{"model":"m","max_tokens":8,"stream":false,"tools":[{"name":"ToolSearch","description":"find tools","input_schema":{"type":"object"}}],"messages":[` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":{"type":"tool_definition","definition":{"name":"Read","description":"read a file","input_schema":{"type":"object"}}}}]}]}`
	if !hasDeferredTools([]byte(unreadable)) {
		t.Fatal("a result carrying a tool_definition was not seen as a deferred tool")
	}
	out = string(withoutDeferredTools([]byte(unreadable)))
	if !strings.Contains(out, `"content":{"type":"tool_definition"`) {
		t.Fatalf("the unreadable result was reworked: %s", out)
	}
}

// isDeepSeekAnthropic is DeepSeek's own Anthropic endpoint alone, however
// the provider is built (#5404): a preset pointed elsewhere keeps the
// blocks until that endpoint refuses them.
func TestDeepSeekAnthropicRecognized(t *testing.T) {
	ds := provider.Provider{ID: "deepseek", Name: "DeepSeek", Key: "k",
		Anthropic: "https://api.deepseek.com/anthropic", Models: []string{"deepseek-chat"}}
	if !isDeepSeekAnthropic(ds, provider.Anthropic) {
		t.Fatal("DeepSeek's own endpoint was not recognized")
	}
	relay := provider.Provider{ID: "relay", Name: "Relay", Preset: "deepseek", Key: "k",
		Anthropic: "https://relay.example.invalid/anthropic", Models: []string{"deepseek-chat"}}
	if isDeepSeekAnthropic(relay, provider.Anthropic) {
		t.Fatal("another host was taken for DeepSeek's endpoint")
	}
}

// #5404: DeepSeek's own endpoint takes a tool_addition at no position
// now, so a body to it goes without the deferred-tool blocks on the first
// ask, its ToolSearch answer's definition on the tools list and the
// answer as the reference, instead of a 422 to learn from first.
func TestDeepSeekAnthropicIsStrippedUpFront(t *testing.T) {
	fresh(t)
	up := &resultDefVendor{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Key: "k", Anthropic: "http://api.deepseek.com/anthropic",
		Models: []string{"deepseek-chat"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	addr := strings.TrimPrefix(srv.URL, "http://")
	s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}}
	body := strings.Replace(nestedDefBody, `"relay/deepseek-chat"`, `"deepseek/deepseek-chat"`, 1)
	body = strings.Replace(body, `{"role":"user","content":"go"}`,
		`{"role":"system","content":[{"type":"text","text":"t"},`+ccAddDef+`]},{"role":"user","content":"go"}`, 1)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	sent := up.sent()
	if len(sent) != 1 {
		t.Fatalf("DeepSeek was asked %d times, want once: %q", len(sent), sent)
	}
	for _, b := range []string{`"tool_addition"`, `"tool_definition"`} {
		if strings.Contains(sent[0], b) {
			t.Errorf("DeepSeek was sent %s: %s", b, sent[0])
		}
	}
	if !strings.Contains(sent[0], `"tool_name":"Read"`) || !strings.Contains(sent[0], `"name":"Read","description":"read a file"`) {
		t.Errorf("the ToolSearch answer did not go as a declared reference: %s", sent[0])
	}
}

// #7817: agy-cli builds its ide-tools MCP declarations only from top-level
// tools and ignores tool_addition and ToolSearch tool_definition blocks while
// still answering 200, so deferred tools stayed uncallable after ToolSearch
// and DeferredToolPlaceholder leaked into ide-tools. Hoisting deferred
// definitions up front and stripping DeferredToolPlaceholder keeps loaded
// tools callable from the next turn without affecting other providers.
func TestAgyCLIDeferredToolsHoistedAndPlaceholderStripped(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	pid := besideFake(t, "agy-cli", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"fake-claude","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`event: content_block_stop`,
			`data: {"type":"content_block_stop","index":0}`,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
		))
	}))
	s := New()
	h := s.Handler()

	// Turn 1: before ToolSearch, DeferredToolPlaceholder is stripped while eager tools stay.
	turn1 := `{"model":"` + pid + `/fake-claude","max_tokens":32,"stream":true,` +
		`"tools":[{"name":"Edit","description":"edit file","input_schema":{"type":"object"}},` +
		`{"name":"ToolSearch","description":"find tools","input_schema":{"type":"object"}},` +
		`{"name":"DeferredToolPlaceholder","description":"","input_schema":{"type":"object"},"defer_loading":true}],` +
		`"messages":[{"role":"user","content":"inspect repo"}]}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(turn1)))
	if rec.Code != 200 {
		t.Fatalf("turn 1 status %d: %s", rec.Code, rec.Body)
	}

	// Turn 2: after ToolSearch loads Read (in tool_result) and Bash (via tool_addition),
	// both definitions join top-level tools and DeferredToolPlaceholder stays out.
	bashAdd := `{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"Bash","description":"run shell","input_schema":{"type":"object"}}}}`
	turn2 := `{"model":"` + pid + `/fake-claude","max_tokens":32,"stream":true,` +
		`"tools":[{"name":"Edit","description":"edit file","input_schema":{"type":"object"}},` +
		`{"name":"ToolSearch","description":"find tools","input_schema":{"type":"object"}},` +
		`{"name":"DeferredToolPlaceholder","description":"","input_schema":{"type":"object"},"defer_loading":true}],` +
		`"messages":[` +
		`{"role":"user","content":"inspect repo"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"ToolSearch","input":{"query":"select:Read,Bash"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":[{"type":"tool_definition","definition":{"name":"Read","description":"read a file","input_schema":{"type":"object"}}}]}]},` +
		`{"role":"system","content":[` + bashAdd + `]},` +
		`{"role":"user","content":"read README"}]}`
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(turn2)))
	if rec.Code != 200 {
		t.Fatalf("turn 2 status %d: %s", rec.Code, rec.Body)
	}

	mu.Lock()
	got := append([]string(nil), sent...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("agy-cli asked %d times, want 2", len(got))
	}
	if strings.Contains(got[0], `"DeferredToolPlaceholder"`) {
		t.Errorf("turn 1 still sent DeferredToolPlaceholder: %s", got[0])
	}
	if !strings.Contains(got[0], `"name":"Edit"`) || !strings.Contains(got[0], `"name":"ToolSearch"`) {
		t.Errorf("turn 1 lost eager tools: %s", got[0])
	}
	for _, banned := range []string{`"DeferredToolPlaceholder"`, `"tool_addition"`, `"tool_definition"`} {
		if strings.Contains(got[1], banned) {
			t.Errorf("turn 2 still sent %s: %s", banned, got[1])
		}
	}
	for _, want := range []string{`"tool_name":"Read"`, `"tool_use_id":"toolu_01"`} {
		if !strings.Contains(got[1], want) {
			t.Errorf("turn 2 missing %s: %s", want, got[1])
		}
	}
	var turn2Sent struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(got[1]), &turn2Sent); err != nil {
		t.Fatalf("parse turn 2 body: %v", err)
	}
	toolNames := make(map[string]bool, len(turn2Sent.Tools))
	for _, tl := range turn2Sent.Tools {
		toolNames[tl.Name] = true
	}
	for _, want := range []string{"Edit", "ToolSearch", "Read", "Bash"} {
		if !toolNames[want] {
			t.Errorf("turn 2 top-level tools missing %q: %v", want, turn2Sent.Tools)
		}
	}
	if toolNames["DeferredToolPlaceholder"] {
		t.Errorf("turn 2 top-level tools still contain DeferredToolPlaceholder: %v", turn2Sent.Tools)
	}
	// Guard: unrelated plugins and prose mentions of DeferredToolPlaceholder stay untouched.
	if deferredOutUpFront(provider.PluginForTest("other", "other"), provider.Anthropic, []byte(`{"model":"gemini-4-neon"}`)) {
		t.Errorf("unrelated plugin was stripped up front")
	}
	proseOnly := `{"model":"m","max_tokens":8,"tools":[{"name":"Edit","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"DeferredToolPlaceholder mentioned in prose"}]}`
	if unchanged := string(withoutDeferredTools([]byte(proseOnly))); unchanged != proseOnly {
		t.Errorf("prose mention of DeferredToolPlaceholder altered body: %s", unchanged)
	}
}
