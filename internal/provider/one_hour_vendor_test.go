package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// stated is a price the user gives a model, in dollars per million.
func stated(in, out, read, write float64) settings.ModelPrice {
	return settings.ModelPrice{Input: new(in), Output: new(out), CacheRead: new(read), CacheWrite: new(write)}
}

// A price the user gives one provider's Claude model stands as they gave
// it: leaving the 1-hour write out means what that provider charges —
// Copilot bills a 1-hour write at its 5-minute price (#3435) — and 2×
// input only on Anthropic's, which is the rule it bills by. A price of the
// model from any provider, like a bare id's, is the maker's rule.
func TestEffectivePriceOneHourScopedToAnthropic(t *testing.T) {
	priceHome(t)
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"anthropic":{"id":"anthropic","models":{"claude-sonnet-5-5":{"id":"claude-sonnet-5-5","cost":{
		"input":3,"output":15,"cache_read":0.15,"cache_write":3.75}}}}}`
	if err := os.WriteFile(catalog.CachePath(), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{
		"copilot/claude-opus-5-5":     stated(4, 20, 0.2, 5),
		"anthropic/claude-sonnet-5-5": stated(3, 15, 0.15, 3.75),
		"claude/claude-sonnet-5-5":    stated(3, 15, 0.15, 3.75),
		"*/claude-haiku-5-5":          stated(1, 5, 0.05, 1.25),
	}}); err != nil {
		t.Fatal(err)
	}
	if pr, _ := EffectivePrice("copilot", "claude-opus-5-5"); pr.CacheWrite1h != 0 {
		t.Errorf("copilot 1h = %v, want none: its 1-hour writes are at its 5-minute price", pr.CacheWrite1h)
	}
	if pr, _ := EffectivePrice("anthropic", "claude-sonnet-5-5"); pr.CacheWrite1h != 6 {
		t.Errorf("anthropic 1h = %v, want 6 (2× input)", pr.CacheWrite1h)
	}
	if pr, _ := EffectivePrice("claude", "claude-sonnet-5-5"); pr.CacheWrite1h != 6 {
		t.Errorf("claude account 1h = %v, want 6 (2× input)", pr.CacheWrite1h)
	}
	if pr, _ := EffectivePrice("relay", "claude-haiku-5-5"); pr.CacheWrite1h != 2 {
		t.Errorf("any provider 1h = %v, want 2 (the maker's 2× input)", pr.CacheWrite1h)
	}
	// a model that is not Claude's is untouched
	if pr, _ := EffectivePrice("copilot", "claude-opus-5-5"); pr.Input != 4 || pr.CacheWrite != 5 {
		t.Errorf("copilot price = %+v, want what the user gave", pr)
	}
}
