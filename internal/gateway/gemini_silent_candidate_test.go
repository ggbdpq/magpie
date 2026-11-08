package gateway

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// A Code Assist (Antigravity) reply that ends its stream with a candidate
// that carries no parts and no finishReason — the model answered nothing,
// now and then right after a tool's result — went to the agent as a reply
// cut short ("the reply ended before it was complete"): asked again as a
// transport failure, the whole 200 KB prompt resent several times over,
// then a 502 the agent spends its own ten retries on — minutes of the
// same 0-token ask, ~60 upstream calls in a reporter's session (#5455,
// JZongf). Such a candidate says the reply is over with nothing said in
// it, as a reply with no candidate at all does (#667): the empty reply's
// to answer, asked again on the same account and failed as that error,
// not a cut one's. The reporter's wire shape: one ~487-byte chunk with
// usageMetadata (in: 2049, out: 0) and a role-only candidate, then the
// stream closes.

// silentCandidates is the shapes on the wire: the Gemini API's bare chunk
// and Code Assist's, wrapped in a response, and a candidate that is an
// empty object outright.
var silentCandidates = map[string]string{
	"role only":   `data: {"candidates":[{"content":{"role":"model"}}],"usageMetadata":{"promptTokenCount":2049,"candidatesTokenCount":0,"thoughtsTokenCount":0,"cachedContentTokenCount":52607}}` + "\n\n",
	"wrapped":     `data: {"response":{"candidates":[{"content":{"role":"model"}}],"usageMetadata":{"promptTokenCount":2049,"candidatesTokenCount":0,"thoughtsTokenCount":0}}}` + "\n\n",
	"bare object": `data: {"candidates":[{}],"usageMetadata":{"promptTokenCount":4098,"candidatesTokenCount":0}}` + "\n\n",
}

func askSilentCandidate(t *testing.T, reply, path, body string) (*fake, int, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	f := &fake{t: t}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	saveGem(t, up.URL, "gemini-4-argon")
	f.reply = reply
	code, out := post(t, path, body)
	return f, code, out
}

func TestGeminiSilentCandidateIsEmptyReply(t *testing.T) {
	for what, reply := range silentCandidates {
		for _, a := range []struct{ name, path, body string }{
			{"anthropic", "/v1/messages", `{"model":"gem/gemini-4-argon","stream":true,"max_tokens":256,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"wait","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"matched: []"}]}]}`},
			{"chat", "/v1/chat/completions", `{"model":"gem/gemini-4-argon","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		} {
			f, code, body := askSilentCandidate(t, reply, a.path, a.body)
			if strings.Contains(body, "ended before it was complete") {
				t.Errorf("%s, %s: the silent candidate read as a cut: %d %s", what, a.name, code, body)
			}
			if !strings.Contains(body, "an empty reply") {
				t.Errorf("%s, %s: not the empty reply's error: %d %s", what, a.name, code, body)
			}
			if f.calls < 2 {
				t.Errorf("%s, %s: asked %d times, not again", what, a.name, f.calls)
			}
		}
		// a client that asked for no stream is told the same error
		_, code, body := askSilentCandidate(t, reply, "/v1/chat/completions",
			`{"model":"gem/gemini-4-argon","messages":[{"role":"user","content":"hi"}]}`)
		if code != 502 || !strings.Contains(body, "an empty reply") || strings.Contains(body, "ended before") {
			t.Errorf("%s, not streamed: %d %s", what, code, body)
		}
	}
}

// A reply that has the silent candidate among its chunks, but said
// something before it, is no empty reply: it goes as it came.
func TestGeminiTextThenSilentCandidateIsSaid(t *testing.T) {
	reply := `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Done."}]}}]}` + "\n\n" +
		`data: {"candidates":[{"content":{"role":"model"}}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":3}}` + "\n\n"
	_, code, body := askSilentCandidate(t, reply, "/v1/chat/completions",
		`{"model":"gem/gemini-4-argon","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "Done.") || strings.Contains(body, "an empty reply") {
		t.Errorf("text then silent candidate: %d %s", code, body)
	}
}
