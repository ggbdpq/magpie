package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

const textCallTools = `"tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}},{"type":"function","function":{"name":"edit","parameters":{"type":"object"}}}]`

// chunkOf is a Chat chunk saying text.
func chunkOf(text string) string {
	b, _ := json.Marshal(text)
	return `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":` + string(b) + `},"finish_reason":null}]}`
}

const stopChunk = `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`

// relayedChat streams up through the gateway to a Chat client whose
// request is body, and gives back the text, the calls (name → arguments)
// and the finish reason the client read.
func relayedChat(t *testing.T, body string, up ...string) (text string, calls map[string]string, finish, raw string) {
	t.Helper()
	fresh(t)
	a := &scripted{replies: []reply{{200, "text/event-stream", sse(append(up, `data: [DONE]`)...)}}}
	scriptedOn(t, "a", provider.Chat, a)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	raw = rec.Body.String()
	calls = map[string]string{}
	var names []string
	for _, line := range strings.Split(raw, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var ch struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int `json:"index"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			t.Fatalf("unreadable chunk %q: %v", data, err)
		}
		for _, c := range ch.Choices {
			text += c.Delta.Content
			for _, tc := range c.Delta.ToolCalls {
				if tc.Function.Name != "" {
					names = append(names, tc.Function.Name)
				}
				if len(names) == 0 {
					continue // a fragment with no name before it
				}
				calls[names[len(names)-1]] += tc.Function.Arguments
			}
			if c.FinishReason != nil {
				finish = *c.FinishReason
			}
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(raw), "data: [DONE]") {
		t.Fatalf("stream didn't end with [DONE]: %s", raw)
	}
	return text, calls, finish, raw
}

// A call DeepSeek wrote into its text, as Hermes' <tool_call> block, its
// tag split across chunks, reaches a Chat client as the call it is, after
// the text before it, and the reply ends as one that called a tool.
func TestTextWrittenCallRelayed(t *testing.T) {
	text, calls, finish, raw := relayedChat(t, `{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}],`+textCallTools+`}`,
		chunkOf("我先看看目录。\n\n<tool"),
		chunkOf(`_call>{"name":"bash","arguments":{"command":"ls -la"}}`),
		chunkOf("</tool_call>"),
		stopChunk)
	if text != "我先看看目录。\n\n" || calls["bash"] != `{"command":"ls -la"}` || finish != "tool_calls" || strings.Contains(raw, "tool_call>") {
		t.Fatalf("text %q calls %v finish %q\n%s", text, calls, finish, raw)
	}
	if !strings.Contains(raw, `"total_tokens":5`) {
		t.Fatalf("usage lost: %s", raw)
	}
}

// #823's second screenshot: a block whose command has a raw newline in its
// string, with DeepSeek's own closing tags strewn after it, and an edit
// whose arguments came as a string of JSON.
func TestTextWrittenCallWithJunkRelayed(t *testing.T) {
	_, calls, finish, raw := relayedChat(t, `{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}],`+textCallTools+`}`,
		chunkOf("<tool_call>{\"name\":\"bash\",\"arguments\":{\"command\":\"cd /e && for s in a b; do\n\techo \\\"$s\\\"; done\"}}</｜DSML｜parameter>\n</｜DSML｜invoke"),
		chunkOf(`<tool_call>{"name":"edit","arguments":"{\"path\":\"a.md\",\"oldText\":\"x\"}"}</tool_call>`),
		stopChunk)
	var bash struct{ Command string }
	if json.Unmarshal([]byte(calls["bash"]), &bash) != nil || bash.Command != "cd /e && for s in a b; do\n\techo \"$s\"; done" {
		t.Fatalf("bash %q\n%s", calls["bash"], raw)
	}
	if calls["edit"] != `{"oldText":"x","path":"a.md"}` || finish != "tool_calls" || strings.Contains(raw, "DSML") {
		t.Fatalf("calls %v finish %q\n%s", calls, finish, raw)
	}
}

// A block naming no tool the request offered, or none offered at all, or
// one cut off, stays the text it was.
func TestTextWrittenCallLeftAsText(t *testing.T) {
	block := `<tool_call>{"name":"rm_rf","arguments":{}}</tool_call>`
	text, calls, finish, _ := relayedChat(t, `{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}],`+textCallTools+`}`,
		chunkOf("Like this: "+block), stopChunk)
	if text != "Like this: "+block || len(calls) != 0 || finish != "stop" {
		t.Fatalf("unknown tool: text %q calls %v finish %q", text, calls, finish)
	}
	block = `<tool_call>{"name":"bash","arguments":{"command":"ls"}}</tool_call>`
	text, calls, finish, _ = relayedChat(t, `{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		chunkOf(block), stopChunk)
	if text != block || len(calls) != 0 || finish != "stop" {
		t.Fatalf("no tools: text %q calls %v finish %q", text, calls, finish)
	}
	cut := `<tool_call>{"name":"bash","arguments":{"command":"l`
	text, calls, finish, _ = relayedChat(t, `{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}],`+textCallTools+`}`,
		chunkOf(cut), chunkOf("s"), stopChunk)
	if text != cut+"s" || len(calls) != 0 || finish != "stop" {
		t.Fatalf("cut: text %q calls %v finish %q", text, calls, finish)
	}
	// text that only looks like a tag's start goes on whole
	text, _, _, _ = relayedChat(t, `{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}],`+textCallTools+`}`,
		chunkOf("a <tool"), chunkOf("tip> b"), stopChunk)
	if text != "a <tooltip> b" {
		t.Fatalf("lookalike: %q", text)
	}
}

