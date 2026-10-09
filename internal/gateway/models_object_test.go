package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #1438: the published pair must not promise replies bigger than the
// window beside them — models.dev lists some subscription models' output
// above the window the live backend reports (grok-4.7's 500000 output
// against a 256000 window), and a client sizing its compaction budget
// from max_output_tokens asks the upstream for what it cannot deliver.
// The agents' own limit was clamped at the context in #338; the gateway's
// /v1/models publishes the same rule.
func TestModelsObjectKeepsOutputWithinTheWindow(t *testing.T) {
	e := provider.Entry{ID: "grok/grok-4.7", Context: 256000, Output: 500000}
	m := modelObject(e)
	if m["max_output_tokens"] != 256000 {
		t.Fatalf("output above the window published as-is: %v", m["max_output_tokens"])
	}
	if m["context_window"] != 256000 || m["max_input_tokens"] != 256000 {
		t.Fatalf("window changed: %v", m)
	}

	// an output within the window, an unknown window, and an unknown
	// output all keep their shapes
	same := provider.Entry{ID: "m", Context: 128000, Output: 64000}
	if got := modelObject(same)["max_output_tokens"]; got != 64000 {
		t.Fatalf("output within the window changed: %v", got)
	}
	noWindow := provider.Entry{ID: "m", Output: 500000}
	if got := modelObject(noWindow)["max_output_tokens"]; got != 500000 {
		t.Fatalf("output with unknown window changed: %v", got)
	}
	noOutput := provider.Entry{ID: "m", Context: 256000}
	if _, ok := modelObject(noOutput)["max_output_tokens"]; ok {
		t.Fatal("unknown output grew a limit")
	}
}
