package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

func TestGeminiRelayModelBody(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	seen := make(chan string, 16)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		model := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1beta/models/"), ":streamGenerateContent")
		if gjson.GetBytes(body, "model").String() != model {
			http.Error(w, `{"error":{"message":"Model is unsupported and no wildcard routing rule is configured"}}`, http.StatusBadRequest)
			return
		}
		if r.URL.RawQuery != "alt=sse" || r.Header.Get("x-goog-api-key") != "relay-key" || gjson.GetBytes(body, "stream").Exists() || !gjson.GetBytes(body, "contents").IsArray() {
			http.Error(w, "wrong Gemini request shape", http.StatusBadRequest)
			return
		}
		seen <- model
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"native reply\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":4,\"candidatesTokenCount\":2,\"totalTokenCount\":6}}\n\n")
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "relay-key", Gemini: up.URL + "/v1beta", GeminiModelInBody: true, Models: []string{"gemini-one", "gemini-two"}}); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"gemini-one", "gemini-two"} {
		if err := provider.SetUpstreamName("relay/"+model, "vendor/"+model); err != nil {
			t.Fatal(err)
		}
	}
	for _, model := range []string{"gemini-one", "gemini-two"} {
		for _, call := range []struct{ path, body string }{
			{"/v1/responses", fmt.Sprintf(`{"model":"relay/%s","input":"hi","stream":false}`, model)},
			{"/v1/responses", fmt.Sprintf(`{"model":"relay/%s","input":"hi","stream":true}`, model)},
			{"/v1/messages", fmt.Sprintf(`{"model":"relay/%s","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`, model)},
			{"/v1/messages", fmt.Sprintf(`{"model":"relay/%s","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, model)},
			{"/v1beta/models/relay/" + model + ":generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`},
			{"/v1beta/models/relay/" + model + ":streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`},
		} {
			code, body := post(t, call.path, call.body)
			if code != http.StatusOK || !strings.Contains(body, "native reply") {
				t.Fatalf("%s for %s: %d %s", call.path, model, code, body)
			}
			select {
			case got := <-seen:
				if got != "vendor/"+model {
					t.Fatalf("%s: asked %q, want %q", call.path, got, "vendor/"+model)
				}
			default:
				t.Fatalf("%s did not reach the Gemini relay", call.path)
			}
		}
	}
}
