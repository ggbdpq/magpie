package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// cragVendor mimics the private Anthropic-compatible upstream of #6914
// (chiyan171): its gateway kernel reads the request's cache controls before
// dispatch, and turns a Messages request, or a count_tokens one, away with
//
//	"Gateway kernel rejected the request cache controls before dispatch
//	(CRAG_CACHE_CONTROL)"
//
// when a tool_use block carries both the caller it gave the call and the
// cache_control the client put on the block. Anything else it answers: a
// plain message, or the count.
type cragVendor struct {
	mu     sync.Mutex
	raw    [][]byte
	bodies []map[string]any
}

// cragRefused reports whether body, a Messages request as it reached the
// vendor, has a tool_use block that carries a caller and a cache_control
// together: the pair #6914's reporter saw the kernel refuse.
func cragRefused(body map[string]any) bool {
	msgs, _ := body["messages"].([]any)
	for _, mi := range msgs {
		m, ok := mi.(map[string]any)
		if !ok {
			continue
		}
		blocks, _ := m["content"].([]any)
		for _, bi := range blocks {
			b, ok := bi.(map[string]any)
			if !ok || b["type"] != "tool_use" {
				continue
			}
			if b["caller"] != nil && b["cache_control"] != nil {
				return true
			}
		}
	}
	return false
}

func (v *cragVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	json.Unmarshal(b, &m)
	v.mu.Lock()
	v.raw = append(v.raw, b)
	v.bodies = append(v.bodies, m)
	v.mu.Unlock()
	if cragRefused(m) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Gateway kernel rejected the request cache controls before dispatch (CRAG_CACHE_CONTROL)"}}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(r.URL.Path, "/count_tokens") {
		io.WriteString(w, `{"input_tokens":42}`)
		return
	}
	io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
}

// lastBody is the request the vendor answered last, its bytes as they came.
func (v *cragVendor) lastBody() []byte {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.raw[len(v.raw)-1]
}

// last is the body of the request the vendor answered last, read.
func (v *cragVendor) last() map[string]any {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.bodies[len(v.bodies)-1]
}

// callerCallBody is the conversation Claude Code holds when #6914's trigger
// is in it (chiyan171): the upstream answered the first call from a plain
// run, the second from one that named a caller, and Claude Code marked the
// blocks of the history it keeps — the two calls included — for its prompt
// cache. The reporter's gateway kernel turns the request away over the
// caller's marked call alone: Claude Desktop, which marks the same way but
// echoes no caller, works, as does a request whose marked calls came
// without one. The turn was retried 7 times and every try failed the same.
const callerCallBody = `{"model":"relay/claude-sonnet-5-5","max_tokens":32000,"stream":false,` +
	`"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude.","cache_control":{"type":"ephemeral"}}],` +
	`"tools":[` +
	`{"name":"Bash","description":"Run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}},` +
	`{"name":"Read","description":"Read a file","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}},"cache_control":{"type":"ephemeral"}}],` +
	`"metadata":{"user_id":"user_abc_account__session_1"},` +
	`"messages":[` +
	`{"role":"user","content":[{"type":"text","text":"list the files and read go.mod"}]},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01A","name":"Bash","input":{"command":"ls"},"cache_control":{"type":"ephemeral"}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01A","content":"go.mod\nmain.go"}]},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01B","name":"Bash","input":{"command":"cat go.mod"},"caller":{"type":"direct"},"cache_control":{"type":"ephemeral"}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01B","content":"module x"},{"type":"text","text":"now build it","cache_control":{"type":"ephemeral"}}]}` +
	`]}`

// the caller's call, as Claude Code sent it and as the vendor is to read it
// back: the mark alone gone, every other byte of the block as written.
const callerCallUnmarked = `{"type":"tool_use","id":"toolu_01B","name":"Bash","input":{"command":"cat go.mod"},"caller":{"type":"direct"}}`

// the same block as the client sent it, its mark in it.
const callerCallMarked = `{"type":"tool_use","id":"toolu_01B","name":"Bash","input":{"command":"cat go.mod"},"caller":{"type":"direct"},"cache_control":{"type":"ephemeral"}}`

// blockOf is the message block named id, from the messages a vendor's
// request carried.
func blockOf(t *testing.T, body map[string]any, id string) map[string]any {
	t.Helper()
	msgs, _ := body["messages"].([]any)
	for _, mi := range msgs {
		m, ok := mi.(map[string]any)
		if !ok {
			continue
		}
		blocks, _ := m["content"].([]any)
		for _, bi := range blocks {
			b, ok := bi.(map[string]any)
			if !ok {
				continue
			}
			if b["id"] == id {
				return b
			}
		}
	}
	t.Fatalf("no block %s in %v", id, body)
	return nil
}

