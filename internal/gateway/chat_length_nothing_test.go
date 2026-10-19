package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A Chat reply cut at its length that said nothing — the whole budget
// spent on reasoning, no text and no call — is not a completed reply.
// qoder/dfmodel ended four replies of one dsh turn so (#2972): 48,000
// output tokens each, every one of them reasoning, and the agent, told
// the output limit was reached, went on at the user's "continue" and
// burned the same void again. Both gates an empty reply fails at let it
// through: emptyFails is not for Chat, and answersNothing spares a cut
// at its length. It fails as an empty reply (#667's) now, asked again
// first. One that said something still goes as it came, and a title cut
// short before its text is still the response.incomplete Codex asks
// again after (#1466).
func TestChatLengthNothingSaidIsAnError(t *testing.T) {
	fresh(t)
	old := retryPause
	retryPause = time.Millisecond
	t.Cleanup(func() { retryPause = old })
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"let me think"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":48000}}`,
			`data: [DONE]`))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "qo", Name: "Qoder", Key: "k", Models: []string{"dfmodel"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}

	// streamed: asked again, then the error in the stream — never a reply
	// ended as it should with nothing in it
	for _, a := range []struct{ name, path, body, ended string }{
		{"anthropic", "/v1/messages", `{"model":"qo/dfmodel","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`, `"stop_reason":"max_tokens"`},
		{"responses", "/v1/responses", `{"model":"qo/dfmodel","stream":true,"input":"hi"}`, `response.incomplete`},
	} {
		calls = 0
		code, body := post(t, a.path, a.body)
		if strings.Contains(body, a.ended) || !strings.Contains(body, "an empty reply") || calls < 2 {
			t.Errorf("%s: a void cut answered %d after %d calls, not the empty-reply error: %s", a.name, code, calls, body)
		}
	}

	// not streamed: the same error, not a reply whole with nothing in it
	calls = 0
	code, body := post(t, "/v1/messages", `{"model":"qo/dfmodel","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 502 || !strings.Contains(body, "an empty reply") {
		t.Errorf("a void cut, not streamed: %d %s", code, body)
	}
}

// A length cut that said something is the truncation it always was, told
// to the client as such.
func TestChatLengthWithTextIsTruncation(t *testing.T) {
	fresh(t)
	old := retryPause
	retryPause = time.Millisecond
	t.Cleanup(func() { retryPause = old })
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Half an ans"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":48000}}`,
			`data: [DONE]`))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "qo", Name: "Qoder", Key: "k", Models: []string{"dfmodel"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []struct{ name, path, body, ended string }{
		{"anthropic", "/v1/messages", `{"model":"qo/dfmodel","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`, `"stop_reason":"max_tokens"`},
		{"responses", "/v1/responses", `{"model":"qo/dfmodel","stream":true,"input":"hi"}`, `response.incomplete`},
	} {
		calls = 0
		code, body := post(t, a.path, a.body)
		if code != 200 || !strings.Contains(body, "Half an ans") || !strings.Contains(body, a.ended) ||
			strings.Contains(body, "an empty reply") || calls != 1 {
			t.Errorf("%s: a cut that said something answered %d after %d calls: %s", a.name, code, calls, body)
		}
	}

	calls = 0
	code, body := post(t, "/v1/messages", `{"model":"qo/dfmodel","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "Half an ans") || !strings.Contains(body, `"stop_reason":"max_tokens"`) ||
		strings.Contains(body, "an empty reply") || calls != 1 {
		t.Errorf("a cut that said something, not streamed: %d after %d calls: %s", code, calls, body)
	}
}
