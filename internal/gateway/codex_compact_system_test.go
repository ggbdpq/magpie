package gateway

import (
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// chatgptSystem400 is what the ChatGPT backend answers a request carrying
// the system role with, passed through the relay above it unchanged
// (jian-cui's sub2api, #5067).
const chatgptSystem400 = `{"detail":"System messages are not allowed"}`

// #5067: Codex on a plugin provider whose Responses endpoint is a relay
// of a ChatGPT account: the summary ask went out with its developer
// messages as system ones (forVendor, #2955) and the backend behind the
// relay refused every one, 400 "System messages are not allowed", three
// of the failures holding the conversation (compactFails) — while the
// turns around it, developer as they came, are taken. The summary is
// asked again with the roles as the client sent them, and the backend
// takes it the way it takes the turns.
func TestCodexCompactSystemRefusedAsksAgainDeveloper(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"resp_v1","status":"in_progress","output":[]}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"SUMMARY"}]}}`,
		`data: {"type":"response.completed","response":{"id":"resp_v1","status":"completed","output":[]}}`)}
	f.refuse = func(body []byte) (int, string) {
		if strings.Contains(string(body), `"role":"system"`) {
			return http.StatusBadRequest, chatgptSystem400
		}
		return 0, ""
	}
	setup(t, provider.Responses, f)
	code, body := codexPost(t, `{"model":"fake/m1","stream":true,"store":false,
	  "input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"You are Codex, a coding agent."}]},
	  {"type":"message","role":"user","content":[{"type":"input_text","text":"fix the login bug"}]},
	  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"I fixed main.go"}]},
	  {"type":"compaction_trigger"}]}`)
	if code != 200 || !strings.Contains(body, `"compaction"`) {
		t.Fatalf("%d %s\nupstream got %s", code, body, f.got)
	}
	if got := compactedText(t, body); got != "SUMMARY" {
		t.Errorf("summary %q", got)
	}
	if f.calls != 2 {
		t.Errorf("asks: %d", f.calls)
	}
	// the second ask went with the roles as the client sent them:
	// developer, never system
	if strings.Contains(string(f.got), `"role":"system"`) {
		t.Errorf("system went over the wire again: %s", f.got)
	}
	if !strings.Contains(string(f.got), `"role":"developer"`) {
		t.Errorf("the retry lost the developer message: %s", f.got)
	}
}