// A native call keeps everything as it came: the text is not read for
// calls once the model has made one itself.
func TestTextWrittenCallBesideNative(t *testing.T) {
	text, calls, finish, _ := relayedChat(t, `{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}],`+textCallTools+`}`,
		chunkOf(`<tool_call>{"name":"bash","arguments":{"command":"ls"}}`),
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"edit","arguments":"{}"}}]},"finish_reason":null}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
	if text != `<tool_call>{"name":"bash","arguments":{"command":"ls"}}` || len(calls) != 1 || calls["edit"] != "{}" || finish != "tool_calls" {
		t.Fatalf("text %q calls %v finish %q", text, calls, finish)
	}
}

// DeepSeek's own DSML, translated for an Anthropic client: the call is a
// tool_use with its parameters, a string one as written and another read
// as JSON, and the turn stops for it.
func TestTextWrittenDSMLCallTranslated(t *testing.T) {
	fresh(t)
	up := sse(chunkOf("Running it.\n\n<｜DSML｜function_calls>\n<｜DSML｜invoke name=\"bash\">\n<｜DSML｜parameter name=\"command\" string=\"true\">ls -la</｜DSML｜parameter>\n"),
		chunkOf("<｜DSML｜parameter name=\"timeout\" string=\"false\">30</｜DSML｜parameter>\n</｜DSML｜invoke>\n</｜DSML｜function_calls>"),
		stopChunk, `data: [DONE]`)
	a := &scripted{replies: []reply{{200, "text/event-stream", up}}}
	scriptedOn(t, "a", provider.Chat, a)
	for _, stream := range []bool{true, false} {
		a.replies = []reply{{200, "text/event-stream", up}}
		rec := httptest.NewRecorder()
		s := "false"
		if stream {
			s = "true"
		}
		New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
			`{"model":"a/m","max_tokens":100,"stream":`+s+`,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"bash","input_schema":{"type":"object"}}]}`)))
		body := rec.Body.String()
		if rec.Code != 200 || strings.Contains(body, "DSML") || !strings.Contains(body, `"tool_use"`) ||
			!strings.Contains(body, "Running it.") || !strings.Contains(body, `"stop_reason":"tool_use"`) {
			t.Fatalf("stream=%v %d %s", stream, rec.Code, body)
		}
		if stream {
			if !strings.Contains(body, `{\"command\":\"ls -la\",\"timeout\":30}`) {
				t.Fatalf("arguments: %s", body)
			}
		} else if !strings.Contains(body, `"input":{"command":"ls -la","timeout":30}`) {
			t.Fatalf("arguments: %s", body)
		}
	}
}