// The reporter's turn: a caller's marked call reaches the gateway as Claude
// Code sent it, the kernel turns the request away, and the turn fails — 7
// tries and out (chiyan171). The mark comes off the caller's call before
// the request goes, the marks around it stand, and the upstream answers.
func TestCallerCallRelayedUnmarked(t *testing.T) {
	fresh(t)
	up := &cragVendor{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: srv.URL,
		Models: []string{"claude-sonnet-5-5"}}); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/messages", callerCallBody)
	t.Logf("the vendor read: %s", up.lastBody())
	if code != 200 {
		t.Fatalf("POST /v1/messages = %d, want 200: %s", code, body)
	}
	sent := up.last()
	// the caller's call goes unmarked, its caller and input as they came
	call := blockOf(t, sent, "toolu_01B")
	if _, marked := call["cache_control"]; marked {
		t.Fatalf("the caller's call kept its mark: %v", call)
	}
	if call["caller"] == nil {
		t.Fatalf("the caller was taken off: %v", call)
	}
	if call["input"] == nil {
		t.Fatalf("the call's input was dropped: %v", call)
	}
	// and as bytes: the block the vendor read is the block the client sent
	// with the mark alone taken off it
	if !bytes.Contains(up.lastBody(), []byte(callerCallUnmarked)) {
		t.Fatalf("the caller's block did not reach the vendor as sent, unmarked:\n%s", up.lastBody())
	}
	if bytes.Contains(up.lastBody(), []byte(`"caller":{"type":"direct"},"cache_control"`)) {
		t.Fatalf("the mark still rides on the caller's block:\n%s", up.lastBody())
	}
	// a marked call of its own, no caller, keeps its mark: the fix is the
	// caller's pair alone, not the request's marks at large
	plain := blockOf(t, sent, "toolu_01A")
	if plain["cache_control"] == nil {
		t.Fatalf("a marked call without a caller lost its mark: %v", plain)
	}
	// the marks around the call stand: the tool's, the system's, the tail's
	var kept []any
	kept, _ = sent["tools"].([]any)
	if len(kept) != 2 || kept[1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("the marked tool lost its mark: %v", kept)
	}
	kept, _ = sent["system"].([]any)
	if len(kept) != 1 || kept[0].(map[string]any)["cache_control"] == nil {
		t.Fatalf("the marked system lost its mark: %v", kept)
	}
	msgs, _ := sent["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	tail, _ := last["content"].([]any)
	if tail[1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("the marked tail lost its mark: %v", tail)
	}
}

// counted as it will be sent: the count the gateway asks reads the request
// as dispatch will see it, the caller's call unmarked (#6914).
func TestCallerCallCountedUnmarked(t *testing.T) {
	fresh(t)
	up := &cragVendor{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: srv.URL,
		Models: []string{"claude-sonnet-5-5"}}); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/messages/count_tokens", callerCallBody)
	t.Logf("the count's vendor read: %s", up.lastBody())
	if code != 200 {
		t.Fatalf("POST /v1/messages/count_tokens = %d, want 200: %s", code, body)
	}
	sent := up.last()
	if _, marked := blockOf(t, sent, "toolu_01B")["cache_control"]; marked {
		t.Fatalf("the count read the caller's call marked: %v", sent)
	}
	if blockOf(t, sent, "toolu_01A")["cache_control"] == nil {
		t.Fatalf("the count read a marked call without a caller unmarked: %v", sent)
	}
	if !bytes.Contains(up.lastBody(), []byte(callerCallUnmarked)) {
		t.Fatalf("the count's request did not carry the caller's block unmarked:\n%s", up.lastBody())
	}
}

// a request without the reported pair goes as it came: a caller's call
// without a mark is nothing to take off, and neither is a marked
// server_tool_use, whose caller names a server's run, not a call of the
// conversation's history.
func TestCallerCallUntouchedWithoutThePair(t *testing.T) {
	fresh(t)
	up := &cragVendor{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: srv.URL,
		Models: []string{"claude-sonnet-5-5"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, call, want string
	}{
		{"a caller's call without a mark",
			`{"type":"tool_use","id":"toolu_01B","name":"Bash","input":{"command":"cat go.mod"},"caller":{"type":"direct"}}`,
			`{"type":"tool_use","id":"toolu_01B","name":"Bash","input":{"command":"cat go.mod"},"caller":{"type":"direct"}}`},
		{"a marked server_tool_use",
			`{"type":"server_tool_use","id":"call_00_x","name":"web_search","input":{"query":"go.mod"},"caller":{"type":"direct"},"cache_control":{"type":"ephemeral"}}`,
			`{"type":"server_tool_use","id":"call_00_x","name":"web_search","input":{"query":"go.mod"},"caller":{"type":"direct"},"cache_control":{"type":"ephemeral"}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := strings.Replace(callerCallBody, callerCallMarked, c.call, 1)
			code, said := post(t, "/v1/messages", body)
			if code != 200 {
				t.Fatalf("POST /v1/messages = %d, want 200: %s", code, said)
			}
			if !bytes.Contains(up.lastBody(), []byte(c.want)) {
				t.Fatalf("the block did not reach the vendor as sent:\n%s", up.lastBody())
			}
		})
	}
}
