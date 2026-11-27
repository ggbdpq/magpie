package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// poeRefusal is Poe's 400 for a body carrying Gemini's google fields,
// behind OmniRoute (#7821, #7935).
const poeRefusal = `[400]: This bot does not support these request fields: google`

// omniRouter is a local multi-backend router (OmniRoute) behind one
// address, serving poe/gemini-4-hydrogen — a Gemini-looking id on a
// loopback host, which geminiCompat takes for Gemini's own — and g4h, an
// alias of the same backend model with no gemini in it. Either backend
// answers a web_search tool call, then text; any body carrying a google
// field (extra_body, extra_content) is refused the way Poe refuses one.
type omniRouter struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (o *omniRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var q map[string]any
	json.Unmarshal(b, &q)
	model, _ := q["model"].(string)
	hasGoogle := bytes.Contains(b, []byte(`"extra_body"`)) || bytes.Contains(b, []byte(`"extra_content"`))
	toolRound := false
	if ts, ok := q["tools"].([]any); ok {
		for _, tt := range ts {
			if m, ok := tt.(map[string]any); ok {
				if fn, ok := m["function"].(map[string]any); ok && fn["name"] == "web_search" {
					toolRound = true
				}
			}
		}
	}
	hasToolResult := false
	if ms, ok := q["messages"].([]any); ok && len(ms) > 0 {
		if last, ok := ms[len(ms)-1].(map[string]any); ok && last["role"] == "tool" {
			hasToolResult = true
		}
	}
	o.mu.Lock()
	o.bodies = append(o.bodies, q)
	o.mu.Unlock()
	if model == "poe/gemini-4-hydrogen" && hasGoogle {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, poeRefusal)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if toolRound && !hasToolResult {
		io.WriteString(w, sse(
			`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"IANA example domains\"}"}}]}}]}`,
			`data: {"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":30,"completion_tokens":7}}`,
			`data: [DONE]`))
		return
	}
	io.WriteString(w, sse(
		`data: {"id":"d","choices":[{"index":0,"delta":{"role":"assistant","content":"Example.com is IANA's reserved domain."}}]}`,
		`data: {"id":"d","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		`data: [DONE]`))
}

func (o *omniRouter) sent() []map[string]any {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]map[string]any(nil), o.bodies...)
}

// A Gemini-named model on a local router whose backend refuses Gemini's
// google fields (#7821) had the refusal carried into the thinking-config
// fallback alone: the web_search call replayed after the search still
// went out with extra_content.google, the backend refused it the same
// way, and the turn ended response.failed server_error after the search
// had completed (#7935, aron-intframe). The learned refusal now takes the
// google fields off the replayed calls; the same model under an alias
// with no gemini in it never carried them, as before.
func TestWebSearchReplayAfterGoogleRefused(t *testing.T) {
	fresh(t)
	up := &omniRouter{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	apis := newSearchAPIs(t)

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // no signed-in agent searches
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	if err := provider.Save(provider.Provider{
		ID: "home-c", Name: "OmniRoute", Key: "k",
		Models: []string{"poe/gemini-4-hydrogen", "g4h"},
		Chat:   srv.URL,
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetSearchAPI(apis.api("tavily", "tvly-k")); err != nil {
		t.Fatal(err)
	}
	s := New()
	ask := func(model string) (int, string) {
		return postTo(t, s, "/v1/responses", `{"model":"home-c/`+model+`","stream":true,
			"reasoning":{"effort":"high","summary":"auto"},
			"input":[{"role":"user","content":"Use built-in web search once for IANA example domains, then give me one sourced fact."}],
			"tools":[{"type":"web_search"}]}`)
	}

	code, out := ask("poe/gemini-4-hydrogen")
	if code != 200 || !strings.Contains(out, "Example.com is IANA's reserved domain.") || strings.Contains(out, "response.failed") {
		t.Fatalf("the searched turn failed on the refused route: %d %s", code, out)
	}
	var searched []map[string]any
	for _, b := range up.sent() {
		if _, ok := b["messages"]; ok {
			searched = append(searched, b)
		}
	}
	if len(searched) < 3 {
		t.Fatalf("the searched turn asked %d times: %v", len(searched), searched)
	}
	last := searched[len(searched)-1]
	if _, ok := last["extra_content"]; ok {
		t.Fatal("the replayed search call went out with extra_content")
	}
	if _, ok := last["extra_body"]; ok {
		t.Fatal("the ask after the search went out with extra_body")
	}

	// the alias with no gemini in it is no Gemini-looking route: it never
	// carried the google fields, and still completes
	code, out = ask("g4h")
	if code != 200 || !strings.Contains(out, "Example.com is IANA's reserved domain.") {
		t.Fatalf("the alias failed: %d %s", code, out)
	}
}