// #823 again (Dazzle-sys, Trae CN's DeepSeek V4.1 Flash in Pi): a reply
// that was a <tool_call> with only DeepSeek's closing tags after it, cut
// short, reached the agent as that text. Tags and nothing else are
// dropped; a <tool_call> wrapping DeepSeek's own invoke is the call.
func TestTextWrittenTagsOnly(t *testing.T) {
	body := `{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}],` + textCallTools + `}`
	text, calls, finish, raw := relayedChat(t, body,
		chunkOf("<tool_call>\n</｜DSML｜parameter>\n"), chunkOf("</｜DSML｜invoke>\n</"), stopChunk)
	if text != "" || len(calls) != 0 || finish != "stop" || strings.Contains(raw, "DSML") {
		t.Fatalf("tags only: text %q calls %v finish %q\n%s", text, calls, finish, raw)
	}
	text, calls, finish, raw = relayedChat(t, body,
		chunkOf("看一下。<tool_call>\n<｜DSML｜invoke name=\"bash\">\n<｜DSML｜parameter name=\"command\" string=\"true\">ls</｜DSML｜parameter>\n</｜DSML｜invoke>\n</tool_call>"), stopChunk)
	if text != "看一下。" || calls["bash"] != `{"command":"ls"}` || finish != "tool_calls" || strings.Contains(raw, "DSML") || strings.Contains(raw, "tool_call>") {
		t.Fatalf("wrapped invoke: text %q calls %v finish %q\n%s", text, calls, finish, raw)
	}
	// text with a tag-like end stays
	text, _, _, _ = relayedChat(t, body, chunkOf("x </tool_call> y"), stopChunk)
	if text != "x </tool_call> y" {
		t.Fatalf("lookalike: %q", text)
	}
}

