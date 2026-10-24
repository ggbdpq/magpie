package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// A plugin provider's price rate saves and prices like a custom one's
// (#3517): the editor sent it to provider/save, the save answered success,
// and yet the rate was in neither providers.json — Save keeps only some of
// a signed-in account's fields — nor the provider as found, whose picks
// withPicks lays over the sign-in, so calls went out billed at the maker's
// price.
func TestPluginPriceRateSavesAndPrices(t *testing.T) {
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", "")
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	// models.dev has nothing for Devin's own models: their price is the
	// plugin's alone; GPT-5.5 is one it prices, for the custom provider
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "openai":{"id":"openai","models":{"gpt-5.5":{"id":"gpt-5.5","cost":{"input":4,"output":20,"cache_read":0.4,"cache_write":1}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	spec := "@magpie-community/opencode-devin-auth"
	write := func(name string, v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		os.MkdirAll(settings.Dir(), 0o700)
		if err := os.WriteFile(filepath.Join(settings.Dir(), name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("plugins.json", map[string]any{"plugins": []map[string]any{{"spec": spec}}})
	write("plugin-auth.json", map[string]map[string]any{"devin": {"type": "api", "key": "devin-key-1234"}})
	write("migrations.json", map[string]any{"devin": map[string]any{"state": "plugin", "package": spec}})
	// Devin's own price, as the plugin lists it
	plugin.UseCached([]plugin.Provider{{ID: "devin", Spec: spec, Name: "Devin",
		Methods:  []plugin.Method{{Type: "api", Label: "API key"}},
		Accounts: []plugin.Account{{Key: "devin", Type: "api"}},
		Models: listedModels(t, `[
			{"id": "swe-1.7-lightning", "name": "SWE-1.7 Lightning", "cost": {"input": 2.5, "output": 12.5, "cache": {"read": 1, "write": 0}}}]`),
	}})
	t.Cleanup(func() { plugin.UseCached(nil) })
	if !Moved("devin") {
		t.Fatal("devin not moved")
	}

	// the editor's save of the plugin provider, the rate filled in
	if err := Save(Provider{ID: "devin", Models: []string{"swe-1.7-lightning"}, PriceRate: 0.15}); err != nil {
		t.Fatal(err)
	}
	var stored *Provider
	for i, p := range load().Providers {
		if p.ID == "devin" {
			stored = &load().Providers[i]
		}
	}
	if stored == nil {
		t.Fatal("the saved plugin provider is not in providers.json")
	}
	if stored.PriceRate != 0.15 {
		t.Errorf("providers.json: priceRate %v, want 0.15", stored.PriceRate)
	}
	// the provider as found: the picks reach the sign-in
	p, err := Find("devin")
	if err != nil {
		t.Fatal(err)
	}
	if p.PriceRate != 0.15 {
		t.Errorf("found: priceRate %v, want 0.15", p.PriceRate)
	}
	// and usage is billed at the rate: Devin's $2.5/$12.5 at 0.15
	if pr, ok := EffectivePrice("devin", "swe-1.7-lightning"); !ok || pr.Input != 0.375 || pr.Output != 1.875 {
		t.Errorf("price: %+v, %v; want $0.375/$1.875, Devin's own at the rate", pr, ok)
	}

	// the same save keeps a custom provider's rate as it always has
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k", Models: []string{"gpt-5.5"}, PriceRate: 0.5}); err != nil {
		t.Fatal(err)
	}
	if pr, ok := EffectivePrice("relay", "gpt-5.5"); !ok || pr.Input != 2 || pr.Output != 10 {
		t.Errorf("custom: %+v, %v; want $2/$10, the catalog's at the custom rate", pr, ok)
	}
}
