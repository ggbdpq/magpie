package cli

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestGeminiRelayModelBodyPair(t *testing.T) {
	p := provider.Provider{ID: "relay", Gemini: "https://relay.example/v1beta"}
	for _, value := range []string{"body", "path", "body", "default"} {
		if err := applyPairs(&p, []string{"gemini-model=" + value}); err != nil {
			t.Fatal(err)
		}
		if p.GeminiModelInBody != (value == "body") {
			t.Fatalf("gemini-model=%s: body=%v", value, p.GeminiModelInBody)
		}
	}
	if err := applyPairs(&p, []string{"gemini-model=unknown"}); err == nil {
		t.Fatal("an unknown model location was accepted")
	}
}