// A token-suffixed GLM block (<tool_calls:TOKEN><tool_call:TOKEN>name
// <arg_key:TOKEN>…</arg_key:TOKEN><arg_value:TOKEN>…</arg_value:TOKEN>
// </tool_call:TOKEN></tool_calls:TOKEN>, #6971, iskylite: workbuddy/hy4flash-free
// in ZeroWork) reaches the client as structured tool_calls after the text
// before it, with neither the wrapper nor the inner tags left in content.
func TestSuffixedTextWrittenCallRelayed(t *testing.T) {
	tools := `"tools":[{"type":"function","function":{"name":"host__agent_utasks","parameters":{"type":"object"}}},{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}}]`
	req := `{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}],` + tools + `}`

	// exact captured shape from #6971, split across SSE chunks mid-tag
	lead := " watermark 用错了，我重新查一下子任务当前状态，拿到正确编号再等报告。"
	text, calls, finish, raw := relayedChat(t, req,
		chunkOf(lead+"<tool_calls:6124"),
		chunkOf("c78e><tool_call:6124c78e>host__agent_utasks<arg_key:6124c78e>action</arg_key:6124c78e>"),
		chunkOf("<arg_value:6124c78e>list</arg_value:6124c78e></tool_call:6124c78e></tool_calls:6124c78e>"),
		stopChunk)
	if text != lead || calls["host__agent_utasks"] != `{"action":"list"}` || finish != "tool_calls" || strings.Contains(raw, "6124c78e") {
		t.Fatalf("suffixed stream: text %q calls %v finish %q\n%s", text, calls, finish, raw)
	}

	// non-streamed whole Chat reply with suffixed wrapper
	fresh(t)
	wholeUp := `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"` + lead + `<tool_calls:6124c78e><tool_call:6124c78e>host__agent_utasks<arg_key:6124c78e>action</arg_key:6124c78e><arg_value:6124c78e>list</arg_value:6124c78e></tool_call:6124c78e></tool_calls:6124c78e>"},"finish_reason":"stop"}]}`
	a := &scripted{replies: []reply{{200, "application/json", wholeUp}}}
	scriptedOn(t, "a", provider.Chat, a)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(strings.Replace(req, `"stream":true`, `"stream":false`, 1))))
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "6124c78e") || !strings.Contains(rec.Body.String(), `"finish_reason":"tool_calls"`) || !strings.Contains(rec.Body.String(), `"host__agent_utasks"`) {
		t.Fatalf("suffixed whole: %d %s", rec.Code, rec.Body.String())
	}

	// native tool_calls already emitted: leftover suffixed wrapper tags go,
	// and a double-emitted text copy is never turned into a second call
	text, calls, finish, raw = relayedChat(t, req,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"bash","arguments":"{}"}}]},"finish_reason":null}]}`,
		chunkOf("\n<tool_calls:6124c78e></tool_calls:6124c78e>"),
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
	if text != "" || len(calls) != 1 || calls["bash"] != "{}" || finish != "tool_calls" || strings.Contains(raw, "6124c78e") {
		t.Fatalf("native leftover wrapper: text %q calls %v finish %q\n%s", text, calls, finish, raw)
	}

	dup := `<tool_calls:6124c78e><tool_call:6124c78e>host__agent_utasks<arg_key:6124c78e>action</arg_key:6124c78e><arg_value:6124c78e>list</arg_value:6124c78e></tool_call:6124c78e></tool_calls:6124c78e>`
	text, calls, finish, _ = relayedChat(t, req,
		chunkOf(dup),
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"bash","arguments":"{}"}}]},"finish_reason":null}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
	if text != dup || len(calls) != 1 || calls["bash"] != "{}" || finish != "tool_calls" {
		t.Fatalf("double emit: text %q calls %v finish %q", text, calls, finish)
	}

	// history cleanup (dropPseudoCalls) strips a suffixed completion wrapper
	msgs := dropPseudoCalls([]Message{{
		Role:  "assistant",
		Parts: []Part{{Kind: Text, Text: "Done.\n<tool_calls:6124c78e><tool_call:6124c78e>finish<arg_key:6124c78e>result</arg_key:6124c78e><arg_value:6124c78e>all checks green</arg_value:6124c78e></tool_call:6124c78e></tool_calls:6124c78e>\nSummary: all checks green."}},
	}}, []Tool{{Name: "finish"}})
	if len(msgs) != 1 || len(msgs[0].Parts) != 1 || strings.Contains(msgs[0].Parts[0].Text, "6124c78e") || !strings.Contains(msgs[0].Parts[0].Text, "Summary: all checks green.") {
		t.Fatalf("dropPseudoCalls suffixed: %+v", msgs)
	}

	// lookalike identifiers and unclosed <tool_call: text pass through intact
	// two suffixed calls inside one <tool_calls:TOKEN> wrapper
	text, calls, finish, raw = relayedChat(t, req,
		chunkOf("<tool_calls:ab_1><tool_call:ab_1>host__agent_utasks<arg_key:ab_1>action</arg_key:ab_1><arg_value:ab_1>list</arg_value:ab_1></tool_call:ab_1><tool_call:ab_1>bash<arg_key:ab_1>cmd</arg_key:ab_1><arg_value:ab_1>pwd</arg_value:ab_1></tool_call:ab_1></tool_calls:ab_1>"),
		stopChunk)
	if text != "" || calls["host__agent_utasks"] != `{"action":"list"}` || calls["bash"] != `{"cmd":"pwd"}` || finish != "tool_calls" || strings.Contains(raw, "ab_1") {
		t.Fatalf("two suffixed calls: text %q calls %v finish %q\n%s", text, calls, finish, raw)
	}

	for _, plain := range []string{"see <tool_call_id> here", "note <tool_call: not a tag", "see <tool_call:foo"} {
		text, calls, finish, _ = relayedChat(t, req, chunkOf(plain[:7]), chunkOf(plain[7:]), stopChunk)
		if text != plain || len(calls) != 0 || finish != "stop" {
			t.Fatalf("lookalike %q: text %q calls %v finish %q", plain, text, calls, finish)
		}
	}
}
