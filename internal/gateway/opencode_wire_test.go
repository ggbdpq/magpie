package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// openCodeGoCatalog is models.dev's opencode-go as it lists the models of
// #1215 (https://models.dev/api.json, 2026-10-08): GPT, Grok and Muse
// Spark through @ai-sdk/openai, MiniMax through @ai-sdk/anthropic, GLM
// with no provider of its own (the provider's @ai-sdk/openai-compatible).
const openCodeGoCatalog = `{
  "opencode-go": {"id":"opencode-go","npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/zen/go/v1","models": {
    "gpt-6-luna": {"id":"gpt-6-luna","name":"GPT-6 Luna","provider":{"npm":"@ai-sdk/openai"}},
    "grok-4.7": {"id":"grok-4.7","name":"Grok 4.7","provider":{"npm":"@ai-sdk/openai"}},
    "muse-spark-1.3-contributor": {"id":"muse-spark-1.3-contributor","name":"Muse Spark 1.3","provider":{"npm":"@ai-sdk/openai"}},
    "minimax-m2.7": {"id":"minimax-m2.7","name":"MiniMax M2.7","provider":{"npm":"@ai-sdk/anthropic"}},
    "glm-5.3": {"id":"glm-5.3","name":"GLM-5.3"}}},
  "openrouter": {"id":"openrouter","models": {
    "gpt-6-luna": {"id":"gpt-6-luna","name":"GPT-6 Luna","provider":{"npm":"@ai-sdk/openai"}}}}
}`

func writeCatalog(t *testing.T, data string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// #1215: an OpenCode Go provider saved with its chat URL alone (added by
// hand, as the reporter's "OpenCode Go 1x" was) sent every model to
// /zen/go/v1/chat/completions, and Go answers GPT, Grok, Muse Spark and
// MiniMax there with 400 "Model does not support this protocol." whatever
// API the client spoke. Each model now goes on the API Go serves it on,
// as models.dev's opencode-go says, from all three of magpie's APIs; GLM
// keeps going on the client's own.
func TestOpenCodeGoModelsGoOnTheirOwnAPI(t *testing.T) {
	fresh(t)
	writeCatalog(t, openCodeGoCatalog)
	if err := provider.Save(provider.Provider{ID: "opencode-go", Name: "OpenCode Go 1x", Key: "k", Chat: "https://opencode.ai/zen/go/v1",
		Models: []string{"gpt-6-luna", "grok-4.7", "muse-spark-1.3-contributor", "minimax-m2.7", "glm-5.3"}}); err != nil {
		t.Fatal(err)
	}
	// Go's own answers: the model's API takes it (here 401, as Go answers
	// a request with no key it knows, 2026-10-08), the other two turn it
	// away as Go does with a key — at the v2 paths of the inference API's
	// /inference/go the models are read at now (#4740)
	own := map[string]string{"gpt-6-luna": "/inference/go/openai/v1/responses", "grok-4.7": "/inference/go/openai/v1/responses",
		"muse-spark-1.3-contributor": "/inference/go/openai/v1/responses", "minimax-m2.7": "/inference/go/anthropic/v1/messages"}
	var mu sync.Mutex
	asked := map[string][]string{}
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		model := modelOf(b)
		mu.Lock()
		asked[model] = append(asked[model], r.URL.Host+r.URL.Path)
		mu.Unlock()
		reply := func(code int, body string) (*http.Response, error) {
			return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		}
		if r.Method != http.MethodPost {
			return reply(404, `{}`)
		}
		if want, ok := own[model]; ok && r.URL.Path != want {
			return reply(400, `{"type":"error","error":{"type":"invalid_request_error","message":"Model does not support this protocol."}}`)
		}
		return reply(401, `{"type":"error","error":{"type":"AuthError","message":"Missing API key."}}`)
	})}
	clients := map[string]string{
		"/v1/chat/completions": `{"model":"opencode-go/%s","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`,
		"/v1/messages":         `{"model":"opencode-go/%s","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`,
		"/v1/responses":        `{"model":"opencode-go/%s","input":"hi","max_output_tokens":16}`,
	}
	for _, model := range []string{"gpt-6-luna", "grok-4.7", "muse-spark-1.3-contributor", "minimax-m2.7", "glm-5.3"} {
		for path, body := range clients {
			mu.Lock()
			asked = map[string][]string{}
			mu.Unlock()
			req := httptest.NewRequest("POST", path, strings.NewReader(strings.Replace(body, "%s", model, 1)))
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			mu.Lock()
			got := asked[model]
			mu.Unlock()
			if len(got) == 0 {
				t.Errorf("%s on %s: upstream never asked (%d %s)", model, path, rec.Code, rec.Body)
				continue
			}
			want := "opencode.ai" + own[model]
			if own[model] == "" {
				// GLM: on the API the client spoke, moved to the
				// inference API's /inference/go (#4740)
				if path == "/v1/messages" {
					want = "opencode.ai/inference/go/anthropic/v1/messages"
				} else {
					want = "opencode.ai/inference/go/openai/v1" + strings.TrimPrefix(path, "/v1")
				}
			}
			if got[0] != want {
				t.Errorf("%s on %s: asked %v, want %s first (%d %s)", model, path, got, want, rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "does not support this protocol") {
				t.Errorf("%s on %s: %d %s", model, path, rec.Code, rec.Body)
			}
		}
	}
}

