package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A client reading /v1/models without X-Magpie-Drawers — Cindy's model
// discovery, say (#1914) — is told what tiers Codex here is offered on
// each model, as another magpie asking with the header is (#1234): a
// model whose tiers hold priority is what tells such a client its Fast
// mode. A model no tier is offered on carries none, and nor does a
// provider of another kind.
func TestModelsOfferTheirTiersWithoutDrawers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token": claims(map[string]any{"email": "plus@example.com",
			"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}}),
		"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "who": "acct-plus"}),
		"refresh_token": "r-acct-plus", "account_id": "acct-plus"}}
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	b, _ := json.Marshal(auth)
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), b, 0o600)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"models":[{"slug":"gpt-6-luna","visibility":"list","service_tiers":[{"id":"priority","name":"Fast","description":"1.5x speed, increased usage"}]},{"slug":"glm-5","visibility":"list"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-6-luna"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: up.URL}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	tiersOf := func(drawers bool) map[string][]string {
		t.Helper()
		req := httptest.NewRequest("GET", "/v1/models", nil)
		if drawers {
			req.Header.Set(provider.DrawersHeader, "1")
		}
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		var list struct {
			Data []struct {
				ID    string `json:"id"`
				Tiers []struct {
					ID string `json:"id"`
				} `json:"service_tiers"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		out := map[string][]string{}
		for _, d := range list.Data {
			var ids []string
			for _, tier := range d.Tiers {
				ids = append(ids, tier.ID)
			}
			out[d.ID] = ids
		}
		return out
	}

	// without the header, as Cindy reads the list: the model the account
	// is offered Fast on says so, and no other does
	got := tiersOf(false)
	if ts := got["codex/gpt-6-luna"]; len(ts) != 1 || ts[0] != "priority" {
		t.Errorf("codex/gpt-6-luna without the header: %v, want [priority]", ts)
	}
	if ts := got["codex/glm-5"]; len(ts) != 0 {
		t.Errorf("codex/glm-5 without the header: %v, want none", ts)
	}
	if ts := got["fake/m1"]; len(ts) != 0 {
		t.Errorf("fake/m1 without the header: %v, want none", ts)
	}

	// another magpie, asking with the header, reads the same as before
	got = tiersOf(true)
	if ts := got["codex/gpt-6-luna"]; len(ts) != 1 || ts[0] != "priority" {
		t.Errorf("codex/gpt-6-luna with the header: %v, want [priority]", ts)
	}
}
