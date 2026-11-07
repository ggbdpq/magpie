package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// The /meta tier fields captured from Copilot CLI 1.0.107, 2026-11-07.
const fusionMeta = `{"auto":{"default_tier":"balance","tiers":[
 {"id":"balance","display_name":"Standard","type":"auto","tier_level":"standard","status":{"enabled":true,"tier_picker_enabled":true}},
 {"id":"hydrafusion","display_name":"High","type":"fusion","tier_level":"high","status":{"enabled":true,"tier_picker_enabled":true}},
 {"id":"hydrafusion-max","display_name":"Max","type":"fusion","tier_level":"max","status":{"enabled":true,"tier_picker_enabled":true}}
]}}`

func TestCopilotFusionStandard(t *testing.T) {
	signIn(t)
	ForgetCopilotForTest()
	t.Cleanup(ForgetCopilotForTest)
	var mu sync.Mutex
	var routed, sent []string
	refuse := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/models":
			io.WriteString(w, `{"data":[{"id":"gpt-4.1","model_picker_enabled":true,"capabilities":{"type":"chat"}}]}`)
		case "/meta":
			if r.Header.Get("X-GitHub-Api-Version") != "2026-09-21" {
				t.Error("Fusion metadata requested with the wrong API version")
			}
			io.WriteString(w, fusionMeta)
		case "/auto":
			var q struct {
				Fusion bool   `json:"fusion"`
				Tier   string `json:"tier"`
				Prompt string `json:"prompt"`
			}
			json.NewDecoder(r.Body).Decode(&q)
			if refuse {
				w.WriteHeader(403)
				io.WriteString(w, `{"error":{"message":"Fusion is unavailable"}}`)
				return
			}
			kind := "legacy"
			if q.Fusion {
				kind = "fusion"
			}
			routed = append(routed, kind+" "+q.Tier+" "+q.Prompt)
			io.WriteString(w, `{"session_token":"`+kind+`-session","selected_model":{"id":"gpt-4.1","supported_endpoints":["/chat/completions"]},"expires_at":`+mustFusionJSON(time.Now().Add(time.Hour).Unix())+`}`)
		case "/chat/completions":
			b, _ := io.ReadAll(r.Body)
			sent = append(sent, bodyModel(b)+" "+r.Header.Get("Copilot-Session-Token"))
			if strings.HasPrefix(r.Header.Get("Copilot-Session-Token"), "fusion-") && r.Header.Get("X-GitHub-Api-Version") != "2026-08-01" {
				t.Error("Fusion inference requested with the wrong API version")
			}
			io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected fallback to %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer api.Close()
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
	}))
	defer tokens.Close()
	old := CopilotTokenURL
	CopilotTokenURL = tokens.URL
	t.Cleanup(func() { CopilotTokenURL = old })
	p, _ := find(All(), "copilot")
	ms, err := p.Fetch(context.Background())
	if err != nil || !slices.ContainsFunc(ms, func(m catalog.Model) bool { return m.ID == "fusion" && m.Name == "Copilot Fusion Standard" }) {
		t.Fatalf("Fusion Standard missing from models: %+v, %v", ms, err)
	}
	send := func(pick string) {
		t.Helper()
		ctx, model, err := p.ResolveAuto(WithAutoPrompt(context.Background(), "test prompt"), pick)
		if err != nil || model != "gpt-4.1" {
			t.Fatalf("%s resolved to %q: %v", pick, model, err)
		}
		body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`)
		req, _ := http.NewRequest("POST", api.URL+"/chat/completions", nil)
		if err := p.Sign(ctx, req, Chat, body); err != nil {
			t.Fatal(err)
		}
		SetBody(req, body)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	send("auto")
	send("fusion")
	send("auto")
	send("fusion")
	mu.Lock()
	if !slices.Equal(routed, []string{"legacy balance test prompt", "fusion balance test prompt"}) || !slices.Equal(sent, []string{"gpt-4.1 legacy-session", "gpt-4.1 fusion-session", "gpt-4.1 legacy-session", "gpt-4.1 fusion-session"}) {
		t.Errorf("routes: %q; requests: %q", routed, sent)
	}
	mu.Unlock()
	// A provider's model test signs the virtual id directly, without the
	// gateway's ResolveAuto step. It must keep the Fusion context too.
	req, _ := http.NewRequest("POST", api.URL+"/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, []byte(`{"model":"fusion","messages":[]}`)); err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(req.Body)
	if bodyModel(b) != "gpt-4.1" || req.Header.Get("Copilot-Session-Token") != "fusion-session" || req.Header.Get("X-GitHub-Api-Version") != "2026-08-01" {
		t.Fatalf("direct Fusion signing lost routing or billing context")
	}
	ctx, model, err := p.ResolveAuto(context.Background(), "fusion")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Retry(ctx, []byte(`{"model":"`+model+`"}`), 400, []byte(`{"error":{"message":"Requested model not available for session"}}`)) {
		t.Fatal("an expired Fusion session must be renewed")
	}
	// Signing with the old request context renews Fusion, leaving legacy
	// Auto's cached pick alone.
	if err := p.Sign(ctx, req, Chat, []byte(`{"model":"gpt-4.1","messages":[]}`)); err != nil {
		t.Fatal(err)
	}
	send("auto")
	mu.Lock()
	if len(routed) != 3 || routed[2] != "fusion balance hi" {
		t.Errorf("session renewal crossed routing modes: %q", routed)
	}
	refuse = true
	mu.Unlock()
	ForgetCopilotForTest()
	// Manual selection can be refused while Fusion may still route to the
	// same model; that refusal must not poison Fusion's routing session.
	p.Retry(context.Background(), []byte(`{"model":"gpt-4.1"}`), 400, []byte(`{"error":{"code":"model_not_supported"}}`))
	mu.Lock()
	refuse = false
	mu.Unlock()
	if _, model, err := p.ResolveAuto(context.Background(), "fusion"); err != nil || model != "gpt-4.1" {
		t.Fatalf("manual selection refusal poisoned Fusion: %q %v", model, err)
	}
	ForgetCopilotForTest()
	mu.Lock()
	refuse = true
	mu.Unlock()
	if _, _, err := p.ResolveAuto(context.Background(), "fusion"); err == nil || !strings.Contains(err.Error(), "Fusion is unavailable") {
		t.Fatalf("Fusion refusal must fail without legacy fallback: %v", err)
	}
}

func TestCopilotFusionMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, meta string
		status     int
		want       bool
	}{
		{"enabled", fusionMeta, 200, true},
		{"disabled", strings.ReplaceAll(fusionMeta, `"enabled":true`, `"enabled":false`), 200, false},
		{"hidden", strings.ReplaceAll(fusionMeta, `"tier_picker_enabled":true`, `"tier_picker_enabled":false`), 200, false},
		{"picker omitted", strings.ReplaceAll(fusionMeta, `,"tier_picker_enabled":true`, ""), 200, true},
		{"legacy balance", strings.ReplaceAll(fusionMeta, `"tier_level":"standard"`, `"tier_level":"balance"`), 200, false},
		{"malformed", `{"auto":`, 200, false},
		{"old service", `{}`, 404, false},
		{"unavailable", fusionMeta, 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.meta)
			}))
			defer api.Close()
			s := copilotSession{Token: "test"}
			s.Endpoints.API = api.URL
			if got := copilotHasFusion(context.Background(), copilotApp{}, s); got != tc.want {
				t.Errorf("Fusion availability = %v, want %v", got, tc.want)
			}
		})
	}
}

func mustFusionJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