// openCodePaidCatalog is models.dev's opencode and opencode-go as they
// name the APIs paid models are served on (2026-11-01): GPT through
// @ai-sdk/openai, Claude and MiniMax through @ai-sdk/anthropic, Gemini
// through @ai-sdk/google, the rest on the chat completions the provider's
// npm names.
const openCodePaidCatalog = `{
  "opencode": {"id":"opencode","npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/inference/v1","models": {
    "kimi-k3": {"id":"kimi-k3","name":"Kimi K3"},
    "gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5","provider":{"npm":"@ai-sdk/openai"}},
    "claude-sonnet-5": {"id":"claude-sonnet-5","name":"Claude Sonnet 5","provider":{"npm":"@ai-sdk/anthropic"}},
    "gemini-3.5-flash": {"id":"gemini-3.5-flash","name":"Gemini 3.5 Flash","provider":{"npm":"@ai-sdk/google"}}}},
  "opencode-go": {"id":"opencode-go","npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/inference/go/openai/v1","models": {
    "kimi-k3": {"id":"kimi-k3","name":"Kimi K3"},
    "gpt-6-luna": {"id":"gpt-6-luna","name":"GPT-6 Luna","provider":{"npm":"@ai-sdk/openai"}},
    "minimax-m2.7": {"id":"minimax-m2.7","name":"MiniMax M2.7","provider":{"npm":"@ai-sdk/anthropic"}}}}
}`

// #4740: a paid OpenCode model — Zen's, asked with a key, and Go's — is
// asked where OpenCode 2 reads it, on the paths the v2 catalog gives its
// provider, not the 1.x /zen paths the free move (#3491) left it on. A
// Gemini model is asked on the Google route the catalog names.
func TestOpenCodePaidOnV2Paths(t *testing.T) {
	fresh(t)
	writeCatalog(t, openCodePaidCatalog)
	for _, p := range []provider.Provider{
		{ID: "zen", Name: "Zen", Key: "sk-zen",
			Chat: "https://opencode.ai/zen/v1", Responses: "https://opencode.ai/zen/v1", Anthropic: "https://opencode.ai/zen",
			Models: []string{"kimi-k3", "gpt-5.5", "claude-sonnet-5", "gemini-3.5-flash"}},
		{ID: "go", Name: "Go", Key: "sk-go",
			Chat: "https://opencode.ai/zen/go/v1", Responses: "https://opencode.ai/zen/go/v1", Anthropic: "https://opencode.ai/zen/go",
			Models: []string{"kimi-k3", "gpt-6-luna", "minimax-m2.7"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	asked := map[string][]string{}
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		model := modelOf(b)
		if model == "" {
			// Gemini's generateContent names the model in its path
			if i := strings.LastIndex(r.URL.Path, "/models/"); i >= 0 {
				model = strings.Split(r.URL.Path[i+len("/models/"):], ":")[0]
			}
		}
		mu.Lock()
		asked[model] = append(asked[model], r.URL.Host+r.URL.Path)
		mu.Unlock()
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Missing API key."}}`)), Request: r}, nil
	})}
	for _, c := range []struct{ model, path, body, want string }{ // model is the client's name; the upstream is asked the bare id
		{"zen/kimi-k3", "/v1/chat/completions",
			`{"model":"zen/kimi-k3","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`,
			"opencode.ai/inference/openai/v1/chat/completions"},
		{"zen/gpt-5.5", "/v1/responses",
			`{"model":"zen/gpt-5.5","input":"hi","max_output_tokens":16}`,
			"opencode.ai/inference/openai/v1/responses"},
		{"zen/claude-sonnet-5", "/v1/messages",
			`{"model":"zen/claude-sonnet-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`,
			"opencode.ai/inference/anthropic/v1/messages"},
		{"zen/gemini-3.5-flash", "/v1beta/models/zen/gemini-3.5-flash:streamGenerateContent",
			`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			"opencode.ai/inference/google/v1beta/models/gemini-3.5-flash:streamGenerateContent"},
		{"go/kimi-k3", "/v1/chat/completions",
			`{"model":"go/kimi-k3","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`,
			"opencode.ai/inference/go/openai/v1/chat/completions"},
		{"go/gpt-6-luna", "/v1/responses",
			`{"model":"go/gpt-6-luna","input":"hi","max_output_tokens":16}`,
			"opencode.ai/inference/go/openai/v1/responses"},
		{"go/minimax-m2.7", "/v1/messages",
			`{"model":"go/minimax-m2.7","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`,
			"opencode.ai/inference/go/anthropic/v1/messages"},
	} {
		mu.Lock()
		asked = map[string][]string{}
		mu.Unlock()
		req := httptest.NewRequest("POST", c.path, strings.NewReader(c.body))
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		mu.Lock()
		got := asked[strings.TrimPrefix(c.model, "zen/")]
		got = append(got, asked[strings.TrimPrefix(c.model, "go/")]...)
		mu.Unlock()
		if len(got) == 0 {
			t.Errorf("%s on %s: upstream never asked (%d %s)", c.model, c.path, rec.Code, rec.Body)
			continue
		}
		if got[0] != c.want {
			t.Errorf("%s on %s: asked %v, want %s first (%d %s)", c.model, c.path, got, c.want, rec.Code, rec.Body)
		}
	}
}

// The rule is OpenCode's gateway's alone: another relay saved with its
// chat URL alone keeps it, and its models stay on it, though models.dev
// says OpenAI's SDK serves the same model elsewhere.
func TestOtherRelayKeepsItsOneAPI(t *testing.T) {
	fresh(t)
	writeCatalog(t, openCodeGoCatalog)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Catalog: "openrouter", Chat: "https://relay.test/v1", Models: []string{"gpt-6-luna"}}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Responses != "" || p.Anthropic != "" || p.Catalog != "openrouter" {
		t.Fatalf("relay changed: %+v", p)
	}
	if apis := p.APIs("gpt-6-luna"); apis != nil {
		t.Errorf("relay gpt-6-luna APIs %v, want none named", apis)
	}
	if got := provider.NativeEndpoints(provider.Entry{Provider: *p, Model: "gpt-6-luna", ID: "relay/gpt-6-luna"}); len(got) != 1 || got[0] != "/v1/chat/completions" {
		t.Errorf("relay native_endpoints %v", got)
	}
}

