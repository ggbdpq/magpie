package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A plugin whose upstream only admits the official Codex client can have
// its config hook set codexClient on the provider: Codex's identity
// headers (its User-Agent, originator, the session and thread, and the
// x-codex- ones) then go on to it as Codex sent them, as they do to a
// provider without an account (#4197, BB-fat — the Codex side of #2817).
// The plugin's own key still signs the request, and Codex's ChatGPT
// sign-in and attestation never go on (codexHeader keeps them off).
// Without the flag nothing is forwarded, as the plugins whose requests are
// pinned to magpie's own shape (Cursor's, Grok's) have them.
func TestPluginCodexClientHeaders(t *testing.T) {
	t.Setenv("FAKE_CODEX_CLIENT", "1")
	var mu sync.Mutex
	var got []http.Header
	pid := besideFake(t, "fakeco", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		got = append(got, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"fake-resp"}}`,
			`data: {"type":"response.completed","response":{"id":"r1","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`))
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"`+pid+`/fake-resp","input":"hi","stream":false}`))
	req.Header.Set("User-Agent", "codex_cli_rs/0.164.0 (Mac OS 26.6.0; arm64)")
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("session-id", "3f2a9c1e-7b64-4d0f-a111-52f0f26e6f5a")
	req.Header.Set("thread-id", "b59e0e32-6c1a-4a2e-9f4c-6d3aa2f6b7c1")
	req.Header.Set("x-codex-window-id", "42")
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("asked %d times", len(got))
	}
	h := got[0]
	for k, want := range map[string]string{
		"User-Agent":        "codex_cli_rs/0.164.0 (Mac OS 26.6.0; arm64)",
		"Originator":        "codex_cli_rs",
		"Session-Id":        "3f2a9c1e-7b64-4d0f-a111-52f0f26e6f5a",
		"Thread-Id":         "b59e0e32-6c1a-4a2e-9f4c-6d3aa2f6b7c1",
		"X-Codex-Window-Id": "42",
	} {
		if h.Get(k) != want {
			t.Errorf("the vendor got %s %q; want %q", k, h.Get(k), want)
		}
	}
	if h.Get("Authorization") == "" {
		t.Error("the vendor got no Authorization: the plugin's own sign-in never went off")
	}
}

// Without the flag a plugin's requests keep magpie's own shape: a Codex
// caller's identity headers stay off it, as the plugins pinned to them
// (Cursor's, Grok's) have their requests said (plugin_grok_parity_test).
func TestPluginWithoutCodexClientFlagSendsNoCodexHeaders(t *testing.T) {
	var mu sync.Mutex
	var got []http.Header
	pid := besideFake(t, "fakeco", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		got = append(got, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"fake-resp"}}`,
			`data: {"type":"response.completed","response":{"id":"r1","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`))
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"`+pid+`/fake-resp","input":"hi","stream":false}`))
	req.Header.Set("User-Agent", "codex_cli_rs/0.164.0 (Mac OS 26.6.0; arm64)")
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("session-id", "3f2a9c1e-7b64-4d0f-a111-52f0f26e6f5a")
	req.Header.Set("x-codex-window-id", "42")
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("asked %d times", len(got))
	}
	h := got[0]
	for _, k := range []string{"Originator", "Session-Id", "Thread-Id", "X-Codex-Window-Id"} {
		if v := h.Get(k); v != "" {
			t.Errorf("the vendor got %s %q without the flag", k, v)
		}
	}
	if ua := h.Get("User-Agent"); strings.HasPrefix(ua, "codex") {
		t.Errorf("the vendor got Codex's User-Agent %q without the flag", ua)
	}
}
