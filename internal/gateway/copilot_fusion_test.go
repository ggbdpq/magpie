package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestCopilotFusionThroughGateway(t *testing.T) {
	var mu sync.Mutex
	var prompts, sent []string
	copilotFake(t, "gho_fusion_gateway", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/models":
			io.WriteString(w, copilotStudentModels)
		case "/auto":
			var q struct {
				Fusion bool   `json:"fusion"`
				Tier   string `json:"tier"`
				Prompt string `json:"prompt"`
			}
			json.NewDecoder(r.Body).Decode(&q)
			if !q.Fusion || q.Tier != "balance" {
				t.Errorf("Fusion not requested: %+v", q)
			}
			prompts = append(prompts, q.Prompt)
			io.WriteString(w, `{"session_token":"fusion-session","selected_model":{"id":"gpt-4.1","supported_endpoints":["/chat/completions"]},"expires_at":0}`)
		case "/chat/completions":
			b, _ := io.ReadAll(r.Body)
			sent = append(sent, modelOf(b)+" "+r.Header.Get("Copilot-Session-Token")+" "+r.Header.Get("X-GitHub-Api-Version"))
			if strings.Contains(string(b), `"stream":true`) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"id\":\"c1\",\"model\":\"gpt-4.1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello from fusion\"}}]}\n\n")
				io.WriteString(w, "data: {\"id\":\"c1\",\"model\":\"gpt-4.1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
				return
			}
			io.WriteString(w, `{"id":"c1","model":"gpt-4.1","choices":[{"index":0,"message":{"role":"assistant","content":"hello from fusion"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":3}}`)
		default:
			t.Errorf("unexpected fallback to %s", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	s := New()
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"copilot/fusion","messages":[{"role":"user","content":"Use Fusion"}]}`},
		{"/v1/responses", `{"model":"copilot/fusion","input":"Use Fusion"}`},
		{"/v1/messages", `{"model":"copilot/fusion","max_tokens":100,"messages":[{"role":"user","content":"Use Fusion"}]}`},
	} {
		code, body := postTo(t, s, tc.path, tc.body)
		if code != 200 || !strings.Contains(body, "hello from fusion") {
			t.Fatalf("%s: %d %s", tc.path, code, body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 1 || prompts[0] != "Use Fusion" || len(sent) != 3 {
		t.Fatalf("prompts: %q, sent: %q", prompts, sent)
	}
	for _, request := range sent {
		if request != "gpt-4.1 fusion-session 2026-08-01" {
			t.Errorf("inference lost its Fusion session: %s", request)
		}
	}
	if call := s.Recent()[0]; call.Usage.Served != "gpt-4.1" {
		t.Errorf("served model lost: %+v", call)
	}
}
