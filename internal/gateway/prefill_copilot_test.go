package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// copilotGeminiPrefillRefusal is the 400 GitHub Copilot returns when a
// Gemini request's conversation ends with the assistant's message
// (#6847). The wording is the report's, verbatim.
const copilotGeminiPrefillRefusal = `{"error":{"message":"Copilot: The last message in a Gemini request must not be from the assistant. Ensure the conversation ends with a user or tool message.","type":"invalid_request_error"}}`

// Copilot's Gemini wording of an assistant-last refusal must be learned as
// a prefill refusal: the client's reconnect (an input ending with the
// assistant's partial message) is re-asked once with a `Continue.` user turn
// instead of the 400 reaching the client, and later requests go so at once.
func TestCopilotGeminiPrefillRefusal(t *testing.T) {
	for _, c := range []struct {
		proto      provider.Protocol
		path, body string
	}{
		{provider.Chat, "/v1/chat/completions", `{"model":"up/m","messages":[
			{"role":"user","content":"Reply with exactly: OK"},{"role":"assistant","content":"O"}]}`},
		{provider.Responses, "/v1/responses", `{"model":"up/m","input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Reply with exactly: OK"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"O"}]}]}`},
	} {
		t.Run(string(c.proto), func(t *testing.T) {
			f := &noPrefill{proto: c.proto, refuse: copilotGeminiPrefillRefusal}
			post := prefillServer(t, f)
			code, body := post(c.path, c.body)
			if code != 200 || !strings.Contains(body, `"OK"`) {
				t.Fatalf("status %d, want the model's answer after a Continue retry: %s", code, body)
			}
			if f.calls() != 2 || f.lastUser(1) != continueText {
				t.Fatalf("calls %d, retry's last user turn %q: want the refusal learned and re-asked with Continue", f.calls(), f.lastUser(1))
			}
			// remembered: the next such request goes with a user turn at once
			if code, _ := post(c.path, c.body); code != 200 || f.calls() != 3 || f.lastUser(2) != continueText {
				t.Fatalf("status %d, calls %d: the refusal wasn't remembered", code, f.calls())
			}
		})
	}
}
