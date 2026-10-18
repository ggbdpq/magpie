package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// A relay that serves only Claude Code (#179: packy's "only accessible via
// the official Claude CLI") gets Claude Code's own headers and body as it
// sent them, the provider's key in place of magpie's
func TestClaudeCodeHeadersReachAnthropicRelay(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"msg","type":"message","content":[]}`}
	up := setup(t, provider.Anthropic, f)
	var query string
	up.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		f.ServeHTTP(w, r)
	})
	claude := http.Header{
		"User-Agent":        {"claude-cli/2.1.0 (external, cli)"},
		"X-App":             {"cli"},
		"Anthropic-Version": {"2023-06-01"},
		"Anthropic-Beta":    {"claude-code-20250219,interleaved-thinking-2025-05-14"},
		"Anthropic-Dangerous-Direct-Browser-Access": {"true"},
		"X-Stainless-Lang":                          {"js"},
		"X-Stainless-Package-Version":               {"0.60.0"},
		"X-Stainless-Retry-Count":                   {"0"},
		"X-Claude-Code-Session-Id":                  {"ses-1"},
		"Authorization":                             {"Bearer " + Token},
		"X-Api-Key":                                 {Token},
	}
	body := `{"model":"m1","max_tokens":5,"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],"metadata":{"user_id":"user_abc_account__session_123"},"messages":[{"role":"user","content":"hi"}]}`
	send := func(path string, h http.Header) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		for k, v := range h {
			req.Header[k] = v
		}
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}

	send("/v1/messages?beta=true", claude)
	for k, v := range claude {
		if k == "Authorization" || k == "X-Api-Key" {
			continue
		}
		if got := f.head.Get(k); got != v[0] {
			t.Errorf("%s: got %q, want %q", k, got, v[0])
		}
	}
	if a, k := f.head.Get("Authorization"), f.head.Get("X-Api-Key"); a != "Bearer k" || k != "k" {
		t.Errorf("auth: %q %q", a, k)
	}
	if query != "beta=true" {
		t.Errorf("query %q", query)
	}
	for _, want := range []string{`"You are Claude Code, Anthropic's official CLI for Claude."`, `"metadata":{"user_id":"user_abc_account__session_123"}`} {
		if !bytes.Contains(f.got, []byte(want)) {
			t.Errorf("body lost %s: %s", want, f.got)
		}
	}

	// a provider with no key of its own still never sees magpie's
	p, _ := provider.Find("fake")
	p.Key = ""
	provider.Save(*p)
	send("/v1/messages", claude)
	if a, k := f.head.Get("Authorization"), f.head.Get("X-Api-Key"); a != "" || k != "" {
		t.Errorf("magpie's key leaked: %q %q", a, k)
	}
	if f.head.Get("X-App") != "cli" {
		t.Errorf("x-app: %v", f.head)
	}

	// another agent's request goes as magpie's
	send("/v1/messages?beta=true", http.Header{"User-Agent": {"opencode/1.0"}, "X-App": {"other"}, "X-Stainless-Lang": {"js"}})
	if ua := f.head.Get("User-Agent"); !strings.HasPrefix(ua, "magpie/") || f.head.Get("X-App") != "" || f.head.Get("X-Stainless-Lang") != "" || query != "" {
		t.Errorf("other agent: %q %v %q", ua, f.head, query)
	}
}

// A plugin's provider whose model speaks Anthropic and whose loader gave a
// key is relayed a Claude Code request as a custom provider is (#2817): an
// upstream that serves the official client alone (#179) knows it by its own
// headers and ?beta=true, which go through the plugin's fetch as they go to
// a relay, the plugin's own key signing the request as ever.
func TestClaudeCodeHeadersReachAnthropicPlugin(t *testing.T) {
	bun := testenv.Bun(t)
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)

	var mu sync.Mutex
	var head http.Header
	var query, got string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		head, query, got = r.Header, r.URL.RawQuery, string(b)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []string{
			`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"fake-claude","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi from claude"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", e)
		}
	}))
	defer up.Close()
	t.Setenv("FAKE_BASE", up.URL+"/v1")

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.APIKey(ctx, "fakeco", 0, nil, "k1", plugin.NewAccount); err != nil {
		t.Fatal(err)
	}
	if p, err := provider.Find("fakeco"); err != nil || !p.IsPlugin() {
		t.Fatalf("Find = %+v, %v", p, err)
	}

	claude := http.Header{
		"User-Agent":               {"claude-cli/2.1.0 (external, cli)"},
		"X-App":                    {"cli"},
		"Anthropic-Beta":           {"claude-code-20250219,interleaved-thinking-2025-05-14"},
		"X-Stainless-Lang":         {"js"},
		"X-Claude-Code-Session-Id": {"ses-1"},
		"Anthropic-Dangerous-Direct-Browser-Access": {"true"},
	}
	// streamed, as Claude Code asks: a plugin's account only streams, so the
	// request is relayed as it was sent
	body := `{"model":"fakeco/fake-claude","stream":true,"max_tokens":5,"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],"metadata":{"user_id":"user_abc_account__session_123"},"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages?beta=true", strings.NewReader(body))
	for k, v := range claude {
		req.Header[k] = v
	}
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hi from claude") {
		t.Fatalf("the reply didn't come back: %s", rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	for _, k := range []string{"User-Agent", "X-App", "Anthropic-Beta", "Anthropic-Dangerous-Direct-Browser-Access", "X-Stainless-Lang", "X-Claude-Code-Session-Id"} {
		if h := head.Get(k); h != claude.Get(k) {
			t.Errorf("%s: got %q, want %q", k, h, claude.Get(k))
		}
	}
	if a := head.Get("Authorization"); a != "Bearer k1" {
		t.Errorf("the plugin's own key: %q", a)
	}
	// the body went through as Claude Code sent it, its system line and
	// metadata kept, as to a relay
	for _, want := range []string{`"You are Claude Code, Anthropic's official CLI for Claude."`, `"metadata":{"user_id":"user_abc_account__session_123"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("body lost %s: %s", want, got)
		}
	}
	if query != "beta=true" {
		t.Errorf("query %q, want beta=true", query)
	}
}
