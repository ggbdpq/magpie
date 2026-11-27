package agent

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// The effort levels dsh's effort slider, or a hand, put on a model magpie
// has none for (a model no catalog magpie reads lists any for, given: true
// on the Providers page) survive the route being written again: the round
// rebuilt the route's models without them, and the 30s round and the
// slider wrote each other's work away, a window of seconds every round in
// which a request carrying a level failed (#7998).
func TestDshKeepsEffortsMagpieHasNoneFor(t *testing.T) {
	home, _, web := dshRouteHome(t)
	slider := "# Your patch layer for this dsh profile.\n" +
		"- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n" +
		"      magpie:\n        displayName: Magpie\n        apiKeyEnv: " + dshKeyRef + "\n        api: openai-completions\n        baseURL: " + gatewayV1() + "\n        models:\n" +
		"          - id: deepseek/pro\n            name: pro · DeepSeek\n            reasoningEfforts:\n              low: low\n              high: high\n" +
		"          - id: deepseek/flash\n            name: flash · DeepSeek\n" +
		"- id: agent-default-model\n  config:\n    provider: magpie\n    model: \"deepseek/pro\"\n    reasoningEffort: high\n"
	os.WriteFile(web, []byte(slider), 0o644)
	a := dsh(home)

	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(web)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "reasoningEfforts:\n              low: low\n              high: high") {
		t.Fatalf("the slider's levels went with the round:\n%s", b)
	}

	// and the round stops writing what nothing changed: the slider is not
	// woken to put its levels back every few seconds
	first := append([]byte{}, b...)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if b, err = os.ReadFile(web); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, b) {
		t.Fatalf("the next round wrote the route again:\n%s", b)
	}
}