// When the upstream says the model isn't served on the API asked and no
// other is left, the error says which URL was asked, and which API the
// vendor's list serves the model on that the provider has no URL for
// (#1215 asked for the error to be diagnosable). A key in the query isn't
// repeated.
func TestWrongAPINote(t *testing.T) {
	fresh(t)
	writeCatalog(t, openCodeGoCatalog)
	// as a provider is before normalize fills in Go's other APIs: one the
	// user cleared by hand, say, or a remote's
	p := provider.Provider{ID: "opencode-go", Name: "OpenCode Go", Catalog: "opencode-go", Chat: "https://opencode.ai/zen/go/v1"}
	u, _ := url.Parse("https://opencode.ai/zen/go/v1/chat/completions?key=secret")
	res := &http.Response{Request: &http.Request{URL: u}}
	got := wrongAPINote(p, "gpt-6-luna", res, nil)
	for _, want := range []string{"asked at https://opencode.ai/zen/go/v1/chat/completions", "gpt-6-luna is served on /v1/responses, which OpenCode Go has no URL for"} {
		if !strings.Contains(got, want) {
			t.Errorf("note %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "secret") {
		t.Errorf("note repeats the query: %q", got)
	}
	if got := wrongAPINote(p, "glm-5.3", res, nil); got != " (asked at https://opencode.ai/zen/go/v1/chat/completions)" {
		t.Errorf("glm note %q", got)
	}
	// a relay with its chat URL alone that names Responses in Chinese:
	// the note says to set that URL (#3766)
	ar := provider.Provider{ID: "agentrouter", Name: "AgentRouter", Chat: "https://agentrouter.org/v1"}
	u, _ = url.Parse("https://agentrouter.org/v1/chat/completions")
	res = &http.Response{Request: &http.Request{URL: u}}
	for _, body := range []string{agentRouterRefusal, agentRouterEscaped} {
		if got := wrongAPINote(ar, "gpt-5.6-sol", res, []byte(body)); got != " (asked at https://agentrouter.org/v1/chat/completions; gpt-5.6-sol is served on /v1/responses, which AgentRouter has no URL for)" {
			t.Errorf("AgentRouter note %q for %s", got, body)
		}
	}
}
