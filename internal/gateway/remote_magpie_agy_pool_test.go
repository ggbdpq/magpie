package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #7971 (nested magpie): a remote magpie's Antigravity account ran out of
// the Claude pool's third-party quota; the same account's Gemini model,
// whose own pool was fine, was answered the held 429 without being asked.
// The pool words tell the pools apart whatever serves them, a remote
// magpie's relay included.
func TestRemoteMagpieAgyPoolQuotaRestsItsPoolOnly(t *testing.T) {
	fresh(t)
	var asked []string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		asked = append(asked, req.Model)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Model, "claude") {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"Antigravity quota reached: Third-party quota reached. Please switch to Gemini models or upgrade your subscription to increase your limits. Resets in 4h55m21s."}}`)
			return
		}
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"from `+req.Model+`"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "remote-magpie", Name: "Remote Magpie", Preset: provider.RemoteMagpiePreset, Key: "k",
		Chat: remote.URL + "/v1", Models: []string{"agy-cli/claude-opus-5-5", "agy-cli/gemini-4-neon"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	if code, body := postAs(t, s, "", `{"model":"remote-magpie/agy-cli/claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`); code != 429 {
		t.Fatalf("claude on the remote: %d %s", code, body)
	}
	if r, ok := restOf("remote-magpie"); ok {
		t.Fatalf("the whole remote magpie rests for the Claude pool's quota: %+v", r)
	}
	if _, ok := restOf("remote-magpie/agy-cli/claude-opus-5-5"); !ok {
		t.Fatalf("the claude model doesn't rest: %v", rests())
	}
	asked = nil
	if code, body := postAs(t, s, "", `{"model":"remote-magpie/agy-cli/gemini-4-neon","messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "from agy-cli/gemini-4-neon") {
		t.Fatalf("gemini on the same account: %d %s (asked %v)", code, body, asked)
	}
}

// The same through a provider of one's own that points at another
// magpie's gateway (#7971) — no preset, so nothing says it is a magpie
// but the reply's own X-Magpie-Provider, which noteMember puts on every
// reply. The pool words are Antigravity's own, relayed, and rest that
// model's pool only, not the account.
func TestOwnProviderToAMagpieAgyPoolQuotaRestsItsPoolOnly(t *testing.T) {
	fresh(t)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Magpie-Provider", "agy-cli")
		w.Header().Set("X-Magpie-Model", req.Model)
		if strings.Contains(req.Model, "claude") {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"Antigravity quota reached: Third-party quota reached. Please switch to Gemini models or upgrade your subscription to increase your limits. Resets in 4h55m21s."}}`)
			return
		}
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"from `+req.Model+`"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "other-magpie", Name: "Other Magpie", Key: "k",
		Chat: remote.URL + "/v1", Models: []string{"agy-cli/claude-opus-5-5", "agy-cli/gemini-4-neon"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	if code, body := postAs(t, s, "", `{"model":"other-magpie/agy-cli/claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`); code != 429 {
		t.Fatalf("claude on the other magpie: %d %s", code, body)
	}
	if r, ok := restOf("other-magpie"); ok {
		t.Fatalf("the whole provider rests for the Claude pool's quota: %+v", r)
	}
	if _, ok := restOf("other-magpie/agy-cli/claude-opus-5-5"); !ok {
		t.Fatalf("the claude model doesn't rest: %v", rests())
	}
	if code, body := postAs(t, s, "", `{"model":"other-magpie/agy-cli/gemini-4-neon","messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "from agy-cli/gemini-4-neon") {
		t.Fatalf("gemini on the same account: %d %s", code, body)
	}
}
