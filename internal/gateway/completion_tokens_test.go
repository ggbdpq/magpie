package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// completionTokens plays an OpenAI-compatible API whose reasoning models
// take a reply's length as max_completion_tokens alone and turn max_tokens
// away by name (#4261: gpt-6.1-sol on api.openai.com answers 400
// "Unsupported parameter: 'max_tokens' is not supported with this model.
// Use 'max_completion_tokens' instead."). words is the 400 it answers a
// request naming max_tokens with, so a negative can name another.
type completionTokensUp struct {
	mu    sync.Mutex
	calls []bedrockCall
	words string
}

func (u *completionTokensUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var m map[string]any
	json.Unmarshal(body, &m)
	u.mu.Lock()
	u.calls = append(u.calls, bedrockCall{r.URL.Path, r.Header.Clone(), "", m})
	u.mu.Unlock()
	if _, ok := m["max_tokens"]; ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, u.words)
		return
	}
	if stream, _ := m["stream"].(bool); stream {
		// the translator asks streamed, whatever the client asked
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"from openai"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
			`data: [DONE]`))
		return
	}
	io.WriteString(w, `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"from openai"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
}

func (u *completionTokensUp) at(i int) bedrockCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls[i]
}

func (u *completionTokensUp) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.calls)
}

func (u *completionTokensUp) last() bedrockCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls[len(u.calls)-1]
}

// postPath sends body to path on one gateway, as postAs does for a chat.
func postPath(t *testing.T, s *Server, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// A chat client's max_tokens, turned away by name, is asked again with the
// reply's length as max_completion_tokens (#4261), and from then on under
// that name from the start, without the round trip.
func TestCompletionTokensAskedAgainOnChat(t *testing.T) {
	fresh(t)
	up := &completionTokensUp{words: `{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","param":"max_tokens","code":"unsupported_parameter"}}`}
	serveOn(t, "openai", "sk", []string{"gpt-6.1-sol"}, up)
	s := New()
	chat := `{"model":"openai/gpt-6.1-sol","max_tokens":20,"messages":[{"role":"user","content":"hi"}]}`
	code, body := postPath(t, s, "/v1/chat/completions", chat)
	if code != 200 || !strings.Contains(body, "from openai") {
		t.Fatalf("the rewritten request wasn't answered: %d %s", code, body)
	}
	if _, ok := up.at(0).body["max_tokens"]; !ok {
		t.Fatalf("the first ask wasn't as the client sent it: %v", up.at(0).body)
	}
	if _, ok := up.at(1).body["max_tokens"]; ok || up.at(1).body["max_completion_tokens"] != float64(20) {
		t.Fatalf("asked again as: %v", up.at(1).body)
	}
	code, body = postPath(t, s, "/v1/chat/completions", chat)
	if code != 200 || !strings.Contains(body, "from openai") {
		t.Fatalf("the remembered rewrite wasn't answered: %d %s", code, body)
	}
	if up.count() != 3 {
		t.Fatalf("two chats took %d asks", up.count())
	}
	if _, ok := up.last().body["max_tokens"]; ok || up.last().body["max_completion_tokens"] != float64(20) {
		t.Fatalf("asked again as: %v", up.last().body)
	}
}

// A Claude client's max_tokens reaches the same model through the
// translator, and is asked again under the name it takes there too.
func TestCompletionTokensAskedAgainOnMessages(t *testing.T) {
	fresh(t)
	up := &completionTokensUp{words: `{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","param":"max_tokens","code":"unsupported_parameter"}}`}
	serveOn(t, "openai", "sk", []string{"gpt-6.1-sol"}, up)
	code, body := postPath(t, New(), "/v1/messages", `{"model":"openai/gpt-6.1-sol","max_tokens":20,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from openai") {
		t.Fatalf("the rewritten request wasn't answered: %d %s", code, body)
	}
	if _, ok := up.at(1).body["max_tokens"]; ok || up.at(1).body["max_completion_tokens"] != float64(20) {
		t.Fatalf("asked again as: %v", up.at(1).body)
	}
}

// A client already naming max_completion_tokens is answered on the first
// ask, and a 400 over something else than the parameter's name stands as
// the vendor said it, untouched.
func TestCompletionTokensOtherRefusalStands(t *testing.T) {
	fresh(t)
	up := &completionTokensUp{words: `{"error":{"message":"Invalid 'max_tokens': integer below minimum value.","param":"max_tokens","code":"integer_below_min"}}`}
	serveOn(t, "openai", "sk", []string{"gpt-6.1-sol"}, up)
	code, body := postPath(t, New(), "/v1/chat/completions", `{"model":"openai/gpt-6.1-sol","max_completion_tokens":20,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from openai") {
		t.Fatalf("not answered as asked: %d %s", code, body)
	}
	if up.count() != 1 {
		t.Fatalf("asked %d times", up.count())
	}
	code, body = postPath(t, New(), "/v1/chat/completions", `{"model":"openai/gpt-6.1-sol","max_tokens":2,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 400 || !strings.Contains(body, "integer below minimum value") {
		t.Fatalf("the vendor's refusal wasn't the client's: %d %s", code, body)
	}
	if up.count() != 2 {
		t.Fatalf("asked %d times over the refused one", up.count())
	}
}
