package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A Chat client that names the effort in both fields — reasoning_effort and
// new-api's/OpenRouter's top-level reasoning object — is asked once more
// without the object Mistral's strict body turns away (422
// extra_forbidden, Tanimodori #6327), and not sent it again. The level
// reasoning_effort carries goes as it is.
func TestTopLevelReasoningNeverReachesMistral(t *testing.T) {
	f := &fake{t: t, reply: chatWholeOK, ctype: "application/json", refuse: mistralRefuse}
	up := setup(t, provider.Chat, f)
	chatProvider(t, up.URL, provider.Provider{ID: "mistral", Preset: "mistral", Models: []string{"mistral-medium-3.5"}})
	srv := New()
	code, body := postTo(t, srv, "/v1/chat/completions", `{"model":"mistral/mistral-medium-3.5","stream":false,
		"reasoning_effort":"max",
		"reasoning":{"effort":"max"},
		"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, `"No."`) {
		t.Fatalf("%d after %d calls: %s", code, f.calls, body)
	}
	if f.calls != 2 {
		t.Errorf("%d upstream calls, want the refused one and the retry", f.calls)
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(f.got, &sent); err != nil {
		t.Fatalf("Mistral was sent %s", f.got)
	}
	if _, ok := sent["reasoning"]; ok {
		t.Errorf("the top-level reasoning object reached Mistral: %s", f.got)
	}
	if string(sent["reasoning_effort"]) != `"max"` {
		t.Errorf("the effort reasoning_effort carries: %s", f.got)
	}
	// the next request goes without the object from the start
	calls := f.calls
	if code, body := postTo(t, srv, "/v1/chat/completions", `{"model":"mistral/mistral-medium-3.5","stream":false,
		"reasoning_effort":"max",
		"reasoning":{"effort":"max"},
		"messages":[{"role":"user","content":"hi"}]}`); code != 200 || f.calls-calls != 1 || !strings.Contains(body, `"No."`) {
		t.Errorf("the learned refusal didn't hold: %d after %d calls: %s", code, f.calls-calls, body)
	}
	var again map[string]json.RawMessage
	json.Unmarshal(f.got, &again)
	if _, ok := again["reasoning"]; ok {
		t.Errorf("the object was sent again: %s", f.got)
	}
}
