package gateway

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// #7889: an Anthropic plugin (like agy-cli 0.1.141+) that adds an image-only
// model with @ai-sdk/openai-compatible and output ["image"] must still
// translate /v1/chat/completions requests for its models (including unlisted
// effort-suffixed ids like gemini-4-hydrogen-medium) to /v1/messages and emit
// OpenAI Chat Completions SSE — never pass raw Anthropic SSE through — while
// still drawing with the image-only model on /v1/images/generations.
func TestAnthropicPluginWithImageOnlyModelTranslatesChatAndDraws(t *testing.T) {
	bun := testenv.Bun(t)
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)

	var mu sync.Mutex
	var paths []string
	up := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/images/generations") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}]}`)
			return
		}
		// Like agy-cli's bridge: any non-image request returns Anthropic Messages SSE.
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		for _, ev := range []string{
			`{"type":"message_start","message":{"id":"msg_7889","type":"message","role":"assistant","model":"gemini-4-hydrogen-medium","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		} {
			name := strings.SplitN(strings.TrimPrefix(ev, `{"type":"`), `"`, 2)[0]
			_, _ = io.WriteString(w, "event: "+name+"\ndata: "+ev+"\n\n")
		}
	})
	testenv.FakeVendor(t, up)

	pluginDir := t.TempDir()
	pluginPath := filepath.Join(pluginDir, "index.js")
	const pluginJS = `
const ID = "agy-img-test"
export const TestPlugin = async () => ({
  config: async (cfg) => {
    cfg.provider = cfg.provider ?? {}
    cfg.provider[ID] = {
      name: "Agy Image Test",
      npm: "@ai-sdk/anthropic",
      api: process.env.FAKE_BASE,
      models: {
        "gemini-4-hydrogen": {
          name: "Gemini 4 Hydrogen",
          provider: { npm: "@ai-sdk/anthropic" },
          reasoning: true,
          variants: { low: {}, medium: {}, high: {} },
          modalities: { input: ["text", "image"], output: ["text"] },
          limit: { context: 1048576, output: 65536 },
        },
        "gemini-3.1-flash-image": {
          name: "Gemini 3.1 Flash Image",
          provider: { npm: "@ai-sdk/openai-compatible" },
          reasoning: false,
          tool_call: false,
          attachment: true,
          modalities: { input: ["text", "image"], output: ["image"] },
          limit: { context: 0, output: 0 },
        },
      },
    }
  },
  auth: {
    provider: ID,
    methods: [{ type: "api", label: "Key" }],
    loader: async () => ({
      apiKey: "k",
      baseURL: process.env.FAKE_BASE,
      fetch: (input, init) => {
        const headers = new Headers(init?.headers ?? {})
        if (process.env.FAKE_RUN) headers.set("X-Fake-Run", process.env.FAKE_RUN)
        return fetch(input, { ...init, headers })
      },
    }),
  },
})
`
	if err := os.WriteFile(pluginPath, []byte(pluginJS), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := plugin.Add(ctx, pluginPath); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.APIKey(ctx, "agy-img-test", 0, nil, "k1", plugin.NewAccount); err != nil {
		t.Fatal(err)
	}
	pid := provider.PluginID("agy-img-test")

	// 1. Streaming /v1/chat/completions for effort-suffixed/unlisted model id
	// must translate to /messages and return OpenAI Chat Completions SSE.
	s := New()
	h := s.Handler()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
		`{"model":"`+pid+`/gemini-4-hydrogen-medium","stream":true,"messages":[{"role":"user","content":"Reply with OK"}]}`,
	))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer magpie")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat status %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if strings.Contains(body, "event: message_start") {
		t.Fatalf("received raw Anthropic SSE on /v1/chat/completions:\n%s", body)
	}
	if !strings.Contains(body, `"chat.completion.chunk"`) || !strings.Contains(body, `"OK"`) || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected translated OpenAI Chat Completions SSE, got:\n%s", body)
	}
	mu.Lock()
	chatPaths := append([]string(nil), paths...)
	paths = nil
	mu.Unlock()
	if len(chatPaths) != 1 || !strings.HasSuffix(chatPaths[0], "/messages") {
		t.Fatalf("upstream paths for chat request = %v, want [/v1/messages]", chatPaths)
	}

	// 2. Image generation on the image-only model must still work via /images/generations.
	code, ans, raw := postImages(t, s, "/v1/images/generations", "application/json",
		`{"model":"`+pid+`/gemini-3.1-flash-image","prompt":"a magpie"}`)
	if code != http.StatusOK || len(ans.Data) != 1 {
		t.Fatalf("images status %d: %s", code, raw)
	}
	mu.Lock()
	imgPaths := append([]string(nil), paths...)
	mu.Unlock()
	if len(imgPaths) != 1 || !strings.HasSuffix(imgPaths[0], "/images/generations") {
		t.Fatalf("upstream paths for image request = %v, want [/v1/images/generations]", imgPaths)
	}
}
