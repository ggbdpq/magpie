package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// Captured on Ark's Coding Plan on 2026-11-10: K3.1 Pro accepts thinking
// disabled only with temperature 0.6. Omitting it (or sending null) still
// gets 400, although the same probe without thinking succeeds. ZCode sends
// output_config.effort but no thinking, so the Messages relay adds disabled.
func TestArkKimiThinkingOffTemperature(t *testing.T) {
	for _, c := range []struct {
		name, path, fields, extra, temp, thinking string
		to                                        provider.Protocol
		group, otherHost, otherModel, refused     bool
	}{
		{name: "ZCode through a group", to: provider.Anthropic, path: "/v1/messages", fields: `"output_config":{"effort":"max"}`, temp: "0.6", thinking: "disabled", group: true},
		{name: "Messages default", to: provider.Anthropic, path: "/v1/messages", temp: "0.6", thinking: "disabled"},
		{name: "Messages null", to: provider.Anthropic, path: "/v1/messages", fields: `"temperature":null,"thinking":{"type":"disabled"}`, temp: "0.6", thinking: "disabled"},
		{name: "client sampling is already stripped", to: provider.Anthropic, path: "/v1/messages", fields: `"temperature":1,"thinking":{"type":"disabled"}`, temp: "0.6", thinking: "disabled"},
		{name: "thinking stays enabled", to: provider.Anthropic, path: "/v1/messages", fields: `"thinking":{"type":"enabled","budget_tokens":1024}`, thinking: "enabled"},
		{name: "extra body enables thinking", to: provider.Anthropic, path: "/v1/messages", extra: `{"!thinking":{"type":"enabled"}}`, thinking: "enabled"},
		{name: "extra body disables thinking", to: provider.Anthropic, path: "/v1/messages", fields: `"thinking":{"type":"enabled","budget_tokens":1024}`, extra: `{"!thinking":{"type":"disabled"}}`, temp: "0.6", thinking: "disabled"},
		{name: "extra body null", to: provider.Anthropic, path: "/v1/messages", extra: `{"!temperature":null}`, temp: "0.6", thinking: "disabled"},
		{name: "extra body numeric value wins", to: provider.Anthropic, path: "/v1/messages", extra: `{"!temperature":0.2}`, temp: "0.2", thinking: "disabled", refused: true},
		{name: "extra body working override", to: provider.Anthropic, path: "/v1/messages", extra: `{"!temperature":1,"!thinking":{"type":"enabled"}}`, temp: "1", thinking: "enabled"},
		{name: "Chat default stays default", to: provider.Chat, path: "/v1/chat/completions"},
		{name: "Responses default stays default", to: provider.Responses, path: "/v1/responses"},
		{name: "Chat relay", to: provider.Chat, path: "/v1/chat/completions", fields: `"thinking":{"type":"disabled"}`, temp: "0.6", thinking: "disabled"},
		{name: "Responses relay", to: provider.Responses, path: "/v1/responses", fields: `"thinking":{"type":"disabled"}`, temp: "0.6", thinking: "disabled"},
		{name: "Chat translated to Messages", to: provider.Anthropic, path: "/v1/chat/completions", fields: `"reasoning_effort":"none"`, temp: "0.6", thinking: "disabled"},
		{name: "Responses translated to Messages", to: provider.Anthropic, path: "/v1/responses", fields: `"reasoning":{"effort":"none"}`, temp: "0.6", thinking: "disabled"},
		{name: "Messages translated to Chat keeps its defaults", to: provider.Chat, path: "/v1/messages", fields: `"thinking":{"type":"disabled"}`},
		{name: "another vendor", to: provider.Anthropic, path: "/v1/messages", thinking: "disabled", otherHost: true},
		{name: "another Ark model", to: provider.Anthropic, path: "/v1/messages", thinking: "disabled", otherModel: true},
		{name: "count tokens", to: provider.Anthropic, path: "/v1/messages/count_tokens"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			noRests()
			t.Cleanup(noRests)
			host, model := arkHost, "kimi-k3.1-pro"
			if c.otherHost {
				host = "relay.example"
			}
			if c.otherModel {
				model = "doubao-seed-2.1-pro"
			}
			p := provider.Provider{ID: "ark-test", Name: "Ark test", Key: "test", Models: []string{model}}
			base := "https://" + host + "/api/coding"
			switch c.to {
			case provider.Anthropic:
				p.Anthropic = base
				if strings.HasPrefix(c.path, "/v1/messages") {
					p.Chat = base + "/v3" // the real preset exposes both APIs
				}
			case provider.Chat:
				p.Chat = base + "/v3"
			case provider.Responses:
				p.Responses = base + "/v3"
			}
			if c.extra != "" {
				if err := json.Unmarshal([]byte(c.extra), &p.ExtraBody); err != nil {
					t.Fatal(err)
				}
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			ref := p.ID + "/" + model
			if c.group {
				if err := provider.SaveGroup(provider.Group{ID: "kimi-k3", Name: "Kimi K3", Members: []string{ref}, Routing: provider.Ordered}); err != nil {
					t.Fatal(err)
				}
				ref = "group/kimi-k3"
			}
			var mu sync.Mutex
			var sent []byte
			calls := 0
			s := New()
			s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
				b, err := io.ReadAll(r.Body)
				if err != nil {
					return nil, err
				}
				mu.Lock()
				sent = append([]byte(nil), b...)
				calls++
				mu.Unlock()
				status, ctype, reply := 200, "text/event-stream", anthropicReply()
				count := strings.HasSuffix(r.URL.Path, "/count_tokens")
				if count {
					ctype, reply = "application/json", `{"input_tokens":3}`
				} else {
					switch c.to {
					case provider.Chat:
						reply = chatReply
					case provider.Responses:
						reply = arkSearchReply
					}
					if host == arkHost && model == "kimi-k3.1-pro" && gjson.GetBytes(b, "thinking.type").String() == "disabled" && gjson.GetBytes(b, "temperature").Float() != 0.6 {
						status, ctype, reply = 400, "application/json", "{\"error\":{\"type\":\"invalid_request_error\",\"message\":\"`temperature` only 0.6 is allowed for this model\"}}"
					}
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {ctype}}, Body: io.NopCloser(strings.NewReader(reply))}, nil
			})}
			input := `"messages":[{"role":"user","content":"Reply OK."}],"max_tokens":128000`
			if c.path == "/v1/responses" {
				input = `"input":"Reply OK.","max_output_tokens":128000`
			}
			fields := ""
			if c.fields != "" {
				fields = "," + c.fields
			}
			body := fmt.Sprintf(`{"model":%q,"stream":true,%s%s}`, ref, input, fields)
			status, reply := postTo(t, s, c.path, body)
			mu.Lock()
			got, n := string(sent), calls
			mu.Unlock()
			if c.refused {
				if !strings.Contains(reply, "only 0.6") {
					t.Fatalf("explicit extraBody override lost: %d %s", status, reply)
				}
			} else if status != 200 || strings.Contains(reply, "only 0.6") {
				t.Fatalf("request rejected: %d %s; upstream body: %s", status, reply, got)
			}
			if n != 1 {
				t.Errorf("upstream called %d times, want one", n)
			}
			if v := gjson.Get(got, "temperature").Raw; v != c.temp {
				t.Errorf("temperature = %q, want %q; sent %s", v, c.temp, got)
			}
			if v := gjson.Get(got, "thinking.type").String(); v != c.thinking {
				t.Errorf("thinking = %q, want %q; sent %s", v, c.thinking, got)
			}
			if v := gjson.Get(got, "model").String(); v != model {
				t.Errorf("model = %q, want %q", v, model)
			}
		})
	}
}
