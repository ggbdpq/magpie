package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The streaming face of the native Gemini route relays a request as it
// came, and #7702 (l1155665) was shown its conversation ending with the
// model's turn refused by the upstream: "Requests ending with a model
// turn are not supported". The non-streaming face is translated, and its
// build appends a user turn after the model's (buildCodeAssistSent); the
// streaming face asks with the same turn after it.
func TestGeminiStreamNeverAskedEndingWithModel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var mu sync.Mutex
	var bodies []map[string]any
	refused := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var q map[string]any
		json.Unmarshal(b, &q)
		mu.Lock()
		bodies = append(bodies, q)
		mu.Unlock()
		if code, out := vertexTurnsAway(0, q); code != http.StatusOK {
			mu.Lock()
			refused++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			io.WriteString(w, out)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hello-"}]}}],"modelVersion":"gemini-2.5-pro"}`,
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"from-gemini"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}}`,
		))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "gem", Name: "Gem", Key: "AIza-test", Gemini: up.URL, Models: []string{"gemini-2.5-pro"}}); err != nil {
		t.Fatal(err)
	}
	const path = "/v1beta/models/gem/gemini-2.5-pro:streamGenerateContent?alt=sse"
	const tools = `,"tools":[{"functionDeclarations":[{"name":"shell","description":"run","parameters":{"type":"object","properties":{"command":{"type":"string"}}}}]}]`
	for _, c := range []struct {
		name, body, want string
	}{
		// #7702's curl: the tool-continuation request OpenCode sends
		{"a turn ending with the model's call", `{"contents":[{"role":"user","parts":[{"text":"run ls"}]},{"role":"model","parts":[{"functionCall":{"name":"shell","args":{"command":"ls"},"id":"c1"}}]}]` + tools + `}`, continueText},
		// #7702 shape D: a trailing signature-only model content
		{"a trailing signature-only model turn", `{"contents":[{"role":"user","parts":[{"text":"run ls"}]},{"role":"model","parts":[{"text":"ran ls"}]},{"role":"model","parts":[{"thoughtSignature":"sig"}]}]` + tools + `}`, continueText},
		{"a compaction asked in the system instruction", `{"systemInstruction":{"parts":[{"text":"You are a helpful AI assistant tasked with summarizing conversations."}]},"contents":[{"role":"user","parts":[{"text":"fix the bug"}]},{"role":"model","parts":[{"text":"Fixed it in main.go."}]}]}`, compactLastText},
	} {
		code, body := post(t, path, c.body)
		if code != http.StatusOK || strings.Contains(body, "model turn") {
			t.Errorf("%s: %d %s", c.name, code, body)
			continue
		}
		mu.Lock()
		last, sent, no := bodies[len(bodies)-1], len(bodies), refused
		mu.Unlock()
		// asked once, and never in a shape the upstream turns away
		if sent == 0 || no != 0 {
			t.Errorf("%s: the upstream was asked %d times, %d of them refused", c.name, sent, no)
		}
		role, text := lastTurn(t, last["contents"])
		if role != "user" || !strings.Contains(text, c.want) {
			t.Errorf("%s: the upstream was asked ending with %s %q, want user %q", c.name, role, text, c.want)
		}
	}
	// a conversation that already ends with the user's turn is relayed as
	// it came
	code, body := post(t, path, `{"contents":[{"role":"user","parts":[{"text":"hi there"}]}]}`)
	if code != http.StatusOK || !strings.Contains(body, "from-gemini") {
		t.Fatalf("a user-ended conversation: %d %s", code, body)
	}
	mu.Lock()
	last := bodies[len(bodies)-1]
	mu.Unlock()
	if role, text := lastTurn(t, last["contents"]); role != "user" || text != "hi there" {
		t.Errorf("a user-ended conversation ends with %s %q", role, text)
	}
}
